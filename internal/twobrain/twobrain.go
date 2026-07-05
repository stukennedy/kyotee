// Package twobrain implements the divergent/convergent two-persona strategy
// with a referee synthesis (inferred spec 05, referenced by specs 01/03/08):
// a "right brain" model generates and explores, a "left brain" model
// critiques and narrows, alternating for N rounds; a referee collapses the
// exchange into the Draft. Events: brain.turn {role, round, text}.
package twobrain

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/stukennedy/kyotee/internal/budget"
	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/pipeline"
	"github.com/stukennedy/kyotee/internal/provider"
	"github.com/stukennedy/kyotee/internal/thinking"
)

type Stage struct {
	Divergent  provider.Provider // right brain: options, ideas, edge cases
	Convergent provider.Provider // left brain: critique, structure, decision
	Referee    provider.Provider // Models.Primary: final synthesis
	Rounds     int               // divergent+convergent exchanges; default 2
	Tools      *thinking.ToolRegistry
	MaxTokens  int
}

func (t *Stage) ID() string { return "twobrain" }

const (
	divergentSystem  = "You are the DIVERGENT brain in a two-brain reasoning pair. Generate possibilities: alternative approaches, non-obvious angles, edge cases, risks the obvious answer misses. Breadth over polish. Do not converge on one answer."
	convergentSystem = "You are the CONVERGENT brain in a two-brain reasoning pair. Critique the divergent brain's ideas ruthlessly: discard the weak, stress-test the strong, and structure what survives toward a single workable answer."
)

func (t *Stage) Run(ctx context.Context, st *pipeline.State, emit events.Emitter) (*pipeline.State, error) {
	if t.Divergent == nil || t.Convergent == nil || t.Referee == nil {
		return st, errors.New("twobrain: divergent, convergent, and referee models are all required")
	}
	rounds := t.Rounds
	if rounds <= 0 {
		rounds = 2
	}

	var exchange strings.Builder
	fmt.Fprintf(&exchange, "Task:\n%s\n", st.Original)

	for r := 1; r <= rounds; r++ {
		if st.Budget.Exhausted() {
			break
		}
		div, err := t.turn(ctx, st, emit, t.Divergent, "divergent", divergentSystem, exchange.String(), r)
		if err != nil {
			return st, err
		}
		fmt.Fprintf(&exchange, "\n--- Round %d, divergent ---\n%s\n", r, div)

		if st.Budget.Exhausted() {
			break
		}
		conv, err := t.turn(ctx, st, emit, t.Convergent, "convergent", convergentSystem, exchange.String(), r)
		if err != nil {
			return st, err
		}
		fmt.Fprintf(&exchange, "\n--- Round %d, convergent ---\n%s\n", r, conv)
	}

	// Referee synthesis reads the whole exchange and produces the Draft.
	resp, err := t.Referee.Generate(ctx, provider.Request{
		System:          "You are the REFEREE of a two-brain exchange. Produce the final answer to the user's task, taking the best of the divergent exploration and the convergent critique. Answer the user directly.",
		Messages:        []provider.Message{provider.UserText(exchange.String())},
		ReasoningEffort: thinking.SolverEffort(st),
		MaxTokens:       t.MaxTokens,
		Metadata:        map[string]string{"task_id": st.TaskID, "stage": t.ID(), "role": "referee"},
	})
	if err != nil {
		return st, err
	}
	st.Draft = resp.Text()
	st.AddTurn(t.ID(), "referee", st.Draft, resp.Usage)
	budget.CheckWarn(&st.Budget, emit)
	emit(events.Event{
		Kind: events.KindBrainTurn, Stage: t.ID(), Actor: t.Referee.Name(),
		Payload: map[string]any{"role": "referee", "round": rounds, "text": st.Draft},
	})
	return st, nil
}

func (t *Stage) turn(ctx context.Context, st *pipeline.State, emit events.Emitter, p provider.Provider, role, system, context_ string, round int) (string, error) {
	flagged := thinking.FlaggedTools(st)
	req := provider.Request{
		System:          system + thinking.ToolInstruction(flagged),
		Messages:        []provider.Message{provider.UserText(context_)},
		ReasoningEffort: thinking.SolverEffort(st),
		MaxTokens:       t.MaxTokens,
		Metadata:        map[string]string{"task_id": st.TaskID, "stage": t.ID(), "role": role},
	}
	if len(flagged) > 0 && t.Tools != nil {
		req.Tools = t.Tools.DefsFor(flagged)
	}
	resp, usage, err := thinking.RunToolLoop(ctx, p, req, t.Tools, 3, emit, t.ID())
	if err != nil {
		return "", err
	}
	text := resp.Text()
	st.AddTurn(t.ID(), role, text, usage)
	budget.CheckWarn(&st.Budget, emit)
	emit(events.Event{
		Kind: events.KindBrainTurn, Stage: t.ID(), Actor: p.Name(),
		Payload: map[string]any{"role": role, "round": round, "text": text},
	})
	return text, nil
}
