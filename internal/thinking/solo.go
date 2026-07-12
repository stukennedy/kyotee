package thinking

import (
	"context"
	"errors"

	"github.com/stukennedy/kyotee/internal/budget"
	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/pipeline"
	"github.com/stukennedy/kyotee/internal/provider"
)

// Solo runs one model to produce the Draft, honoring thinking.* meta and
// executing any tool-use loop (spec 04 §4).
type Solo struct {
	Model        provider.Provider
	Tools        *ToolRegistry
	MaxToolCalls int
	MaxTokens    int
	Temperature  float64
}

func (s *Solo) ID() string { return "solo" }

const soloSystem = "You are a capable assistant. Answer the user's request directly and completely. If tools are available and a fact could be stale or unknown, use the tools rather than guessing."

func (s *Solo) Run(ctx context.Context, st *pipeline.State, emit events.Emitter) (*pipeline.State, error) {
	if s.Model == nil {
		return st, errors.New("solo: no model configured")
	}

	flagged := FlaggedTools(st)
	req := provider.Request{
		System:          soloSystem + ToolInstruction(flagged),
		Messages:        []provider.Message{provider.UserText(st.PromptBody())},
		ReasoningEffort: SolverEffort(st),
		MaxTokens:       s.MaxTokens,
		Temperature:     s.Temperature,
		Metadata:        map[string]string{"task_id": st.TaskID, "stage": s.ID()},
	}
	if len(flagged) > 0 && s.Tools != nil {
		req.Tools = s.Tools.DefsFor(flagged)
	}

	resp, usage, err := RunToolLoop(ctx, s.Model, req, s.Tools, s.MaxToolCalls, emit, s.ID())
	if err != nil {
		return st, err
	}

	st.Draft = resp.Text()
	st.AddTurn(s.ID(), "solo", st.Draft, usage)
	budget.CheckWarn(&st.Budget, emit)
	return st, nil
}
