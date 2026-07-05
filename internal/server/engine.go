// Package server exposes the engine over HTTP + SSE (inferred spec 02 §HTTP
// surface): task submission with overrides, event streaming with full replay,
// resume, and hot-reloadable config. The TUI (spec 08) is a pure client.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	Holder *config.Holder
	Bus    *events.MemBus
	Store  *state.FileStore

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
		running: map[string]bool{},
	}
	e.rebuild(cfg)
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
	e.tools = thinking.NewToolRegistry(&thinking.WebSearch{})
}

// ReloadConfig validates and applies new config; invalid YAML never takes
// effect and the old config stays live.
func (e *Engine) ReloadConfig(raw []byte) error {
	cfg, err := config.Parse(raw)
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

// Submit creates a task, kicks off its pipeline in the background, and
// returns the task ID immediately.
func (e *Engine) Submit(text string, ov receptionist.Overrides) (string, error) {
	if text == "" {
		return "", fmt.Errorf("empty task text")
	}
	taskID := newTaskID()
	st := pipeline.NewState(taskID, text)

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
	go e.run(st, receptionist.Overrides{})
	return nil
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
			Payload: map[string]any{"message": "intake failed: " + err.Error()}})
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

func newTaskID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}
