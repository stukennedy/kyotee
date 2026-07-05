package twobrain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/pipeline"
	"github.com/stukennedy/kyotee/internal/provider"
)

const metaEffort = "thinking.effort"

func TestTwoBrainExchangeAndReferee(t *testing.T) {
	div := provider.NewFake("right", "openai",
		provider.TextResponse("idea1, idea2, weird idea3", 40, 40))
	conv := provider.NewFake("left", "anthropic",
		provider.TextResponse("idea2 survives scrutiny; discard the rest", 40, 40))
	ref := provider.NewFake("referee", "anthropic",
		provider.TextResponse("Final: go with idea2.", 60, 30))

	st := pipeline.NewState("tb", "design a cache eviction strategy")
	st.Meta[metaEffort] = "high"
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
	roles := map[string]int{}
	total := 0
	for _, ev := range evs {
		if ev.Kind == events.KindBrainTurn {
			total++
			role, _ := ev.Payload["role"].(string)
			roles[role]++
		}
	}
	if total != 5 || roles["divergent"] != 2 || roles["convergent"] != 2 || roles["referee"] != 1 {
		t.Fatalf("unexpected brain.turn events: %d total, roles %v", total, roles)
	}
	// Convergent must see the divergent output; referee must see the full
	// exchange (both personas, both rounds).
	if !strings.Contains(conv.Requests[0].Messages[0].Content[0].Text, "idea1") {
		t.Fatal("convergent prompt missing divergent output")
	}
	refPrompt := ref.Requests[0].Messages[0].Content[0].Text
	for _, want := range []string{"round 1, divergent", "round 2, convergent", "idea1", "idea2 survives"} {
		if !strings.Contains(refPrompt, want) {
			t.Fatalf("referee prompt missing %q", want)
		}
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

// Temperature is the mechanism (spec 05): divergent turns must use the high
// temperature and convergent turns the low one.
func TestTemperatureSplit(t *testing.T) {
	div := provider.NewFake("d", "anthropic", provider.TextResponse("ideas", 10, 10))
	conv := provider.NewFake("c", "anthropic", provider.TextResponse("critique", 10, 10))
	ref := provider.NewFake("r", "anthropic", provider.TextResponse("final", 10, 10))

	st := pipeline.NewState("tt", "task")
	stage := &Stage{Divergent: div, Convergent: conv, Referee: ref, Rounds: 2,
		DivTemp: 1.0, ConvTemp: 0.3}
	if _, err := stage.Run(context.Background(), st, func(events.Event) {}); err != nil {
		t.Fatal(err)
	}
	for _, r := range div.Requests {
		if r.Temperature != 1.0 {
			t.Fatalf("divergent temp %v, want 1.0", r.Temperature)
		}
	}
	for _, r := range conv.Requests {
		if r.Temperature != 0.3 {
			t.Fatalf("convergent temp %v, want 0.3", r.Temperature)
		}
	}
}

// Rounds above the hard cap are clamped.
func TestRoundsHardCap(t *testing.T) {
	div := provider.NewFake("d", "anthropic", provider.TextResponse("x", 1, 1))
	conv := provider.NewFake("c", "anthropic", provider.TextResponse("y", 1, 1))
	ref := provider.NewFake("r", "anthropic", provider.TextResponse("z", 1, 1))

	st := pipeline.NewState("tc", "task")
	stage := &Stage{Divergent: div, Convergent: conv, Referee: ref, Rounds: 9}
	if _, err := stage.Run(context.Background(), st, func(events.Event) {}); err != nil {
		t.Fatal(err)
	}
	if len(div.Requests) != RoundsMax {
		t.Fatalf("divergent ran %d rounds, want clamp at %d", len(div.Requests), RoundsMax)
	}
}

// Persona prompts load from external files referenced in config.
func TestLoadPromptsFromFiles(t *testing.T) {
	dir := t.TempDir()
	divPath := filepath.Join(dir, "divergent.md")
	os.WriteFile(divPath, []byte("CUSTOM DIVERGENT PROMPT"), 0o644)

	p, err := LoadPrompts(divPath, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Divergent != "CUSTOM DIVERGENT PROMPT" {
		t.Fatalf("divergent prompt not loaded: %q", p.Divergent)
	}
	if p.Convergent != DefaultPrompts.Convergent {
		t.Fatal("unset paths must keep defaults")
	}
	if _, err := LoadPrompts(filepath.Join(dir, "missing.md"), "", ""); err == nil {
		t.Fatal("missing prompt file must error")
	}

	// And the stage must actually use it.
	div := provider.NewFake("d", "anthropic", provider.TextResponse("x", 1, 1))
	conv := provider.NewFake("c", "anthropic", provider.TextResponse("y", 1, 1))
	ref := provider.NewFake("r", "anthropic", provider.TextResponse("z", 1, 1))
	st := pipeline.NewState("tp", "task")
	stage := &Stage{Divergent: div, Convergent: conv, Referee: ref, Rounds: 1, Prompts: p}
	if _, err := stage.Run(context.Background(), st, func(events.Event) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(div.Requests[0].System, "CUSTOM DIVERGENT PROMPT") {
		t.Fatal("stage did not use loaded divergent prompt")
	}
}
