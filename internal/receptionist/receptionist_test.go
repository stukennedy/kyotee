package receptionist

import (
	"context"
	"strings"
	"testing"

	"github.com/stukennedy/kyotee/internal/config"
	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/pipeline"
	"github.com/stukennedy/kyotee/internal/provider"
	"github.com/stukennedy/kyotee/internal/thinking"
)

func testConfig() *config.Config {
	c := &config.Config{
		Providers: []config.Provider{
			{Name: "cheap", Kind: "mock", Vendor: "anthropic"},
			{Name: "mid", Kind: "mock", Vendor: "anthropic"},
			{Name: "strong", Kind: "mock", Vendor: "anthropic"},
			{Name: "other", Kind: "mock", Vendor: "openai"},
		},
		Models:  config.ModelRoles{Receptionist: "cheap", Default: "mid"},
		Council: config.Council{Members: []string{"mid", "other", "strong"}},
		Routes: []config.Route{
			{When: config.When{Complexity: "trivial"}, Strategy: "solo", Thinking: "fast",
				Models: config.Models{Primary: "cheap"}, MaxCostUSD: 0.10},
			{When: config.When{Domain: "code", Complexity: "standard"}, Strategy: "solo", Thinking: "auto",
				Models: config.Models{Primary: "mid"}},
			{When: config.When{Domain: "code", Complexity: "hard"}, Strategy: "twobrain", Thinking: "slow",
				Models: config.Models{Primary: "strong", Divergent: "other", Convergent: "mid"}},
			{When: config.When{Domain: "reasoning", Complexity: "hard"}, Strategy: "council", Thinking: "slow",
				Models: config.Models{Primary: "strong", Council: []string{"mid", "other", "strong"}}},
			{Strategy: "solo", Thinking: "auto", Models: config.Models{Primary: "mid"}},
		},
	}
	c.Defaults()
	if err := c.Validate(); err != nil {
		panic(err)
	}
	return c
}

func newReceptionist(c *config.Config) *Receptionist {
	return &Receptionist{
		Cfg:      config.NewHolder(c),
		Registry: config.BuildRegistry(c),
		Tools:    thinking.NewToolRegistry(),
	}
}

func TestRoutingRulesFirstMatchWins(t *testing.T) {
	cfg := testConfig()
	cases := []struct {
		class    pipeline.Classification
		strategy string
		mode     string
		primary  string
	}{
		{pipeline.Classification{Complexity: "trivial", Domain: "chat"}, "solo", "fast", "cheap"},
		{pipeline.Classification{Complexity: "standard", Domain: "code"}, "solo", "auto", "mid"},
		{pipeline.Classification{Complexity: "hard", Domain: "code"}, "twobrain", "slow", "strong"},
		{pipeline.Classification{Complexity: "hard", Domain: "reasoning"}, "council", "slow", "strong"},
		{pipeline.Classification{Complexity: "standard", Domain: "creative"}, "solo", "auto", "mid"},
	}
	for _, tc := range cases {
		route := MatchRoute(cfg, tc.class)
		if route.Strategy != tc.strategy || route.ThinkingMode != tc.mode || route.Models.Primary != tc.primary {
			t.Errorf("class %+v: got (%s,%s,%s), want (%s,%s,%s)",
				tc.class, route.Strategy, route.ThinkingMode, route.Models.Primary,
				tc.strategy, tc.mode, tc.primary)
		}
	}
}

func stageIDs(stages []pipeline.Stage) []string {
	ids := make([]string, len(stages))
	for i, s := range stages {
		ids[i] = s.ID()
	}
	return ids
}

func TestPipelineAssemblyPerStrategy(t *testing.T) {
	cfg := testConfig()
	r := newReceptionist(cfg)
	emit := func(events.Event) {}

	cases := []struct {
		class pipeline.Classification
		want  []string
	}{
		{pipeline.Classification{Complexity: "trivial", Domain: "chat", ToolNeed: "none"},
			[]string{"thinking", "solo", "output"}},
		{pipeline.Classification{Complexity: "hard", Domain: "code", ToolNeed: "none"},
			[]string{"thinking", "twobrain", "output"}},
		{pipeline.Classification{Complexity: "hard", Domain: "reasoning", ToolNeed: "none"},
			[]string{"thinking", "council", "synthesis", "output"}},
	}
	for _, tc := range cases {
		st := pipeline.NewState("t", "task")
		st.Class = tc.class
		st.Budget.LimitUSD = 0
		stages, err := r.Intake(context.Background(), st, Overrides{}, emit)
		if err != nil {
			t.Fatal(err)
		}
		got := stageIDs(stages)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("class %+v: pipeline %v, want %v", tc.class, got, tc.want)
		}
		if got[len(got)-1] != "output" {
			t.Errorf("Output must be terminal, got %v", got)
		}
	}
}

func TestPreflightDowngradesCouncilToSolo(t *testing.T) {
	cfg := testConfig()
	// Make council members expensive so the estimate blows a small ceiling.
	for i := range cfg.Providers {
		cfg.Providers[i].Cost = config.Cost{Input: 100, Output: 500}
	}
	cfg.Budget.DefaultLimitUSD = 0.01
	for i := range cfg.Routes {
		cfg.Routes[i].MaxCostUSD = 0
	}
	r := newReceptionist(cfg)

	var warns []events.Event
	emit := func(ev events.Event) {
		if ev.Kind == events.KindBudgetWarn {
			warns = append(warns, ev)
		}
	}

	st := pipeline.NewState("t", "hard reasoning task")
	st.Class = pipeline.Classification{Complexity: "hard", Domain: "reasoning", ToolNeed: "none"}
	stages, err := r.Intake(context.Background(), st, Overrides{}, emit)
	if err != nil {
		t.Fatal(err)
	}
	got := stageIDs(stages)
	if strings.Join(got, ",") != "thinking,solo,output" {
		t.Fatalf("expected downgrade to solo pipeline, got %v", got)
	}
	if len(warns) == 0 {
		t.Fatal("expected budget.warn on preflight downgrade")
	}
	if reason, _ := warns[0].Payload["reason"].(string); !strings.Contains(reason, "downgrading to solo") {
		t.Fatalf("warn payload missing downgrade reason: %+v", warns[0].Payload)
	}
}

func TestClassifierBattery(t *testing.T) {
	cfg := testConfig()
	r := newReceptionist(cfg)
	cheap, _ := r.Registry.Get("cheap")
	fake := cheap.(*provider.Fake)

	cases := []struct {
		prompt   string
		verdict  string
		toolNeed string
	}{
		{"hey, how's it going?", `{"complexity":"trivial","domain":"chat","tool_need":"none","confidence":0.95,"rationale":"greeting"}`, "none"},
		{"write a function to reverse a linked list", `{"complexity":"standard","domain":"code","tool_need":"none","confidence":0.9,"rationale":"standard code"}`, "none"},
		{"who is the current UK prime minister?", `{"complexity":"trivial","domain":"research","tool_need":"required","confidence":0.9,"rationale":"present-state fact"}`, "required"},
	}
	for _, tc := range cases {
		fake.Script = []provider.Response{provider.TextResponse(tc.verdict, 50, 30)}
		fake.Requests = nil
		st := pipeline.NewState("t", tc.prompt)
		class := r.Classify(context.Background(), st, func(events.Event) {})
		if class.ToolNeed != tc.toolNeed {
			t.Errorf("%q: tool_need %q, want %q", tc.prompt, class.ToolNeed, tc.toolNeed)
		}
	}
}

func TestClassifierParseFailureFallsBack(t *testing.T) {
	cfg := testConfig()
	r := newReceptionist(cfg)
	cheap, _ := r.Registry.Get("cheap")
	cheap.(*provider.Fake).Script = []provider.Response{provider.TextResponse("I think this is a standard task!", 10, 10)}

	var warned bool
	st := pipeline.NewState("t", "anything")
	class := r.Classify(context.Background(), st, func(ev events.Event) {
		if ev.Kind == events.KindError {
			warned = true
		}
	})
	if class != fallbackClass {
		t.Fatalf("expected fallback classification, got %+v", class)
	}
	if !warned {
		t.Fatal("expected a warn event on parse failure")
	}
}

func TestOverridesForceStrategy(t *testing.T) {
	cfg := testConfig()
	r := newReceptionist(cfg)
	st := pipeline.NewState("t", "simple question")
	st.Class = pipeline.Classification{Complexity: "trivial", Domain: "chat", ToolNeed: "none"}

	stages, err := r.Intake(context.Background(), st, Overrides{Strategy: "council", MaxCostUSD: 100}, func(events.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	got := stageIDs(stages)
	if strings.Join(got, ",") != "thinking,council,synthesis,output" {
		t.Fatalf("override to council not honored: %v", got)
	}
	if st.Budget.LimitUSD != 100 {
		t.Fatalf("budget override not applied: %v", st.Budget.LimitUSD)
	}
}
