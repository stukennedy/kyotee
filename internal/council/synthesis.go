package council

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

// Synthesis always follows Council (spec 06 §5): it reads the full debate
// transcript and collapses it into the single State.Draft. When deadlock was
// resolved as synthesis_notes_dissent, the disagreement is surfaced, not
// papered over.
type Synthesis struct {
	Model     provider.Provider // Models.Primary
	MaxTokens int
}

func (s *Synthesis) ID() string { return "synthesis" }

func (s *Synthesis) Run(ctx context.Context, st *pipeline.State, emit events.Emitter) (*pipeline.State, error) {
	if s.Model == nil {
		return st, errors.New("synthesis: no model configured")
	}

	var debate strings.Builder
	for _, t := range st.Transcript {
		if t.Stage != "council" {
			continue
		}
		fmt.Fprintf(&debate, "[%s]\n%s\n\n", t.Role, t.Content)
	}
	if debate.Len() == 0 {
		return st, errors.New("synthesis: no council transcript to synthesise")
	}

	system := "You are the synthesiser of a multi-model council debate. Produce the single best final answer to the user's task, grounded in the debate. Answer the user directly — do not narrate the debate."
	prompt := "Task:\n" + st.Original + "\n\nFull debate transcript:\n\n" + debate.String()

	switch st.Meta[MetaOutcome] {
	case "synthesis_notes_dissent":
		system += " The council did NOT reach consensus. You MUST explicitly record the substantive dissent: state the majority/strongest view and then, under a 'Dissent' heading, the unresolved disagreement. Honesty about the split is part of the answer."
	case "referee", "majority_vote":
		if w := st.Meta[MetaWinner]; w != "" {
			prompt += "\n\nDeadlock was resolved in favour of:\n" + w
		}
	}

	resp, err := s.Model.Generate(ctx, provider.Request{
		System:          system,
		Messages:        []provider.Message{provider.UserText(prompt)},
		ReasoningEffort: thinking.SolverEffort(st),
		MaxTokens:       s.MaxTokens,
		Metadata:        map[string]string{"task_id": st.TaskID, "stage": s.ID()},
	})
	if err != nil {
		return st, err
	}

	st.Draft = resp.Text()
	st.AddTurn(s.ID(), "synthesis", st.Draft, resp.Usage)
	budget.CheckWarn(&st.Budget, emit)
	return st, nil
}
