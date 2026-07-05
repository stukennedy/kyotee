package twobrain

import (
	"context"
	"strings"
	"testing"

	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/pipeline"
	"github.com/stukennedy/kyotee/internal/provider"
)

func TestTwoBrainExchangeAndReferee(t *testing.T) {
	div := provider.NewFake("right", "openai",
		provider.TextResponse("idea1, idea2, weird idea3", 40, 40))
	conv := provider.NewFake("left", "anthropic",
		provider.TextResponse("idea2 survives scrutiny; discard the rest", 40, 40))
	ref := provider.NewFake("referee", "anthropic",
		provider.TextResponse("Final: go with idea2.", 60, 30))

	st := pipeline.NewState("tb", "design a cache eviction strategy")
	st.Meta[pipelineMetaEffort] = "high"
	var evs []events.Event
	emit := func(ev events.Event) { evs = append(evs, ev) }

	stage := &Stage{Divergent: div, Convergent: conv, Referee: ref, Rounds: 2}
	if _, err := stage.Run(context.Background(), st, emit); err != nil {
		t.Fatal(err)
	}

	if st.Draft != "Final: go with idea2." {
		t.Fatalf("draft should come from referee: %q", st.Draft)
	}
	// 2 rounds × 2 personas + 1 referee = 5 brain.turn events.
	var turns []events.Event
	roles := map[string]int{}
	for _, ev := range evs {
		if ev.Kind == events.KindBrainTurn {
			turns = append(turns, ev)
			role, _ := ev.Payload["role"].(string)
			roles[role]++
		}
	}
	if len(turns) != 5 || roles["divergent"] != 2 || roles["convergent"] != 2 || roles["referee"] != 1 {
		t.Fatalf("unexpected brain.turn events: %d total, roles %v", len(turns), roles)
	}
	// Convergent must see the divergent output; referee must see both.
	if !strings.Contains(conv.Requests[0].Messages[0].Content[0].Text, "idea1") {
		t.Fatal("convergent prompt missing divergent output")
	}
	refPrompt := ref.Requests[0].Messages[0].Content[0].Text
	if !strings.Contains(refPrompt, "divergent") || !strings.Contains(refPrompt, "convergent") {
		t.Fatal("referee prompt missing the exchange")
	}
	// Effort propagates to every call.
	for _, f := range []*provider.Fake{div, conv, ref} {
		for _, r := range f.Requests {
			if r.ReasoningEffort != "high" {
				t.Fatalf("%s: effort not propagated", f.ModelName)
			}
		}
	}
}

const pipelineMetaEffort = "thinking.effort"
