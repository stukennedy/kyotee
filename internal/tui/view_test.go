package tui

import (
	"strings"
	"testing"

	"github.com/stukennedy/tooey/tooeytest"

	"github.com/stukennedy/kyotee/internal/receptionist"
)

// Golden-frame checks via the Tooey v0.5 tooeytest package: the view renders
// the observed engine state without panicking and places key regions.
func TestMainViewRenders(t *testing.T) {
	m := NewModel(NewClient("http://localhost:0"))
	m.TaskID = "t-123"
	m.Strategy = "council"
	m.Pipeline = []string{"thinking", "council", "synthesis", "output"}
	m.Class = map[string]any{"domain": "reasoning", "complexity": "hard", "tool_need": "none"}
	m.SpentUSD, m.LimitUSD = 0.42, 3.00
	m.ThinkMode = "slow (effort: high)"
	m.memberView("claude").Position = "position A"
	m.memberView("gpt").Position = "position B"
	m.Consensus = "✓ consensus (vote, 2 rounds)"

	frame := tooeytest.RenderText(View(m, ""), 130, 40)
	for _, want := range []string{"Kyotee Harness", "Routing", "council", "claude", "gpt", "consensus", "$0.42"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame missing %q:\n%s", want, frame)
		}
	}
}

func TestOverrideModalOverlaysMainUI(t *testing.T) {
	m := NewModel(NewClient("http://localhost:0"))
	m.Active = overlayOverride
	m.Override = receptionist.Overrides{Strategy: "council"}

	frame := tooeytest.RenderText(View(m, ""), 120, 36)
	if !strings.Contains(frame, "Override & escalate") {
		t.Fatalf("override modal not rendered:\n%s", frame)
	}
	// The base UI must still be painted underneath the overlay.
	if !strings.Contains(frame, "Kyotee Harness") {
		t.Fatal("overlay should paint on top of the main UI, not replace it")
	}
	if !strings.Contains(frame, "council") {
		t.Fatal("override values not shown")
	}
}

func TestCostMeterThresholdColours(t *testing.T) {
	m := NewModel(NewClient("http://localhost:0"))
	m.LimitUSD = 1.0
	for _, spent := range []float64{0.1, 0.6, 0.85, 0.99} {
		m.SpentUSD = spent
		frame := tooeytest.RenderText(m.costMeter(), 60, 1)
		if !strings.Contains(frame, "cost:") {
			t.Fatalf("cost meter missing at %v:\n%s", spent, frame)
		}
	}
}
