// Package thinking implements the fast/slow gate and the tool-need pre-pass
// (spec 04) — the structural step that stops a solver answering present-state
// facts from stale working memory.
package thinking

import (
	"context"
	"fmt"
	"strings"

	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/jsonx"
	"github.com/stukennedy/kyotee/internal/pipeline"
	"github.com/stukennedy/kyotee/internal/provider"
)

// State.Meta keys written by the Thinking stage and read by solver stages.
const (
	MetaMode   = "thinking.mode"   // "fast" | "slow"
	MetaEffort = "thinking.effort" // "minimal" | "low" | "medium" | "high"
	MetaTools  = "thinking.tools"  // comma-separated tool names, or ""
)

// Options tunes the stage (from config.Thinking).
type Options struct {
	FastEffort          string  // default "low"
	SlowEffort          string  // default "high"
	ConfidenceThreshold float64 // receptionist confidence below this triggers slow
	MaxToolCalls        int     // solver tool-loop cap
}

func (o Options) withDefaults() Options {
	if o.FastEffort == "" {
		o.FastEffort = "low"
	}
	if o.SlowEffort == "" {
		o.SlowEffort = "high"
	}
	if o.ConfidenceThreshold == 0 {
		o.ConfidenceThreshold = 0.4
	}
	if o.MaxToolCalls == 0 {
		o.MaxToolCalls = 5
	}
	return o
}

// Stage decides fast/slow, sets effort, and runs the tool-need pre-pass.
// It writes decisions into State.Meta; the adjacent solver stage reads them.
type Stage struct {
	Mode  string            // "fast" | "slow" | "auto" (from the Route)
	Gate  provider.Provider // cheap model for the auto gate and pre-pass
	Tools *ToolRegistry
	Opts  Options
}

func (s *Stage) ID() string { return "thinking" }

// explicitSlowFlags force slow mode deterministically — the user asked for
// care; we don't gamble on the gate model noticing.
var explicitSlowFlags = []string{"think hard", "think carefully", "be careful", "double-check", "double check"}

func (s *Stage) Run(ctx context.Context, st *pipeline.State, emit events.Emitter) (*pipeline.State, error) {
	opts := s.Opts.withDefaults()
	mode := s.Mode
	if mode == "" {
		mode = "auto"
	}
	reason := "route declared mode=" + mode
	var suggestedTools []string

	if mode == "auto" {
		mode, reason, suggestedTools = s.autoGate(ctx, st, emit)
	}

	// tool_need=required from the Receptionist always forces slow (spec 03 §3).
	if st.Class.ToolNeed == "required" && mode != "slow" {
		mode, reason = "slow", "classifier flagged tool_need=required"
	}

	effort := opts.FastEffort
	if mode == "slow" {
		effort = opts.SlowEffort
	}
	st.Meta[MetaMode] = mode
	st.Meta[MetaEffort] = effort
	st.Meta[MetaTools] = ""

	emit(events.Event{
		Kind: events.KindThinkingMode, Stage: s.ID(), Actor: s.gateName(),
		Payload: map[string]any{"mode": mode, "effort": effort, "reason": reason},
	})

	if mode == "slow" {
		s.toolPrePass(ctx, st, emit, suggestedTools)
	}
	return st, nil
}

func (s *Stage) gateName() string {
	if s.Gate != nil {
		return s.Gate.Name()
	}
	return "thinking"
}

type gateVerdict struct {
	NeedsSlow      bool     `json:"needs_slow"`
	Reasons        []string `json:"reasons"`
	SuggestedTools []string `json:"suggested_tools"`
}

// autoGate makes the runtime fast/slow decision with one cheap call plus
// deterministic overlays for explicit user flags and low classifier confidence.
func (s *Stage) autoGate(ctx context.Context, st *pipeline.State, emit events.Emitter) (mode, reason string, tools []string) {
	opts := s.Opts.withDefaults()
	lower := strings.ToLower(st.Original)
	for _, flag := range explicitSlowFlags {
		if strings.Contains(lower, flag) {
			return "slow", "explicit_user_flag: " + flag, nil
		}
	}
	if st.Class.Confidence > 0 && st.Class.Confidence < opts.ConfidenceThreshold {
		return "slow", fmt.Sprintf("low_confidence: classifier at %.2f", st.Class.Confidence), nil
	}
	if s.Gate == nil {
		return "fast", "no gate model configured", nil
	}

	system := `You are a metacognitive gate deciding whether a task needs SLOW thinking (high effort, tool checks) or FAST thinking (answer directly).

Slow triggers:
- present_state_fact: office-holders, prices, "current/latest/now", live status, anything time-sensitive that could be stale in training data
- multi_step_math: arithmetic or derivations that should not be done in-head
- repo_or_file_ref: needs to read actual code or files
- explicit_user_flag: the user asked for care ("think hard", "be careful", "check")
- high_stakes_ambiguity: genuinely ambiguous or high-stakes decisions

Respond with JSON ONLY, no prose, no fences:
{"needs_slow": bool, "reasons": ["trigger", ...], "suggested_tools": ["tool", ...]}`

	resp, err := s.Gate.Generate(ctx, provider.Request{
		System:    system,
		Messages:  []provider.Message{provider.UserText("Task: " + st.Original)},
		MaxTokens: 300,
		Metadata:  map[string]string{"task_id": st.TaskID, "stage": "thinking.gate"},
	})
	if err != nil {
		// Fail open to slow: a wasted slow pass is cheaper than a stale answer.
		return "slow", "gate error, defaulting slow: " + err.Error(), nil
	}
	st.AddTurn(s.ID(), "gate", resp.Text(), resp.Usage)

	var v gateVerdict
	if err := jsonx.Parse(resp.Text(), &v); err != nil {
		return "slow", "gate parse failure, defaulting slow", nil
	}
	if v.NeedsSlow {
		return "slow", "gate: " + strings.Join(v.Reasons, ","), v.SuggestedTools
	}
	return "fast", "gate: no slow triggers", nil
}

type prePassVerdict struct {
	MustLookUp     []string `json:"must_look_up"`
	ToolsToUse     []string `json:"tools_to_use"`
	SafeFromMemory []string `json:"safe_from_memory"`
	Verdict        string   `json:"verdict"` // "use_tools" | "answer_directly"
}

// toolPrePass is the core mechanism (spec 04 §3): before the solver answers,
// determine what must be looked up and which tools to use, and record it so
// the solver is *instructed* — not merely hoped — to ground those facts.
func (s *Stage) toolPrePass(ctx context.Context, st *pipeline.State, emit events.Emitter, gateSuggested []string) {
	if s.Tools == nil || len(s.Tools.Names()) == 0 || s.Gate == nil {
		return
	}

	system := fmt.Sprintf(`Before an AI answers the user's task, list what it would need to look up. Anything that is a present-state fact (could have changed since training) MUST be looked up, not answered from memory.

Available tools: %s

Respond with JSON ONLY, no prose, no fences:
{"must_look_up": ["fact", ...], "tools_to_use": ["tool", ...], "safe_from_memory": ["fact", ...], "verdict": "use_tools" | "answer_directly"}`,
		strings.Join(s.Tools.Names(), ", "))

	resp, err := s.Gate.Generate(ctx, provider.Request{
		System:    system,
		Messages:  []provider.Message{provider.UserText("Task: " + st.Original)},
		MaxTokens: 400,
		Metadata:  map[string]string{"task_id": st.TaskID, "stage": "thinking.prepass"},
	})

	var v prePassVerdict
	switch {
	case err != nil:
		// Pre-pass failure falls back to the gate's suggestion, if any.
		v = prePassVerdict{ToolsToUse: gateSuggested}
		if len(gateSuggested) > 0 {
			v.Verdict = "use_tools"
		}
	default:
		st.AddTurn(s.ID(), "prepass", resp.Text(), resp.Usage)
		if perr := jsonx.Parse(resp.Text(), &v); perr != nil {
			v = prePassVerdict{ToolsToUse: gateSuggested}
			if len(gateSuggested) > 0 {
				v.Verdict = "use_tools"
			}
		}
	}

	// Only keep tools that actually exist in the registry.
	var usable []string
	for _, name := range v.ToolsToUse {
		if _, ok := s.Tools.Get(strings.TrimSpace(name)); ok {
			usable = append(usable, strings.TrimSpace(name))
		}
	}
	needsTool := v.Verdict == "use_tools" && len(usable) > 0
	if needsTool {
		st.Meta[MetaTools] = strings.Join(usable, ",")
	}

	emit(events.Event{
		Kind: events.KindThinkingToolChk, Stage: s.ID(), Actor: s.gateName(),
		Payload: map[string]any{
			"needs_tool":   needsTool,
			"verdict":      v.Verdict,
			"tools":        usable,
			"must_look_up": v.MustLookUp,
		},
	})
}

// SolverEffort reads the effort the Thinking stage chose for the solver.
func SolverEffort(st *pipeline.State) string {
	if e := st.Meta[MetaEffort]; e != "" {
		return e
	}
	return "medium"
}

// FlaggedTools returns the tool names the solver has been instructed to use.
func FlaggedTools(st *pipeline.State) []string {
	raw := st.Meta[MetaTools]
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ToolInstruction is appended to a solver's system prompt when tools were
// flagged by the pre-pass.
func ToolInstruction(tools []string) string {
	if len(tools) == 0 {
		return ""
	}
	return fmt.Sprintf("\n\nYou have been flagged to use these tools before answering: %s. Do not answer from memory for the flagged facts — call the tools first and ground your answer in their results.",
		strings.Join(tools, ", "))
}
