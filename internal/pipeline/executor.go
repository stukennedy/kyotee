package pipeline

import (
	"context"
	"errors"
	"time"

	"github.com/stukennedy/kyotee/internal/events"
)

// ErrBudgetExhausted is returned when the spend ceiling halts a task between
// stages. The current Draft is promoted to Final as the best-effort answer.
var ErrBudgetExhausted = errors.New("budget exhausted")

// Store is the persistence surface the Executor checkpoints through.
// internal/state provides the JSON-file implementation.
type Store interface {
	Save(st *State) error
	Load(taskID string) (*State, error)
	List() ([]string, error) // task IDs
}

// Executor runs stages in order, checkpointing after each and emitting
// lifecycle events. On error it persists state and returns; the task can be
// resumed from its checkpoints.
type Executor struct {
	Store Store
	Bus   events.Bus
}

func (e *Executor) Execute(ctx context.Context, stages []Stage, st *State) (*State, error) {
	emit := events.EmitterFor(e.Bus, st.TaskID)

	for _, stage := range stages {
		if st.Checkpointed(stage.ID()) {
			continue // resume: skip completed stages
		}

		// Enforce the global ceiling between stages: halt, promote Draft→Final.
		if st.Budget.Exhausted() {
			st.Final = st.Draft
			e.persist(st, emit)
			e.emitFinal(st, emit, "budget_exhausted")
			return st, ErrBudgetExhausted
		}

		if err := ctx.Err(); err != nil {
			e.persist(st, emit)
			return st, err
		}

		spentBefore := st.Budget.SpentUSD
		turnsBefore := len(st.Transcript)
		start := time.Now()
		emit(events.Event{
			Kind:  events.KindStageStart,
			Stage: stage.ID(),
			Payload: map[string]any{
				"stage":     stage.ID(),
				"spent_usd": st.Budget.SpentUSD,
			},
		})

		next, err := stage.Run(ctx, st, emit)
		if next != nil {
			st = next
		}
		if err != nil {
			// Discard the failed stage's partial turns: the stage re-runs
			// from scratch on resume, and stale partials would corrupt
			// round counting and synthesis input. Spend stays accounted in
			// Budget, and the event log retains the full record.
			if len(st.Transcript) > turnsBefore {
				st.Transcript = st.Transcript[:turnsBefore]
			}
			emit(events.Event{
				Kind:  events.KindError,
				Stage: stage.ID(),
				Payload: map[string]any{
					"message":  err.Error(),
					"stage":    stage.ID(),
					"terminal": true, // run stops here; resumable from checkpoint
				},
			})
			e.persist(st, emit)
			return st, err
		}

		st.Checkpoints = append(st.Checkpoints, stage.ID())
		e.persist(st, emit)
		emit(events.Event{
			Kind:  events.KindStageEnd,
			Stage: stage.ID(),
			Payload: map[string]any{
				"stage":          stage.ID(),
				"cost_delta_usd": st.Budget.SpentUSD - spentBefore,
				"spent_usd":      st.Budget.SpentUSD,
				"duration_ms":    time.Since(start).Milliseconds(),
			},
		})
	}

	// Terminal: promote Draft→Final if no stage set Final explicitly.
	if st.Final == "" {
		st.Final = st.Draft
	}
	e.persist(st, emit)
	e.emitFinal(st, emit, "completed")
	return st, nil
}

func (e *Executor) persist(st *State, emit events.Emitter) {
	if e.Store == nil {
		return
	}
	if err := e.Store.Save(st); err != nil {
		emit(events.Event{
			Kind:    events.KindError,
			Payload: map[string]any{"message": "checkpoint save failed: " + err.Error()},
		})
	}
}

func (e *Executor) emitFinal(st *State, emit events.Emitter, reason string) {
	perStage := map[string]float64{}
	for _, t := range st.Transcript {
		perStage[t.Stage] += t.Usage.CostUSD
	}
	emit(events.Event{
		Kind: events.KindTaskFinal,
		Payload: map[string]any{
			"text":           st.Final,
			"reason":         reason,
			"total_cost_usd": st.Budget.SpentUSD,
			"total_tokens":   st.Budget.Tokens,
			"per_stage_usd":  perStage,
		},
	})
}
