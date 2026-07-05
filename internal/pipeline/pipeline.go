// Package pipeline defines the Stage contract, the State envelope threaded
// through every stage, and the Executor that runs a task's stage slice.
package pipeline

import (
	"context"
	"slices"

	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/provider"
)

// Stage is one unit in a task's pipeline.
type Stage interface {
	// ID is a stable stage kind, e.g. "thinking", "council", "twobrain".
	ID() string
	// Run transforms the pipeline state. It may call providers, emit events,
	// and mutate/append to State. It returns the updated State or an error.
	Run(ctx context.Context, st *State, emit events.Emitter) (*State, error)
}

// Classification is the Receptionist's verdict on a task (spec 03 §2).
// Defined here because State carries it and every solver reads it.
type Classification struct {
	Complexity string  `json:"complexity"` // "trivial" | "standard" | "hard"
	Domain     string  `json:"domain"`     // "code" | "research" | "reasoning" | "creative" | "chat"
	ToolNeed   string  `json:"tool_need"`  // "none" | "likely" | "required"
	Confidence float64 `json:"confidence"` // 0..1, classifier's self-estimate
	Rationale  string  `json:"rationale"`  // one line, for the event log
}

// BudgetState is the running cost ledger carried in State (spec 03 §5).
type BudgetState struct {
	LimitUSD float64 `json:"limit_usd"`
	SpentUSD float64 `json:"spent_usd"`
	Tokens   int     `json:"tokens"`
	// WarnedPct records warn thresholds already fired, so budget.warn emits
	// once per threshold even across resume.
	WarnedPct []float64 `json:"warned_pct,omitempty"`
}

// Account adds a provider call's usage to the ledger.
func (b *BudgetState) Account(u provider.Usage) {
	b.SpentUSD += u.CostUSD
	b.Tokens += u.InputTokens + u.OutputTokens
}

// Exhausted reports whether the ceiling has been reached (0 limit = unlimited).
func (b *BudgetState) Exhausted() bool {
	return b.LimitUSD > 0 && b.SpentUSD >= b.LimitUSD
}

// RemainingUSD returns budget headroom; -1 means unlimited.
func (b *BudgetState) RemainingUSD() float64 {
	if b.LimitUSD <= 0 {
		return -1
	}
	if r := b.LimitUSD - b.SpentUSD; r > 0 {
		return r
	}
	return 0
}

// State is the envelope threaded through every stage. It is also the unit of
// persistence and must be JSON-serializable.
type State struct {
	TaskID      string            `json:"task_id"`
	Original    string            `json:"original"` // the user's original request
	Class       Classification    `json:"class"`
	Transcript  []Turn            `json:"transcript"` // accumulated reasoning/answers across stages
	Draft       string            `json:"draft"`      // current best-answer candidate
	Final       string            `json:"final"`      // set only by the terminal stage
	Budget      BudgetState       `json:"budget"`
	Checkpoints []string          `json:"checkpoints"` // stage IDs completed, for resume
	Meta        map[string]string `json:"meta"`
}

type Turn struct {
	Stage   string         `json:"stage"`
	Role    string         `json:"role"` // "divergent" | "convergent" | "referee" | "council:<model>" | "solo"
	Content string         `json:"content"`
	Usage   provider.Usage `json:"usage"`
}

// NewState builds a fresh State for a task.
func NewState(taskID, original string) *State {
	return &State{TaskID: taskID, Original: original, Meta: map[string]string{}}
}

// Checkpointed reports whether a stage ID has already completed.
func (s *State) Checkpointed(stageID string) bool {
	return slices.Contains(s.Checkpoints, stageID)
}

// AddTurn appends a transcript turn and accounts its usage against the budget.
func (s *State) AddTurn(stage, role, content string, u provider.Usage) {
	s.Transcript = append(s.Transcript, Turn{Stage: stage, Role: role, Content: content, Usage: u})
	s.Budget.Account(u)
}
