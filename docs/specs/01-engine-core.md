# 01 — Engine Core: Contracts & Pipeline Executor

This spec defines the three cross-cutting types (`Provider`, `Stage`, `Event`) and the pipeline executor and state machine that run them. **Every other spec depends on this one.** Implement this first.

Language: Go 1.24+. No provider SDKs imported here — this package defines interfaces only.

---

## 1. The `Provider` Interface

The single abstraction over every LLM, regardless of vendor. Lives in `internal/provider`.

```go
package provider

import "context"

// Provider is the vendor-agnostic model surface. Adapters wrap vendor SDKs.
type Provider interface {
    // Name is a stable identifier, e.g. "claude-opus-4-8", "gpt-5".
    Name() string
    // Vendor is the family, e.g. "anthropic", "openai", "google", "local".
    Vendor() string
    // Generate runs a single completion. Streaming is delivered via the
    // Stream callback in Request; the returned Response is the final aggregate.
    Generate(ctx context.Context, req Request) (Response, error)
    // Capabilities reports what this model supports (tools, vision, etc.).
    Capabilities() Capabilities
    // CostPer1M returns input/output USD cost per 1M tokens for budgeting.
    CostPer1M() (inputUSD, outputUSD float64)
}

type Request struct {
    System       string
    Messages     []Message
    Tools        []ToolDef        // provider-agnostic tool declarations
    Temperature  float64
    MaxTokens    int
    // ReasoningEffort is a normalized knob: "minimal" | "low" | "medium" | "high".
    // Adapters map this to vendor-specific reasoning/thinking params.
    ReasoningEffort string
    // Stream, if non-nil, receives incremental deltas for observability.
    Stream       func(Delta)
    // Metadata is opaque pass-through for tracing (task ID, stage, etc.).
    Metadata     map[string]string
}

type Response struct {
    Content    []Block          // text and tool_use blocks, in order
    StopReason string
    Usage      Usage
}

type Block struct {
    Type     string            // "text" | "tool_use" | "tool_result"
    Text     string            // when Type == "text"
    ToolCall *ToolCall         // when Type == "tool_use"
}

type Usage struct {
    InputTokens  int
    OutputTokens int
    CostUSD      float64        // computed by adapter from CostPer1M
}

type Capabilities struct {
    Tools        bool
    Vision       bool
    Reasoning    bool           // supports explicit reasoning-effort control
    MaxContext   int
}

type Message struct {
    Role    string             // "user" | "assistant" | "tool"
    Content []Block
}

type Delta struct {
    Type string                // "text" | "tool_use_start" | "reasoning" | "done"
    Text string
}

// ToolDef, ToolCall, ToolResult are provider-agnostic tool types.
type ToolDef struct {
    Name        string
    Description string
    Schema      map[string]any  // JSON schema of parameters
}
type ToolCall struct {
    ID     string
    Name   string
    Input  map[string]any
}
```

**Adapter responsibility.** Each vendor adapter (spec `02`) translates `Request` → vendor payload and vendor stream → `Delta`/`Response`, and computes `Usage.CostUSD` from token counts. `ReasoningEffort` mapping is the adapter's job (e.g. Anthropic extended-thinking budget, OpenAI reasoning effort). If a vendor lacks a knob, the adapter maps to the nearest available or no-ops.

**Registry.** A `provider.Registry` maps `Name() → Provider`. Built from config at startup. All mechanisms resolve models by name through the registry; none instantiate providers directly.

```go
type Registry interface {
    Get(name string) (Provider, error)
    List() []Provider
}
```

---

## 2. The `Stage` Interface

A `Stage` is one unit in a task's pipeline. Lives in `internal/pipeline`.

```go
package pipeline

import (
    "context"
    "harness/internal/events"
)

type Stage interface {
    // ID is a stable stage kind, e.g. "thinking", "council", "twobrain".
    ID() string
    // Run transforms the pipeline state. It may call providers, emit events,
    // and mutate/append to State. It returns the updated State or an error.
    Run(ctx context.Context, st *State, emit events.Emitter) (*State, error)
}

// State is the envelope threaded through every stage. It is also the unit
// of persistence (see §4). It must be JSON-serializable.
type State struct {
    TaskID       string
    Original     string            // the user's original request
    Class        Classification    // set by the Receptionist (spec 03)
    Transcript   []Turn            // accumulated reasoning/answers across stages
    Draft        string            // current best-answer candidate
    Final        string            // set only by the terminal stage
    Budget       BudgetState       // running cost/limit (spec 03 §Budget)
    Checkpoints  []string          // stage IDs completed, for resume
    Meta         map[string]string
}

type Turn struct {
    Stage   string
    Role    string   // "divergent" | "convergent" | "referee" | "council:<model>" | "solo"
    Content string
    Usage   provider.Usage
}
```

**Pipeline = `[]Stage`.** The Receptionist assembles the slice; the Executor runs it.

### The Executor

```go
type Executor struct {
    Store  state.Store
    Bus    events.Bus
}

// Execute runs stages in order, checkpointing after each, emitting lifecycle
// events. On error it persists state and returns; the task can be resumed.
func (e *Executor) Execute(ctx context.Context, stages []Stage, st *State) (*State, error)
```

Executor rules:
- Emit `stage.start` / `stage.end` events around each stage (with cost delta).
- Persist `State` after every stage (checkpoint) via `state.Store`.
- If `ctx` is cancelled or a stage errors, persist and return — do not partially discard.
- Enforce the global budget ceiling *between* stages: if `Budget.SpentUSD >= Budget.LimitUSD`, halt with `ErrBudgetExhausted` and mark the current `Draft` as `Final` (best-effort answer).

---

## 3. The `Event` Contract

Everything observable is an `Event` on a bus. The TUI (spec `08`) is a pure consumer. Lives in `internal/events`.

```go
package events

type Event struct {
    TaskID  string         `json:"task_id"`
    Seq     int64          `json:"seq"`      // monotonic per task
    Kind    string         `json:"kind"`     // see catalog below
    Stage   string         `json:"stage"`    // stage ID or "" for lifecycle
    Actor   string         `json:"actor"`    // model name / persona / "receptionist"
    Payload map[string]any `json:"payload"`
    TS      int64          `json:"ts"`       // unix millis
}

type Emitter func(Event)          // stage-scoped convenience

type Bus interface {
    Publish(Event)
    // Subscribe returns a channel of events for a task (or all tasks if id == "").
    Subscribe(taskID string) (<-chan Event, func())  // func() unsubscribes
}
```

### Event Kind Catalog (v1)

| Kind | Emitted by | Key payload fields |
|---|---|---|
| `task.received` | Receptionist | `text` |
| `task.classified` | Receptionist | `complexity`, `domain`, `tool_need`, `strategy` |
| `task.routed` | Receptionist | `pipeline` (list of stage IDs), `models` |
| `stage.start` / `stage.end` | Executor | `stage`, `cost_delta_usd`, `spent_usd` |
| `thinking.mode` | Thinking | `mode` (fast/slow), `reason` |
| `thinking.tool_check` | Thinking | `needs_tool` (bool), `verdict` |
| `tool.call` / `tool.result` | any stage | `name`, `input` / `output` |
| `brain.turn` | Two-Brain | `role` (divergent/convergent/referee), `round`, `text` |
| `council.opening` | Council | `model`, `position` |
| `council.rebuttal` | Council | `model`, `round`, `text` |
| `council.vote` | Council | `model`, `choice`, `confidence` |
| `council.consensus` | Council | `reached` (bool), `method`, `rounds_used` |
| `budget.warn` | Budget | `spent_usd`, `limit_usd`, `pct` |
| `task.final` | Executor | `text`, `total_cost_usd`, `total_tokens` |
| `error` | any | `message`, `stage` |

The SSE server (spec `02` §HTTP surface) serialises these events one-per-`data:` line for the TUI.

---

## 4. State Persistence

```go
package state

type Store interface {
    Save(st *pipeline.State) error
    Load(taskID string) (*pipeline.State, error)
    List() ([]string, error)          // task IDs
}
```

v1 implementation: JSON files under `~/.harness/tasks/<taskID>.json`, written atomically (temp + rename). This satisfies resume and audit without a DB dependency. Interface allows a SQLite backend later.

**Resume flow:** load `State`, inspect `Checkpoints`, and have the Receptionist rebuild the remaining pipeline (skip completed stage IDs), then hand to Executor.

---

## 5. Acceptance Criteria

- [ ] `Provider`, `Stage`/`State`, `Event`/`Bus`, `state.Store` compile as defined and are the only definitions of these types in the codebase.
- [ ] A no-op `Stage` can be run by the `Executor`, producing `stage.start`/`stage.end` events and a checkpoint on disk.
- [ ] Executor halts cleanly with `ErrBudgetExhausted` when `SpentUSD >= LimitUSD`, promoting `Draft` to `Final`.
- [ ] A `State` can be saved, the process restarted, loaded, and its `Checkpoints` inspected.
- [ ] Events published to the `Bus` are receivable by a subscriber filtered on `TaskID`.
- [ ] Unit tests cover: budget halt, checkpoint/resume, event fan-out to two concurrent subscribers.
