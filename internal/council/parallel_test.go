package council

import (
	"context"
	"testing"
	"time"

	"github.com/stukennedy/kyotee/internal/pipeline"
	"github.com/stukennedy/kyotee/internal/provider"
)

// Opening turns must execute in parallel: three members that each take
// ~60ms should finish their openings in far less than 3×60ms.
func TestOpeningsRunInParallel(t *testing.T) {
	slow := func(name, vendor string) *provider.Fake {
		return &provider.Fake{ModelName: name, VendorName: vendor,
			ScriptFn: func(call int, _ provider.Request) (provider.Response, error) {
				time.Sleep(60 * time.Millisecond)
				return provider.TextResponse(`p `+name+`. {"choice": "A", "confidence": 1, "agree_with_group": true}`, 10, 10), nil
			}}
	}
	members := []provider.Provider{slow("m1", "a"), slow("m2", "b"), slow("m3", "c")}
	st := pipeline.NewState("cp", "q")
	emit, _, _ := collect()

	start := time.Now()
	stage := &Stage{Members: members, Rounds: 1, Consensus: ConsensusConfig{Method: "vote", Threshold: 0.66}}
	if _, err := stage.Run(context.Background(), st, emit); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Fatalf("openings appear sequential: %v for 3×60ms members", elapsed)
	}
}

// Mid-debate budget exhaustion must stop rounds early and still leave
// positions for a best-effort synthesis.
func TestBudgetHaltMidDebate(t *testing.T) {
	expensive := func(name, vendor string) *provider.Fake {
		f := &provider.Fake{ModelName: name, VendorName: vendor,
			ScriptFn: func(int, provider.Request) (provider.Response, error) {
				return provider.Response{
					Content:    []provider.Block{{Type: "text", Text: `pos. {"choice": "` + name + `", "confidence": 1, "agree_with_group": false}`}},
					StopReason: "end_turn",
					Usage:      provider.Usage{InputTokens: 10, OutputTokens: 10, CostUSD: 0.5},
				}, nil
			}}
		return f
	}
	members := []provider.Provider{expensive("m1", "a"), expensive("m2", "b"), expensive("m3", "c")}
	st := pipeline.NewState("cb", "q")
	st.Budget.LimitUSD = 1.0 // exhausted after the three openings (1.5)
	emit, _, _ := collect()

	stage := &Stage{Members: members, Rounds: 3, Consensus: ConsensusConfig{Method: "vote", Threshold: 0.9}}
	if _, err := stage.Run(context.Background(), st, emit); err != nil {
		t.Fatal(err)
	}
	if st.Meta[MetaOutcome] != "budget_halt" {
		t.Fatalf("expected budget_halt outcome, got %q", st.Meta[MetaOutcome])
	}

	synth := provider.NewFake("synth", "anthropic", provider.TextResponse("best-effort answer", 50, 20))
	sy := &Synthesis{Model: synth}
	if _, err := sy.Run(context.Background(), st, emit); err != nil {
		t.Fatal(err)
	}
	if st.Draft != "best-effort answer" {
		t.Fatalf("synthesis should still produce a draft: %q", st.Draft)
	}
}
