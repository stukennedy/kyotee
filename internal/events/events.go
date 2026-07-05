// Package events defines the Event contract and the in-process Bus.
// Everything observable in the engine is an Event; the TUI is a pure consumer.
package events

// Event kinds (v1 catalog, spec 01 §3).
const (
	KindTaskReceived     = "task.received"
	KindTaskClassified   = "task.classified"
	KindTaskRouted       = "task.routed"
	KindStageStart       = "stage.start"
	KindStageEnd         = "stage.end"
	KindThinkingMode     = "thinking.mode"
	KindThinkingToolChk  = "thinking.tool_check"
	KindToolCall         = "tool.call"
	KindToolResult       = "tool.result"
	KindBrainTurn        = "brain.turn"
	KindCouncilOpening   = "council.opening"
	KindCouncilRebuttal  = "council.rebuttal"
	KindCouncilVote      = "council.vote"
	KindCouncilConsensus = "council.consensus"
	KindBudgetWarn       = "budget.warn"
	KindTaskFinal        = "task.final"
	KindError            = "error"
)

// Event is the single observable unit emitted by every mechanism.
type Event struct {
	TaskID  string         `json:"task_id"`
	Seq     int64          `json:"seq"` // monotonic per task, assigned by the Bus
	Kind    string         `json:"kind"`
	Stage   string         `json:"stage"` // stage ID or "" for lifecycle
	Actor   string         `json:"actor"` // model name / persona / "receptionist"
	Payload map[string]any `json:"payload"`
	TS      int64          `json:"ts"` // unix millis
}

// Emitter is a stage-scoped convenience for publishing events.
type Emitter func(Event)

// Bus fans events out to subscribers and retains per-task history so late
// subscribers can replay from Seq 0 (required by the SSE surface, spec 08 §6).
type Bus interface {
	Publish(Event)
	// Subscribe returns a channel of events for a task (or all tasks if id == "").
	// The returned func unsubscribes. History for the task is replayed first.
	Subscribe(taskID string) (<-chan Event, func())
	// History returns a copy of all retained events for a task.
	History(taskID string) []Event
}
