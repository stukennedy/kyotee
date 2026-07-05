// Package tui is the Tooey front-end (spec 08): a pure SSE consumer and HTTP
// action poster. It holds no orchestration logic — every behaviour it
// triggers is observed back through the event stream.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/stukennedy/tooey/app"
	"github.com/stukennedy/tooey/component"
	"github.com/stukennedy/tooey/input"

	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/receptionist"
)

// Messages delivered into Update.
type (
	// SSEMsg carries one engine event.
	SSEMsg struct{ Event events.Event }
	// SSEStatusMsg reports stream connect/disconnect.
	SSEStatusMsg struct{ Connected bool }
	// TaskCreatedMsg is the POST /v1/tasks result.
	TaskCreatedMsg struct {
		TaskID string
		Err    error
	}
	// ConfigFetchedMsg is the GET /v1/config result.
	ConfigFetchedMsg struct {
		YAML string
		Err  error
	}
	// ConfigSavedMsg is the PUT /v1/config result.
	ConfigSavedMsg struct{ Err error }
	// TasksListedMsg is the GET /v1/tasks result (for resume).
	TasksListedMsg struct {
		Tasks []TaskSummary
		Err   error
	}
	// ResumedMsg is the POST resume result.
	ResumedMsg struct {
		TaskID string
		Err    error
	}
)

type TaskSummary struct {
	TaskID   string  `json:"task_id"`
	Original string  `json:"original"`
	Final    string  `json:"final"`
	Running  bool    `json:"running"`
	SpentUSD float64 `json:"spent_usd"`
}

// MemberView tracks one council member's evolving position (spec 08 §2).
type MemberView struct {
	Position   string
	Choice     string
	Confidence float64
	Round      int
}

type BrainTurn struct {
	Role  string
	Round int
	Text  string
}

// overlay identifies which modal is active.
type overlay int

const (
	overlayNone overlay = iota
	overlayConfig
	overlayResume
	overlayOverride
)

type Model struct {
	Client *Client

	Input component.TextInput

	// Observed engine state, updated purely from events.
	TaskID    string
	Class     map[string]any // task.classified payload
	Strategy  string
	Pipeline  []string
	Stage     string
	ThinkMode string
	ToolCheck string
	ToolCalls []string
	Brains    []BrainTurn
	Referee   string
	Council   map[string]*MemberView
	Members   []string // stable pane order
	Consensus string
	Synthesis string
	Final     string
	SpentUSD  float64
	LimitUSD  float64
	WarnPct   float64
	Log       []string
	seen      map[int64]bool // Seq de-dup across reconnects

	Connected bool
	Status    string

	// Overlays.
	Active      overlay
	ConfigInput component.TextInput
	Tasks       []TaskSummary
	TaskSel     int

	// Override & escalate (spec 08 §5): applied to the NEXT submitted task.
	Override receptionist.Overrides

	quitArmed    bool
	width        int
	height       int
	cancelStream context.CancelFunc // stops the previous task's SSE stream
}

// startStream cancels any previous task's stream and returns a Sub for the
// new one.
func (m *Model) startStream(taskID string) app.Sub {
	if m.cancelStream != nil {
		m.cancelStream()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelStream = cancel
	return m.Client.StreamSub(ctx, taskID)
}

func NewModel(client *Client) *Model {
	m := &Model{
		Client:  client,
		Input:   component.NewTextInput("describe a task and press Enter…"),
		Council: map[string]*MemberView{},
		seen:    map[int64]bool{},
		Status:  "ready",
	}
	m.Input.Focused = true
	return m
}

// reset clears per-task view state when a new task starts.
func (m *Model) reset(taskID string) {
	m.TaskID = taskID
	m.Class = nil
	m.Strategy, m.Stage, m.ThinkMode, m.ToolCheck, m.Consensus, m.Synthesis, m.Final = "", "", "", "", "", "", ""
	m.Pipeline = nil
	m.ToolCalls = nil
	m.Brains = nil
	m.Referee = ""
	m.Council = map[string]*MemberView{}
	m.Members = nil
	m.SpentUSD, m.LimitUSD, m.WarnPct = 0, 0, 0
	m.Log = nil
	m.seen = map[int64]bool{}
}

func Update(m *Model, msg app.Msg) app.UpdateResult[*Model] {
	switch msg := msg.(type) {
	case app.KeyMsg:
		return m.handleKey(msg.Key)
	case app.PasteMsg:
		if m.Active == overlayConfig {
			m.ConfigInput = m.ConfigInput.Paste(msg.Text)
		} else {
			m.Input = m.Input.Paste(msg.Text)
		}
		return app.NoCmd(m)
	case app.ResizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return app.NoCmd(m)
	case app.DismissMsg:
		// Escape while a modal's focus scope is active (Tooey v0.5).
		m.Active = overlayNone
		return app.NoCmd(m)

	case SSEMsg:
		m.applyEvent(msg.Event)
		return app.NoCmd(m)
	case SSEStatusMsg:
		m.Connected = msg.Connected
		return app.NoCmd(m)

	case TaskCreatedMsg:
		if msg.Err != nil {
			m.Status = "submit failed: " + msg.Err.Error()
			return app.NoCmd(m)
		}
		m.reset(msg.TaskID)
		m.Status = "task " + msg.TaskID
		return app.WithSub(m, m.startStream(msg.TaskID))

	case ConfigFetchedMsg:
		if msg.Err != nil {
			m.Status = "config fetch failed: " + msg.Err.Error()
			return app.NoCmd(m)
		}
		m.Active = overlayConfig
		m.ConfigInput = component.NewTextInput("")
		m.ConfigInput = m.ConfigInput.Paste(msg.YAML)
		return app.NoCmd(m)
	case ConfigSavedMsg:
		if msg.Err != nil {
			// Engine rejected it — old config stays live; surface inline.
			m.Status = "config rejected: " + msg.Err.Error()
			return app.NoCmd(m)
		}
		m.Active = overlayNone
		m.Status = "config hot-reloaded"
		return app.NoCmd(m)

	case TasksListedMsg:
		if msg.Err != nil {
			m.Status = "task list failed: " + msg.Err.Error()
			return app.NoCmd(m)
		}
		m.Tasks, m.TaskSel, m.Active = msg.Tasks, 0, overlayResume
		return app.NoCmd(m)
	case ResumedMsg:
		if msg.Err != nil {
			m.Status = "resume failed: " + msg.Err.Error()
			return app.NoCmd(m)
		}
		m.reset(msg.TaskID)
		m.Status = "resuming " + msg.TaskID
		m.Active = overlayNone
		return app.WithSub(m, m.startStream(msg.TaskID))
	}
	return app.NoCmd(m)
}

func (m *Model) handleKey(k input.Key) app.UpdateResult[*Model] {
	if k.Type == input.CtrlC {
		if m.quitArmed {
			return app.Quit(m)
		}
		m.quitArmed = true
		m.Status = "press Ctrl+C again to quit"
		return app.NoCmd(m)
	}
	m.quitArmed = false

	switch m.Active {
	case overlayConfig:
		return m.handleConfigKey(k)
	case overlayResume:
		return m.handleResumeKey(k)
	case overlayOverride:
		return m.handleOverrideKey(k)
	}

	switch k.Type {
	case input.Enter:
		text := strings.TrimSpace(m.Input.Value)
		if text == "" {
			return app.NoCmd(m)
		}
		ov := m.Override
		m.Input = component.NewTextInput(m.Input.Placeholder)
		m.Input.Focused = true
		m.Status = "submitting…"
		return app.WithCmd(m, m.Client.SubmitCmd(text, ov))
	case input.RuneKey:
		// Command keys only fire on an empty prompt so typing stays natural.
		if m.Input.Value == "" {
			switch k.Rune {
			case 'q':
				return app.Quit(m)
			case 'c':
				m.Status = "fetching config…"
				return app.WithCmd(m, m.Client.FetchConfigCmd())
			case 'r':
				m.Status = "listing tasks…"
				return app.WithCmd(m, m.Client.ListTasksCmd())
			case 'o':
				m.Active = overlayOverride
				return app.NoCmd(m)
			}
		}
	}
	m.Input = m.Input.Update(k)
	return app.NoCmd(m)
}

func (m *Model) handleConfigKey(k input.Key) app.UpdateResult[*Model] {
	switch k.Type {
	case input.Escape:
		m.Active = overlayNone
		return app.NoCmd(m)
	case input.Enter:
		m.Status = "saving config…"
		return app.WithCmd(m, m.Client.SaveConfigCmd(m.ConfigInput.Value))
	}
	m.ConfigInput = m.ConfigInput.Update(k)
	return app.NoCmd(m)
}

func (m *Model) handleResumeKey(k input.Key) app.UpdateResult[*Model] {
	switch k.Type {
	case input.Escape:
		m.Active = overlayNone
	case input.Up:
		if m.TaskSel > 0 {
			m.TaskSel--
		}
	case input.Down:
		if m.TaskSel < len(m.Tasks)-1 {
			m.TaskSel++
		}
	case input.Enter:
		if len(m.Tasks) > 0 {
			id := m.Tasks[m.TaskSel].TaskID
			m.Status = "resuming " + id + "…"
			return app.WithCmd(m, m.Client.ResumeCmd(id))
		}
	}
	return app.NoCmd(m)
}

// handleOverrideKey: minimal override & escalate — cycle strategy/thinking,
// nudge budget; applied to the next submitted task.
func (m *Model) handleOverrideKey(k input.Key) app.UpdateResult[*Model] {
	cycle := func(cur string, opts ...string) string {
		for i, o := range opts {
			if o == cur {
				return opts[(i+1)%len(opts)]
			}
		}
		return opts[0]
	}
	switch k.Type {
	case input.Escape, input.Enter:
		m.Active = overlayNone
	case input.RuneKey:
		switch k.Rune {
		case 's':
			m.Override.Strategy = cycle(m.Override.Strategy, "", "solo", "twobrain", "council")
		case 't':
			m.Override.Thinking = cycle(m.Override.Thinking, "", "fast", "slow", "auto")
		case '+':
			m.Override.BudgetUSD += 1
		case '-':
			if m.Override.BudgetUSD >= 1 {
				m.Override.BudgetUSD -= 1
			} else {
				m.Override.BudgetUSD = 0
			}
		case 'x':
			m.Override = receptionist.Overrides{}
		}
	}
	return app.NoCmd(m)
}

// applyEvent maps one engine event onto exactly one region of the model.
func (m *Model) applyEvent(ev events.Event) {
	if ev.TaskID != m.TaskID || m.seen[ev.Seq] {
		return // stale stream or reconnect duplicate
	}
	m.seen[ev.Seq] = true
	m.logEvent(ev)

	p := ev.Payload
	switch ev.Kind {
	case events.KindTaskClassified:
		m.Class = p
	case events.KindTaskRouted:
		m.Strategy, _ = p["strategy"].(string)
		if limit, ok := p["limit_usd"].(float64); ok {
			m.LimitUSD = limit
		}
		if stages, ok := p["pipeline"].([]any); ok {
			m.Pipeline = nil
			for _, s := range stages {
				if str, ok := s.(string); ok {
					m.Pipeline = append(m.Pipeline, str)
				}
			}
		}
	case events.KindStageStart:
		m.Stage, _ = p["stage"].(string)
	case events.KindStageEnd:
		if spent, ok := p["spent_usd"].(float64); ok {
			m.SpentUSD = spent
		}
	case events.KindThinkingMode:
		mode, _ := p["mode"].(string)
		effort, _ := p["effort"].(string)
		m.ThinkMode = mode + " (effort: " + effort + ")"
	case events.KindThinkingToolChk:
		verdict, _ := p["verdict"].(string)
		if tools, ok := p["tools"].([]any); ok && len(tools) > 0 {
			names := make([]string, 0, len(tools))
			for _, t := range tools {
				if s, ok := t.(string); ok {
					names = append(names, s)
				}
			}
			verdict += " → " + strings.Join(names, ",")
		}
		m.ToolCheck = verdict
	case events.KindToolCall:
		name, _ := p["name"].(string)
		in, _ := p["input"].(string)
		m.ToolCalls = append(m.ToolCalls, fmt.Sprintf("%s %s", name, truncate(in, 40)))
	case events.KindBrainTurn:
		role, _ := p["role"].(string)
		text, _ := p["text"].(string)
		round := intFrom(p["round"])
		if role == "referee" {
			m.Referee = text
		} else {
			m.Brains = append(m.Brains, BrainTurn{Role: role, Round: round, Text: text})
		}
	case events.KindCouncilOpening, events.KindCouncilRebuttal:
		model, _ := p["model"].(string)
		mv := m.memberView(model)
		if pos, ok := p["position"].(string); ok {
			mv.Position = pos
		}
		if text, ok := p["text"].(string); ok {
			mv.Position = text
		}
		mv.Round = intFrom(p["round"])
	case events.KindCouncilVote:
		model, _ := p["model"].(string)
		mv := m.memberView(model)
		mv.Choice, _ = p["choice"].(string)
		if c, ok := p["confidence"].(float64); ok {
			mv.Confidence = c
		}
	case events.KindCouncilConsensus:
		reached, _ := p["reached"].(bool)
		method, _ := p["method"].(string)
		rounds := intFrom(p["rounds_used"])
		if reached {
			m.Consensus = fmt.Sprintf("✓ consensus (%s, %d rounds)", method, rounds)
		} else {
			m.Consensus = fmt.Sprintf("… no consensus yet (%s, %d rounds)", method, rounds)
		}
	case events.KindBudgetWarn:
		if pct, ok := p["pct"].(float64); ok && pct > m.WarnPct {
			m.WarnPct = pct
		}
		if spent, ok := p["spent_usd"].(float64); ok {
			m.SpentUSD = spent
		}
		if limit, ok := p["limit_usd"].(float64); ok && limit > 0 {
			m.LimitUSD = limit
		}
	case events.KindTaskFinal:
		m.Final, _ = p["text"].(string)
		if cost, ok := p["total_cost_usd"].(float64); ok {
			m.SpentUSD = cost
		}
		m.Stage = "done"
		if m.Strategy == "council" {
			m.Synthesis = m.Final
		}
	case events.KindError:
		if msg, ok := p["message"].(string); ok {
			m.Status = "engine: " + truncate(msg, 80)
		}
	}
}

func (m *Model) memberView(model string) *MemberView {
	mv, ok := m.Council[model]
	if !ok {
		mv = &MemberView{}
		m.Council[model] = mv
		m.Members = append(m.Members, model)
		sort.Strings(m.Members)
	}
	return mv
}

func (m *Model) logEvent(ev events.Event) {
	ts := time.UnixMilli(ev.TS).Format("15:04:05")
	line := fmt.Sprintf("%s %-18s %s", ts, ev.Kind, ev.Actor)
	m.Log = append(m.Log, line)
	if len(m.Log) > 500 {
		m.Log = m.Log[len(m.Log)-500:]
	}
}

func intFrom(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
