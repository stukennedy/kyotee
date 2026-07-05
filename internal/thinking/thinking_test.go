package thinking

import (
	"context"
	"fmt"
	"testing"

	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/pipeline"
	"github.com/stukennedy/kyotee/internal/provider"
)

func collect() (events.Emitter, *[]events.Event) {
	var evs []events.Event
	return func(ev events.Event) { evs = append(evs, ev) }, &evs
}

func kinds(evs []events.Event, kind string) []events.Event {
	var out []events.Event
	for _, ev := range evs {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// fakeSearch is a scripted web_search stand-in.
func fakeSearch(result string) *ToolRegistry {
	return NewToolRegistry(&FuncTool{
		Definition: provider.ToolDef{
			Name:        "web_search",
			Description: "search the web",
			Schema:      map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}},
		},
		Fn: func(context.Context, map[string]any) (string, error) { return result, nil },
	})
}

// TestPrimeMinisterEndToEnd: auto gate → slow, pre-pass → use_tools, solver
// performs the web_search call, answer grounded in the tool result.
func TestPrimeMinisterEndToEnd(t *testing.T) {
	gate := provider.NewFake("gate", "anthropic",
		provider.TextResponse(`{"needs_slow": true, "reasons": ["present_state_fact"], "suggested_tools": ["web_search"]}`, 20, 20),
		provider.TextResponse(`{"must_look_up": ["current UK prime minister"], "tools_to_use": ["web_search"], "safe_from_memory": [], "verdict": "use_tools"}`, 20, 20),
	)
	tools := fakeSearch("BREAKING: Jo Bloggs became UK prime minister last month.")

	st := pipeline.NewState("pm", "who is the prime minister?")
	st.Class = pipeline.Classification{Complexity: "trivial", Domain: "research", ToolNeed: "likely", Confidence: 0.9}
	emit, evs := collect()

	thinkStage := &Stage{Mode: "auto", Gate: gate, Tools: tools}
	if _, err := thinkStage.Run(context.Background(), st, emit); err != nil {
		t.Fatal(err)
	}
	if st.Meta[MetaMode] != "slow" {
		t.Fatalf("gate should pick slow, got %q", st.Meta[MetaMode])
	}
	if st.Meta[MetaTools] != "web_search" {
		t.Fatalf("pre-pass should flag web_search, got %q", st.Meta[MetaTools])
	}
	toolChecks := kinds(*evs, events.KindThinkingToolChk)
	if len(toolChecks) != 1 || toolChecks[0].Payload["needs_tool"] != true {
		t.Fatalf("expected thinking.tool_check with needs_tool=true, got %+v", toolChecks)
	}

	// Solver: first requests the tool, then answers from its result.
	solver := &provider.Fake{ModelName: "solver", VendorName: "anthropic",
		ScriptFn: func(call int, req provider.Request) (provider.Response, error) {
			if call == 0 {
				if len(req.Tools) == 0 {
					return provider.Response{}, fmt.Errorf("solver got no tools")
				}
				return provider.Response{
					Content: []provider.Block{{Type: "tool_use", ToolCall: &provider.ToolCall{
						ID: "c1", Name: "web_search", Input: map[string]any{"query": "current UK prime minister"},
					}}},
					StopReason: "tool_use",
					Usage:      provider.Usage{InputTokens: 50, OutputTokens: 20},
				}, nil
			}
			// Must have received the tool result.
			last := req.Messages[len(req.Messages)-1]
			if last.Role != "tool" {
				return provider.Response{}, fmt.Errorf("expected tool result message, got role %s", last.Role)
			}
			return provider.TextResponse("The prime minister is Jo Bloggs, per the latest search results.", 100, 30), nil
		}}

	solo := &Solo{Model: solver, Tools: tools}
	if _, err := solo.Run(context.Background(), st, emit); err != nil {
		t.Fatal(err)
	}
	if len(kinds(*evs, events.KindToolCall)) != 1 || len(kinds(*evs, events.KindToolResult)) != 1 {
		t.Fatal("expected exactly one tool.call and one tool.result event")
	}
	if st.Draft == "" || st.Draft != "The prime minister is Jo Bloggs, per the latest search results." {
		t.Fatalf("draft not grounded in tool result: %q", st.Draft)
	}
}

// Timeless prompt: gate says fast, no pre-pass call, no tool cost.
func TestTimelessPromptStaysFast(t *testing.T) {
	gate := provider.NewFake("gate", "anthropic",
		provider.TextResponse(`{"needs_slow": false, "reasons": [], "suggested_tools": []}`, 20, 10))
	st := pipeline.NewState("hm", "what is a hash map?")
	st.Class = pipeline.Classification{Confidence: 0.95}
	emit, evs := collect()

	stage := &Stage{Mode: "auto", Gate: gate, Tools: fakeSearch("unused")}
	if _, err := stage.Run(context.Background(), st, emit); err != nil {
		t.Fatal(err)
	}
	if st.Meta[MetaMode] != "fast" {
		t.Fatalf("want fast, got %q", st.Meta[MetaMode])
	}
	if len(gate.Requests) != 1 {
		t.Fatalf("pre-pass must not run on fast path; gate calls = %d", len(gate.Requests))
	}
	if len(kinds(*evs, events.KindThinkingToolChk)) != 0 {
		t.Fatal("no tool_check event expected on fast path")
	}
}

func TestExplicitUserFlagForcesSlow(t *testing.T) {
	// Gate would say fast — the deterministic overlay must win without a call.
	gate := provider.NewFake("gate", "anthropic",
		provider.TextResponse(`{"needs_slow": false}`, 10, 10),
		provider.TextResponse(`{"verdict": "answer_directly"}`, 10, 10))
	st := pipeline.NewState("th", "think hard: what's 2+2?")
	st.Class = pipeline.Classification{Confidence: 0.99}
	emit, _ := collect()

	stage := &Stage{Mode: "auto", Gate: gate, Tools: fakeSearch("unused")}
	if _, err := stage.Run(context.Background(), st, emit); err != nil {
		t.Fatal(err)
	}
	if st.Meta[MetaMode] != "slow" {
		t.Fatalf("explicit user flag must force slow, got %q", st.Meta[MetaMode])
	}
}

func TestEffortPropagatesToSolver(t *testing.T) {
	st := pipeline.NewState("ef", "task")
	st.Meta[MetaMode] = "slow"
	st.Meta[MetaEffort] = "high"

	var captured string
	solver := &provider.Fake{ModelName: "solver", VendorName: "anthropic",
		ScriptFn: func(_ int, req provider.Request) (provider.Response, error) {
			captured = req.ReasoningEffort
			return provider.TextResponse("done", 10, 10), nil
		}}
	emit, _ := collect()
	solo := &Solo{Model: solver}
	if _, err := solo.Run(context.Background(), st, emit); err != nil {
		t.Fatal(err)
	}
	if captured != "high" {
		t.Fatalf("ReasoningEffort not propagated: %q", captured)
	}
}

// The tool loop must terminate at the cap even against a model that always
// asks for another tool call.
func TestToolLoopTerminatesAtCap(t *testing.T) {
	calls := 0
	greedy := &provider.Fake{ModelName: "greedy", VendorName: "anthropic",
		ScriptFn: func(call int, req provider.Request) (provider.Response, error) {
			calls++
			if len(req.Tools) == 0 {
				return provider.TextResponse("final answer", 10, 10), nil
			}
			return provider.Response{
				Content: []provider.Block{{Type: "tool_use", ToolCall: &provider.ToolCall{
					ID: fmt.Sprintf("c%d", call), Name: "web_search", Input: map[string]any{"query": "more"},
				}}},
				StopReason: "tool_use",
				Usage:      provider.Usage{InputTokens: 10, OutputTokens: 10},
			}, nil
		}}
	tools := fakeSearch("result")
	emit, _ := collect()

	req := provider.Request{
		Messages: []provider.Message{provider.UserText("go")},
		Tools:    tools.Defs(),
	}
	resp, _, err := RunToolLoop(context.Background(), greedy, req, tools, 3, emit, "solo")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text() != "final answer" {
		t.Fatalf("loop did not force a final answer: %q", resp.Text())
	}
	if calls > 6 {
		t.Fatalf("too many generate calls (%d) — loop not bounded", calls)
	}
}
