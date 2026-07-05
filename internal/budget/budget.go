// Package budget is the load-bearing cost control (spec 03 §5): warn
// thresholds, and preflight estimation used to downgrade expensive
// strategies before they start.
package budget

import (
	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/pipeline"
	"github.com/stukennedy/kyotee/internal/provider"
)

// WarnThresholds are the fractions of the ceiling at which budget.warn fires.
var WarnThresholds = []float64{0.50, 0.80, 0.95}

// CheckWarn emits budget.warn once per crossed threshold. Fired thresholds
// are recorded in the state's BudgetState so they survive resume. Stages
// call this after accounting each provider call; the Executor also calls it
// between stages.
func CheckWarn(b *pipeline.BudgetState, emit events.Emitter) {
	if b.LimitUSD <= 0 {
		return
	}
	pct := b.SpentUSD / b.LimitUSD
	for _, th := range WarnThresholds {
		if pct < th {
			continue
		}
		fired := false
		for _, w := range b.WarnedPct {
			if w == th {
				fired = true
				break
			}
		}
		if fired {
			continue
		}
		b.WarnedPct = append(b.WarnedPct, th)
		emit(events.Event{
			Kind: events.KindBudgetWarn,
			Payload: map[string]any{
				"spent_usd": b.SpentUSD,
				"limit_usd": b.LimitUSD,
				"pct":       pct,
				"threshold": th,
			},
		})
	}
}

// Estimate is a worst-case spend projection for a strategy.
type Estimate struct {
	Calls           int
	AvgInputTokens  int
	AvgOutputTokens int
}

// DefaultAvgInput/Output are conservative per-call token assumptions used
// when config doesn't override them.
const (
	DefaultAvgInput  = 2000
	DefaultAvgOutput = 1200
)

// EstimateCouncil projects worst-case council spend:
// members × (1 opening + rounds rebuttals) + consensus checks + synthesis.
func EstimateCouncil(members []provider.Provider, rounds int, synth provider.Provider) float64 {
	total := 0.0
	for _, m := range members {
		total += provider.CostFor(m, DefaultAvgInput, DefaultAvgOutput) * float64(1+rounds)
	}
	if synth != nil {
		// Synthesis reads the whole debate: scale input by member count.
		total += provider.CostFor(synth, DefaultAvgInput*(1+len(members)), DefaultAvgOutput)
	}
	return total
}

// EstimateTwoBrain projects worst-case two-brain spend:
// rounds × (divergent + convergent) + referee synthesis.
func EstimateTwoBrain(div, conv, referee provider.Provider, rounds int) float64 {
	total := 0.0
	for _, p := range []provider.Provider{div, conv} {
		if p != nil {
			total += provider.CostFor(p, DefaultAvgInput, DefaultAvgOutput) * float64(rounds)
		}
	}
	if referee != nil {
		total += provider.CostFor(referee, DefaultAvgInput*3, DefaultAvgOutput)
	}
	return total
}
