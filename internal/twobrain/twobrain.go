// Package twobrain implements the divergent/convergent debate (spec 05): a
// high-temperature divergent persona proposes, a low-temperature convergent
// persona critiques, they iterate, and a referee synthesises. Temperature is
// the mechanism — the sampling split produces the behavioural split more
// than prompt wording does. Single provider is fine; vendor diversity is the
// Council's job.
package twobrain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/stukennedy/kyotee/internal/budget"
	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/pipeline"
	"github.com/stukennedy/kyotee/internal/provider"
	"github.com/stukennedy/kyotee/internal/thinking"
)

// RoundsMax hard-caps the exchange — returns diminish fast and tokens
// multiply (spec 05 §2).
const RoundsMax = 3

const (
	DefaultDivTemp  = 1.0
	DefaultConvTemp = 0.3
)

// Prompts are the persona system prompts. Operators tune them via external
// files referenced from config (twobrain.prompts); these are the built-ins.
type Prompts struct {
	Divergent  string
	Convergent string
	Referee    string
}

var DefaultPrompts = Prompts{
	Divergent:  "You are the DIVERGENT (right) brain in a two-brain reasoning pair. Explore widely: propose multiple distinct approaches, unconventional angles, edge cases, and risks the obvious answer misses. Don't self-censor and don't converge — breadth over polish.",
	Convergent: "You are the CONVERGENT (left) brain in a two-brain reasoning pair. Be rigorous: critique each proposed approach, find flaws, stress-test feasibility, rank the options, and converge on the strongest with explicit reasons. Request refinement where an option is promising but underspecified.",
	Referee:    "You are the REFEREE of a two-brain exchange. Read the full debate and produce the final answer to the user's task, taking the best of the divergent exploration and the convergent critique. Answer the user directly.",
}

// LoadPrompts reads persona prompt files (empty path → built-in default).
// A named-but-unreadable file is an error: the operator asked for a prompt
// we cannot deliver.
func LoadPrompts(divPath, convPath, refPath string) (Prompts, error) {
	p := DefaultPrompts
	load := func(path string, dst *string) error {
		if path == "" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("twobrain prompt %s: %w", path, err)
		}
		if s := strings.TrimSpace(string(data)); s != "" {
			*dst = s
		}
		return nil
	}
	if err := load(divPath, &p.Divergent); err != nil {
		return p, err
	}
	if err := load(convPath, &p.Convergent); err != nil {
		return p, err
	}
	if err := load(refPath, &p.Referee); err != nil {
		return p, err
	}
	return p, nil
}

type Stage struct {
	Divergent    provider.Provider // right brain
	Convergent   provider.Provider // left brain (often same base model)
	Referee      provider.Provider // Models.Primary
	Rounds       int               // 1..RoundsMax; default 2
	DivTemp      float64           // default 1.0
	ConvTemp     float64           // default 0.3
	Prompts      Prompts
	Tools        *thinking.ToolRegistry
	MaxToolCalls int // per-turn tool-loop cap (defaults.tool_call_cap)
	MaxTokens    int
}

func (t *Stage) ID() string { return "twobrain" }

func (t *Stage) Run(ctx context.Context, st *pipeline.State, emit events.Emitter) (*pipeline.State, error) {
	if t.Divergent == nil || t.Convergent == nil || t.Referee == nil {
		return st, errors.New("twobrain: divergent, convergent, and referee models are all required")
	}
	rounds := t.Rounds
	if rounds <= 0 {
		rounds = 2
	}
	if rounds > RoundsMax {
		rounds = RoundsMax
	}
	divTemp, convTemp := t.DivTemp, t.ConvTemp
	if divTemp == 0 {
		divTemp = DefaultDivTemp
	}
	if convTemp == 0 {
		convTemp = DefaultConvTemp
	}
	prompts := t.Prompts
	if prompts.Divergent == "" {
		prompts.Divergent = DefaultPrompts.Divergent
	}
	if prompts.Convergent == "" {
		prompts.Convergent = DefaultPrompts.Convergent
	}
	if prompts.Referee == "" {
		prompts.Referee = DefaultPrompts.Referee
	}

	// Handoffs carry only the prior turn's distilled text (spec 05 §2), not
	// the whole growing transcript; the referee alone sees everything.
	lastConv := "" // convergent's latest critique, seen by divergent
	lastDiv := ""  // divergent's latest proposals, seen by convergent

	for r := 1; r <= rounds; r++ {
		if st.Budget.Exhausted() {
			break
		}
		divPrompt := fmt.Sprintf("Task:\n%s\n\nRound %d of %d. Propose distinct approaches.", st.PromptBody(), r, rounds)
		if lastConv != "" {
			divPrompt = fmt.Sprintf("Task:\n%s\n\nRound %d of %d. The convergent brain's latest critique:\n%s\n\nRefine the surviving options and address the critique.",
				st.PromptBody(), r, rounds, distill(lastConv))
		}
		div, err := t.turn(ctx, st, emit, t.Divergent, "divergent", prompts.Divergent, divPrompt, divTemp, r)
		if err != nil {
			return st, err
		}
		lastDiv = div

		if st.Budget.Exhausted() {
			break
		}
		convPrompt := fmt.Sprintf("Task:\n%s\n\nRound %d of %d. The divergent brain proposes:\n%s\n\nCritique each option, rank them, and %s.",
			st.PromptBody(), r, rounds, distill(lastDiv),
			map[bool]string{true: "converge on the strongest with your recommendation and rationale", false: "flag which need refinement"}[r == rounds])
		conv, err := t.turn(ctx, st, emit, t.Convergent, "convergent", prompts.Convergent, convPrompt, convTemp, r)
		if err != nil {
			return st, err
		}
		lastConv = conv
	}

	// Referee reads the full transcript of the exchange.
	var full strings.Builder
	fmt.Fprintf(&full, "Task:\n%s\n\nFull two-brain exchange:\n", st.PromptBody())
	round := 0
	for _, turn := range st.Transcript {
		if turn.Stage != t.ID() {
			continue
		}
		if turn.Role == "divergent" {
			round++
		}
		fmt.Fprintf(&full, "\n--- round %d, %s ---\n%s\n", round, turn.Role, turn.Content)
	}

	resp, err := t.Referee.Generate(ctx, provider.Request{
		System:          prompts.Referee,
		Messages:        []provider.Message{provider.UserText(full.String())},
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

func (t *Stage) turn(ctx context.Context, st *pipeline.State, emit events.Emitter, p provider.Provider, role, system, prompt string, temp float64, round int) (string, error) {
	flagged := thinking.FlaggedTools(st)
	req := provider.Request{
		System:          system + thinking.ToolInstruction(flagged),
		Messages:        []provider.Message{provider.UserText(prompt)},
		ReasoningEffort: thinking.SolverEffort(st),
		Temperature:     temp,
		MaxTokens:       t.MaxTokens,
		Metadata:        map[string]string{"task_id": st.TaskID, "stage": t.ID(), "role": role},
	}
	if len(flagged) > 0 && t.Tools != nil {
		req.Tools = t.Tools.DefsFor(flagged)
	}
	resp, usage, err := thinking.RunToolLoop(ctx, p, req, t.Tools, t.MaxToolCalls, emit, t.ID())
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

// distill truncates a handoff on a sentence-ish boundary to bound token cost.
func distill(s string) string {
	const max = 2000
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndexAny(cut, ".\n"); i > max/2 {
		cut = cut[:i+1]
	}
	return cut + " […]"
}
