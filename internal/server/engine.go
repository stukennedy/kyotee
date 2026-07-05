// Package server exposes the engine over HTTP + SSE (spec 02 §3): task
// submission with validated overrides, event streaming with full replay,
// resume, provider listing, and hot-reloadable config. The TUI and the
// Skill shim are pure clients of this surface.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/stukennedy/kyotee/internal/config"
	"github.com/stukennedy/kyotee/internal/council"
	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/pipeline"
	"github.com/stukennedy/kyotee/internal/provider"
	"github.com/stukennedy/kyotee/internal/receptionist"
	"github.com/stukennedy/kyotee/internal/state"
	"github.com/stukennedy/kyotee/internal/thinking"
)

// Engine owns the long-lived pieces (bus, store, config holder, registry)
// and runs each task's receptionist→executor flow in a goroutine.
type Engine struct {
	Holder     *config.Holder
	Bus        *events.MemBus
	Store      *state.FileStore
	ConfigPath string // source file for POST /v1/config/reload ("" = defaults)

	elog *eventLog

	mu       sync.Mutex
	registry provider.Registry
	embedder council.Embedder
	tools    *thinking.ToolRegistry
	running  map[string]bool
}

func NewEngine(cfg *config.Config, store *state.FileStore) *Engine {
	e := &Engine{
		Holder:  config.NewHolder(cfg),
		Bus:     events.NewBus(),
		Store:   store,
		elog:    newEventLog(store.Dir),
		running: map[string]bool{},
	}
	e.rebuild(cfg)
	// Persist every event to the per-task ndjson log (spec 02 §3): replay
	// survives engine restarts. Subscribe synchronously so no head event
	// published between engine construction and follower startup is lost.
	ch, _ := e.Bus.Subscribe("")
	go e.elog.drain(ch)
	return e
}

// rebuild swaps registry/embedder/tools after a config hot-reload.
func (e *Engine) rebuild(cfg *config.Config) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.registry = config.BuildRegistry(cfg)
	if emb := config.BuildEmbedder(cfg); emb != nil {
		e.embedder = emb
	} else {
		e.embedder = nil
	}
	e.tools = config.BuildTools(cfg)
}

// ReloadConfig validates and applies new config; invalid config never takes
// effect and the old config stays live. In-flight tasks keep the snapshot
// they captured at intake.
func (e *Engine) ReloadConfig(raw []byte) error {
	cfg, err := config.Parse(raw)
	if err != nil {
		return err
	}
	e.Holder.Set(cfg)
	e.rebuild(cfg)
	return nil
}

// ReloadConfigFromDisk re-reads the config file (POST /v1/config/reload).
func (e *Engine) ReloadConfigFromDisk() error {
	cfg, err := config.Load(e.ConfigPath)
	if err != nil {
		return err
	}
	e.Holder.Set(cfg)
	e.rebuild(cfg)
	return nil
}

func (e *Engine) receptionist() *receptionist.Receptionist {
	e.mu.Lock()
	defer e.mu.Unlock()
	return &receptionist.Receptionist{
		Cfg:      e.Holder,
		Registry: e.registry,
		Tools:    e.tools,
		Embedder: e.embedder,
	}
}

// Submit validates the override, creates a task, kicks off its pipeline in
// the background, and returns the task ID. An invalid override rejects the
// task before it starts (spec 07 §4).
func (e *Engine) Submit(text string, ov receptionist.Overrides) (string, error) {
	if text == "" {
		return "", fmt.Errorf("empty task text")
	}
	if err := ov.Validate(e.Holder.Get()); err != nil {
		return "", err
	}
	taskID := newTaskID()
	st := pipeline.NewState(taskID, text)
	// Persist the submit-time overrides so resume re-applies them — an
	// override-escalated council task must not re-route as solo on resume.
	if ovJSON, err := json.Marshal(ov); err == nil {
		st.Meta["overrides"] = string(ovJSON)
	}

	e.mu.Lock()
	e.running[taskID] = true
	e.mu.Unlock()

	go e.run(st, ov)
	return taskID, nil
}

// Resume reloads a persisted task and re-runs its remaining stages.
func (e *Engine) Resume(taskID string) error {
	e.mu.Lock()
	if e.running[taskID] {
		e.mu.Unlock()
		return fmt.Errorf("task %s is already running", taskID)
	}
	e.running[taskID] = true
	e.mu.Unlock()

	st, err := e.Store.Load(taskID)
	if err != nil {
		e.mu.Lock()
		delete(e.running, taskID)
		e.mu.Unlock()
		return fmt.Errorf("load task %s: %w", taskID, err)
	}
	// After an engine restart the bus starts at Seq 0; continue numbering
	// after the persisted event log so replay + live never collide.
	if persisted := e.elog.read(taskID); len(persisted) > 0 {
		e.Bus.SeedSeq(taskID, persisted[len(persisted)-1].Seq+1)
	}
	// Re-apply the submit-time overrides persisted in Meta.
	var ov receptionist.Overrides
	if raw := st.Meta["overrides"]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &ov)
	}
	go e.run(st, ov)
	return nil
}

func (e *Engine) Running(taskID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running[taskID]
}

func (e *Engine) run(st *pipeline.State, ov receptionist.Overrides) {
	defer func() {
		e.mu.Lock()
		delete(e.running, st.TaskID)
		e.mu.Unlock()
	}()

	emit := events.EmitterFor(e.Bus, st.TaskID)
	emit(events.Event{Kind: events.KindTaskReceived, Actor: "receptionist",
		Payload: map[string]any{"text": st.Original}})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	stages, err := e.receptionist().Intake(ctx, st, ov, emit)
	if err != nil {
		emit(events.Event{Kind: events.KindError,
			Payload: map[string]any{"message": "intake failed: " + err.Error(), "terminal": true}})
		_ = e.Store.Save(st)
		return
	}

	ex := &pipeline.Executor{Store: e.Store, Bus: e.Bus}
	if _, err := ex.Execute(ctx, stages, st); err != nil {
		// Executor already emitted error / budget events and persisted state.
		return
	}
}

// TaskInfo is the list-endpoint summary row.
type TaskInfo struct {
	TaskID   string  `json:"task_id"`
	Original string  `json:"original"`
	Final    string  `json:"final"`
	Running  bool    `json:"running"`
	SpentUSD float64 `json:"spent_usd"`
}

func (e *Engine) Tasks() ([]TaskInfo, error) {
	ids, err := e.Store.List()
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	runningSnapshot := make(map[string]bool, len(e.running))
	for id := range e.running {
		runningSnapshot[id] = true
	}
	e.mu.Unlock()

	out := make([]TaskInfo, 0, len(ids))
	for _, id := range ids {
		st, err := e.Store.Load(id)
		if err != nil {
			continue
		}
		out = append(out, TaskInfo{
			TaskID: id, Original: st.Original, Final: st.Final,
			Running: runningSnapshot[id], SpentUSD: st.Budget.SpentUSD,
		})
	}
	return out, nil
}

// ProviderInfo is the /v1/providers row (spec 02 §3).
type ProviderInfo struct {
	Name        string  `json:"name"`
	Vendor      string  `json:"vendor"`
	Tools       bool    `json:"tools"`
	Reasoning   bool    `json:"reasoning"`
	MaxContext  int     `json:"max_context"`
	InputPer1M  float64 `json:"input_usd_per_1m"`
	OutputPer1M float64 `json:"output_usd_per_1m"`
}

func (e *Engine) Providers() []ProviderInfo {
	e.mu.Lock()
	reg := e.registry
	e.mu.Unlock()

	var out []ProviderInfo
	for _, p := range reg.List() {
		caps := p.Capabilities()
		in, outUSD := p.CostPer1M()
		out = append(out, ProviderInfo{
			Name: p.Name(), Vendor: p.Vendor(),
			Tools: caps.Tools, Reasoning: caps.Reasoning, MaxContext: caps.MaxContext,
			InputPer1M: in, OutputPer1M: outUSD,
		})
	}
	return out
}

func newTaskID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}
