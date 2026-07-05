// Package receptionist is the always-on front door (spec 03): it classifies
// each task with a cheap model, routes it to a strategy via config rules,
// assembles the stage pipeline, and enforces the cost budget. It classifies;
// it does not solve.
package receptionist

import (
	"context"
	"fmt"

	"github.com/stukennedy/kyotee/internal/budget"
	"github.com/stukennedy/kyotee/internal/config"
	"github.com/stukennedy/kyotee/internal/council"
	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/pipeline"
	"github.com/stukennedy/kyotee/internal/provider"
	"github.com/stukennedy/kyotee/internal/thinking"
	"github.com/stukennedy/kyotee/internal/twobrain"
)

// Route is the resolved solving strategy for one task (spec 03 §3).
type Route struct {
	Strategy     string // "solo" | "twobrain" | "council"
	ThinkingMode string // "fast" | "slow" | "auto"
	Models       config.Models
	MaxCostUSD   float64 // per-task ceiling (0 = inherit global default)
}

// Overrides are per-task knobs from the TUI/API (spec 08 §5); zero values
// mean "no override".
type Overrides struct {
	Strategy        string  `json:"strategy,omitempty"`
	Thinking        string  `json:"thinking,omitempty"`
	MaxCostUSD      float64 `json:"max_cost_usd,omitempty"`
	CouncilRounds   int     `json:"council_rounds,omitempty"`
	ConsensusMethod string  `json:"consensus_method,omitempty"`
}

type Receptionist struct {
	Cfg      *config.Holder
	Registry provider.Registry
	Tools    *thinking.ToolRegistry
	Embedder council.Embedder
}

// Intake classifies, routes, preflights, and assembles the pipeline for a
// task. On resume the existing classification is reused and completed stages
// are skipped by the Executor via State.Checkpoints.
func (r *Receptionist) Intake(ctx context.Context, st *pipeline.State, ov Overrides, emit events.Emitter) ([]pipeline.Stage, error) {
	cfg := r.Cfg.Get()

	if st.Class.Complexity == "" {
		st.Class = r.Classify(ctx, st, emit)
	}
	emit(events.Event{
		Kind: events.KindTaskClassified, Actor: "receptionist",
		Payload: map[string]any{
			"complexity": st.Class.Complexity, "domain": st.Class.Domain,
			"tool_need": st.Class.ToolNeed, "confidence": st.Class.Confidence,
			"rationale": st.Class.Rationale,
		},
	})

	route := MatchRoute(cfg, st.Class)
	applyOverrides(&route, ov)

	// Budget ceiling: per-route override, else global default. Never lower an
	// already-set limit on resume.
	if st.Budget.LimitUSD == 0 {
		limit := route.MaxCostUSD
		if limit == 0 {
			limit = cfg.Budget.DefaultLimitUSD
		}
		st.Budget.LimitUSD = limit
	}
	if ov.MaxCostUSD > 0 {
		st.Budget.LimitUSD = ov.MaxCostUSD
	}

	route = r.preflight(route, st, cfg, emit)

	stages, stageIDs, modelNames, err := r.assemble(route, cfg, ov)
	if err != nil {
		return nil, err
	}

	st.Meta["strategy"] = route.Strategy
	emit(events.Event{
		Kind: events.KindTaskRouted, Actor: "receptionist",
		Payload: map[string]any{
			"strategy": route.Strategy, "thinking": route.ThinkingMode,
			"pipeline": stageIDs, "models": modelNames,
			"limit_usd": st.Budget.LimitUSD,
		},
	})
	return stages, nil
}

// MatchRoute applies config rules top-to-bottom; first match wins. If no
// rule matches, a safe solo/auto default on the default model is returned.
func MatchRoute(cfg *config.Config, class pipeline.Classification) Route {
	for _, rule := range cfg.Routes {
		if rule.When.Complexity != "" && rule.When.Complexity != class.Complexity {
			continue
		}
		if rule.When.Domain != "" && rule.When.Domain != class.Domain {
			continue
		}
		if rule.When.ToolNeed != "" && rule.When.ToolNeed != class.ToolNeed {
			continue
		}
		return Route{
			Strategy:     rule.Strategy,
			ThinkingMode: defaultStr(rule.Thinking, "auto"),
			Models:       rule.Models,
			MaxCostUSD:   rule.MaxCostUSD,
		}
	}
	return Route{
		Strategy:     "solo",
		ThinkingMode: "auto",
		Models:       config.Models{Primary: cfg.Models.Default},
	}
}

func applyOverrides(route *Route, ov Overrides) {
	if ov.Strategy != "" {
		route.Strategy = ov.Strategy
	}
	if ov.Thinking != "" {
		route.ThinkingMode = ov.Thinking
	}
}

// preflight estimates worst-case spend for expensive strategies and
// downgrades to solo when the ceiling can't cover it (spec 03 §5): better a
// cheaper answer than a refusal.
func (r *Receptionist) preflight(route Route, st *pipeline.State, cfg *config.Config, emit events.Emitter) Route {
	remaining := st.Budget.RemainingUSD()
	if remaining < 0 { // unlimited
		return route
	}

	var estimate float64
	switch route.Strategy {
	case "council":
		members := r.resolveAll(councilMembers(route, cfg))
		synth, _ := r.resolve(route.Models.Primary, cfg)
		estimate = budget.EstimateCouncil(members, cfg.Council.Rounds, synth)
	case "twobrain":
		div, _ := r.resolve(route.Models.Divergent, cfg)
		conv, _ := r.resolve(route.Models.Convergent, cfg)
		ref, _ := r.resolve(route.Models.Primary, cfg)
		estimate = budget.EstimateTwoBrain(div, conv, ref, cfg.TwoBrain.Rounds)
	default:
		return route
	}

	if estimate <= remaining {
		return route
	}
	emit(events.Event{
		Kind: events.KindBudgetWarn, Actor: "receptionist",
		Payload: map[string]any{
			"spent_usd": st.Budget.SpentUSD,
			"limit_usd": st.Budget.LimitUSD,
			"pct":       safePct(st.Budget),
			"reason": fmt.Sprintf("preflight: %s estimate $%.2f exceeds remaining $%.2f — downgrading to solo",
				route.Strategy, estimate, remaining),
		},
	})
	route.Strategy = "solo"
	if route.Models.Primary == "" {
		route.Models.Primary = cfg.Models.Default
	}
	return route
}

// assemble builds the ordered []Stage for the route (spec 03 §4):
// Thinking(mode) → solver stage(s) → Output.
func (r *Receptionist) assemble(route Route, cfg *config.Config, ov Overrides) ([]pipeline.Stage, []string, map[string]any, error) {
	gate, err := r.resolve(cfg.Models.Receptionist, cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("receptionist model: %w", err)
	}
	primary, err := r.resolve(route.Models.Primary, cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("primary model: %w", err)
	}

	stages := []pipeline.Stage{&thinking.Stage{
		Mode:  route.ThinkingMode,
		Gate:  gate,
		Tools: r.Tools,
		Opts: thinking.Options{
			FastEffort:          cfg.Thinking.FastEffort,
			SlowEffort:          cfg.Thinking.SlowEffort,
			ConfidenceThreshold: cfg.Thinking.ConfidenceThreshold,
			MaxToolCalls:        cfg.Thinking.MaxToolCalls,
		},
	}}
	models := map[string]any{"primary": primary.Name()}

	switch route.Strategy {
	case "twobrain":
		div, err := r.resolve(defaultStr(route.Models.Divergent, route.Models.Primary), cfg)
		if err != nil {
			return nil, nil, nil, err
		}
		conv, err := r.resolve(defaultStr(route.Models.Convergent, route.Models.Primary), cfg)
		if err != nil {
			return nil, nil, nil, err
		}
		stages = append(stages, &twobrain.Stage{
			Divergent: div, Convergent: conv, Referee: primary,
			Rounds: cfg.TwoBrain.Rounds, Tools: r.Tools,
		})
		models["divergent"], models["convergent"] = div.Name(), conv.Name()

	case "council":
		members := r.resolveAll(councilMembers(route, cfg))
		if len(members) < 2 {
			return nil, nil, nil, fmt.Errorf("council route needs >=2 resolvable members, got %d", len(members))
		}
		rounds := cfg.Council.Rounds
		if ov.CouncilRounds > 0 {
			rounds = ov.CouncilRounds
		}
		method := cfg.Council.Consensus.Method
		if ov.ConsensusMethod != "" {
			method = ov.ConsensusMethod
		}
		stages = append(stages,
			&council.Stage{
				Members: members, Rounds: rounds,
				Consensus:  council.ConsensusConfig{Method: method, Threshold: cfg.Council.Consensus.Threshold},
				OnDeadlock: cfg.Council.OnDeadlock,
				Referee:    primary, Embedder: r.Embedder, Tools: r.Tools,
			},
			&council.Synthesis{Model: primary},
		)
		names := make([]string, len(members))
		for i, m := range members {
			names[i] = m.Name()
		}
		models["council"] = names

	default: // solo
		stages = append(stages, &thinking.Solo{
			Model: primary, Tools: r.Tools, MaxToolCalls: cfg.Thinking.MaxToolCalls,
		})
	}

	stages = append(stages, pipeline.Output{})
	ids := make([]string, len(stages))
	for i, s := range stages {
		ids[i] = s.ID()
	}
	return stages, ids, models, nil
}

// resolve maps a model name (falling back to the config default) to a Provider.
func (r *Receptionist) resolve(name string, cfg *config.Config) (provider.Provider, error) {
	if name == "" {
		name = cfg.Models.Default
	}
	if name == "" {
		return nil, fmt.Errorf("no model name and no models.default configured")
	}
	return r.Registry.Get(name)
}

func (r *Receptionist) resolveAll(names []string) []provider.Provider {
	var out []provider.Provider
	for _, n := range names {
		if p, err := r.Registry.Get(n); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// councilMembers falls back to the config-level default member list when the
// route doesn't declare one (e.g. an override escalated to council).
func councilMembers(route Route, cfg *config.Config) []string {
	if len(route.Models.Council) > 0 {
		return route.Models.Council
	}
	return cfg.Council.Members
}

func defaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func safePct(b pipeline.BudgetState) float64 {
	if b.LimitUSD <= 0 {
		return 0
	}
	return b.SpentUSD / b.LimitUSD
}
