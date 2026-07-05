# 03 — Receptionist: Router, Classifier & Budget

The Receptionist is the always-on front door. It classifies each task, assembles the stage pipeline, selects models, and enforces the cost budget. It is the only mechanism that runs on every task. Depends on `01`, `02`.

Lives in `internal/receptionist` and `internal/budget`.

---

## 1. Responsibilities

1. **Classify** the incoming task along fixed dimensions using a cheap, fast model.
2. **Route** — map the classification to a solving strategy, thinking mode, and model set via config rules.
3. **Assemble** the concrete `[]Stage` pipeline the Executor will run.
4. **Budget** — attach a spend ceiling and gate expensive strategies; prefer cheap paths for trivial work.
5. **Handoff** — carry a context envelope so any downstream model can pick up work.

The Receptionist **classifies; it does not solve.** Its model is deliberately cheap (Haiku-class).

---

## 2. Classification

A single call to the receptionist model returns a strict-JSON verdict (use the structured-output discipline: system prompt demands JSON only, parse defensively).

```go
type Classification struct {
    Complexity string   `json:"complexity"` // "trivial" | "standard" | "hard"
    Domain     string   `json:"domain"`     // "code" | "research" | "reasoning" | "creative" | "chat"
    ToolNeed   string   `json:"tool_need"`  // "none" | "likely" | "required"
    Confidence float64  `json:"confidence"` // 0..1, classifier's self-estimate
    Rationale  string   `json:"rationale"`  // one line, for the event log
}
```

Classifier prompt requirements:
- Instruct it to flag `tool_need: required` for any request hinging on **current/present-state facts** (dates, prices, who-holds-a-role, "latest", "current"), file/repo access, or math it cannot do reliably in-head. This is the first line of defence for the fast/slow problem (spec `04`).
- Instruct it to rate `complexity: hard` only for genuinely multi-constraint / high-stakes / ambiguous problems — the gate that unlocks council/two-brain and their cost.
- Output JSON only, no prose, no fences.

Emit `task.classified` with the full struct.

**Fallback:** if classification fails to parse or the model errors, default to `{standard, chat, likely, 0.0}` and log a `budget.warn`-style note. Never block the task on classifier failure.

---

## 3. Routing Rules

Rules are **config-declared** (spec `07`), matched top-to-bottom; first match wins. A rule maps a classification predicate to a **route**:

```go
type Route struct {
    Strategy     string   // "solo" | "twobrain" | "council"
    ThinkingMode string   // "fast" | "slow" | "auto"
    Models       Models   // which model(s) the strategy uses
    MaxCostUSD   float64  // per-task ceiling (0 = inherit global default)
}

type Models struct {
    Primary  string   // solo / referee / synthesis model
    Divergent string  // two-brain right brain (optional)
    Convergent string // two-brain left brain (optional)
    Council  []string // council members (>=2 distinct vendors recommended)
}
```

Example rule semantics (declared in YAML, see `07`):

| Predicate | Route |
|---|---|
| `complexity == trivial` | solo, fast, cheap model, low budget |
| `domain == code && complexity == standard` | solo, auto, mid model |
| `domain == code && complexity == hard` | two-brain, slow, strong models + referee |
| `domain == reasoning && complexity == hard` | council, slow, 3 cross-vendor models |
| `tool_need == required` | force ThinkingMode=slow regardless of other fields |
| *default* | solo, auto, mid model |

`ThinkingMode: auto` defers the fast/slow decision to the Thinking stage's runtime gate (spec `04`), rather than fixing it at routing time. Prefer `auto` for most routes; use explicit `slow` only where you always want the tool-check pre-pass.

Emit `task.routed` with the chosen strategy, pipeline stage IDs, and model names.

---

## 4. Pipeline Assembly

Given a `Route`, build the ordered `[]Stage`:

```
base:      [ Thinking(mode) ]                 // always present; wraps the solver
solo:      [ Thinking(mode) → Solo(primary) ]
twobrain:  [ Thinking(mode) → TwoBrain(div,conv,referee) ]
council:   [ Thinking(mode) → Council(members) → Synthesis(primary) ]
terminal:  → Output                           // promotes Draft→Final, emits task.final
```

Notes:
- **Thinking wraps, it doesn't precede-and-forget.** Implementation detail resolved in spec `04`: the Thinking stage runs the tool-check pre-pass and sets effort, then the *following* solver stage reads mode/effort from `State.Meta`. Keep them as adjacent stages sharing state for simplicity.
- **Council always gets a Synthesis stage** (spec `06`) to collapse the debate into one answer, using `Models.Primary` as synthesiser.
- **Solo needs no synthesis** — its output is the draft.
- Resume: when rebuilding for `/resume`, skip stages whose ID is in `State.Checkpoints`.

---

## 5. Budget & Telemetry

Lives in `internal/budget`. This is **load-bearing** — without it, composing slow+council+two-brain is ruinously expensive.

```go
type BudgetState struct {
    LimitUSD  float64
    SpentUSD  float64
    Tokens    int
}

// Account is called by every stage after each provider call.
func (b *BudgetState) Account(u provider.Usage)   // adds cost + tokens

// PreflightCouncil / PreflightTwoBrain estimate worst-case spend and refuse
// to start the strategy if it would blow the ceiling, downgrading to solo.
func (r *Receptionist) preflight(route Route, b BudgetState) Route
```

Rules:
- **Global default ceiling** from config; per-route override allowed.
- **Preflight downgrade:** before launching council/two-brain, estimate `rounds × members × avg_output_tokens × cost`. If the estimate exceeds remaining budget, **downgrade the strategy** (council→solo, two-brain→solo) and emit `budget.warn` with the reason. Better a cheaper answer than a refusal.
- **Mid-run enforcement** is the Executor's job (spec `01` §Executor): halt between stages at the ceiling, promote `Draft`→`Final`.
- **Warn thresholds:** emit `budget.warn` at 50/80/95% of ceiling.
- **Telemetry:** every `stage.end` carries `cost_delta_usd` and cumulative `spent_usd`. Persist a per-task cost summary in the final state (`task.final` payload: `total_cost_usd`, `total_tokens`, and a per-stage breakdown). The TUI's cost meter (spec `08`) reads these events.

---

## 6. Handoff Envelope

When one model hands to another (council rounds, two-brain persona switch, synthesis), pass a compact envelope built from `State`, not the raw full transcript, to control token cost:

```go
type Envelope struct {
    Task        string   // original request
    Class       Classification
    PriorPoints []string // distilled key points/claims so far (not full text)
    OpenIssues  []string // unresolved questions for the next actor
    CostSoFar   float64
}
```

Each strategy decides how much of the transcript to distil into `PriorPoints`. Full transcripts are always available in `State.Transcript` for the final synthesis, but intermediate handoffs should prefer the distilled envelope.

---

## 7. Acceptance Criteria

- [ ] Classifier returns a valid `Classification` for a battery of sample tasks (trivial chat, standard code, hard reasoning, current-fact question) and flags `tool_need: required` on present-state-fact prompts.
- [ ] Classifier parse-failure falls back to the safe default without blocking.
- [ ] Config routing rules are matched top-to-bottom, first-match-wins, producing the expected `Route` for each sample (table test).
- [ ] Pipeline assembly yields the correct `[]Stage` per strategy, and `Output` is always terminal.
- [ ] Preflight downgrades council→solo when the budget can't cover the estimate, emitting `budget.warn`.
- [ ] `budget.warn` fires at 50/80/95%; Executor halts at 100% (integration with spec `01`).
- [ ] `task.final` payload contains total cost, total tokens, and a per-stage cost breakdown.
- [ ] Resume rebuilds a pipeline that skips checkpointed stages.
