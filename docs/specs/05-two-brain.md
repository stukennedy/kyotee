# 05 — Two-Brain: Divergent / Convergent Debate

Implements the creative-vs-analytical internal debate: a high-temperature divergent persona proposes, a low-temperature convergent persona critiques, they iterate, and a referee synthesises. A solving strategy selected by the Receptionist for problems needing creative tension. Single provider is fine. Depends on `01`, `02`, `04`.

Lives in `internal/twobrain`.

---

## 1. Concept

Two personas over (optionally) the same base model, differentiated primarily by **sampling temperature** — the lever that actually produces divergent vs convergent behaviour, more so than prompt wording:

- **Right brain (divergent):** high temperature (~1.0). Generates options, unconventional angles, breadth. System prompt: explore widely, propose multiple distinct approaches, don't self-censor.
- **Left brain (convergent):** low temperature (~0.3). Critiques, prunes, stress-tests feasibility, picks. System prompt: be rigorous, find flaws, converge on the strongest option with reasons.
- **Referee:** synthesises the exchange into one answer. Uses `Models.Primary`.

---

## 2. Protocol

```
round 1: RIGHT proposes N approaches
         LEFT critiques each, ranks, may request refinement
round 2: RIGHT refines the surviving/critiqued options
         LEFT converges → recommendation + rationale
REFEREE: reads the exchange, produces the final Draft
```

- **Default rounds: 2.** Config-tunable (`twobrain.rounds`), but hard-cap low — returns diminish fast and tokens multiply. 2 is the recommended default; allow 1–3.
- Each turn is a provider call. Divergent uses the div temperature; convergent uses the conv temperature; both honor `thinking.effort` from spec `04`.
- Handoffs use the distilled `Envelope` (spec `03` §6) plus the prior turn's text — not the entire growing transcript — to bound cost. The referee, however, sees the full `State.Transcript`.

```go
type TwoBrain struct {
    Divergent   string   // model name
    Convergent  string   // model name (often same base as Divergent)
    Referee     string   // model name (Models.Primary)
    Rounds      int
    DivTemp     float64  // default 1.0
    ConvTemp    float64  // default 0.3
}
// Run implements pipeline.Stage. Writes referee output to State.Draft.
```

---

## 3. Events

Emit `brain.turn` for every persona turn with `role` (`divergent`|`convergent`|`referee`), `round`, and the text. The TUI (spec `08`) renders these as an alternating two-column debate with the referee's synthesis below. Each turn also appends a `Turn` to `State.Transcript` with per-turn `Usage` for cost tracking.

---

## 4. Design Notes

- **Temperature is the mechanism.** If a model doesn't expose temperature (rare), fall back to differentiating via strong system-prompt framing, but log that the split is weaker.
- **Same-vendor is acceptable here** — Two-Brain is about creative tension within one mind, not perspective diversity across vendors (that's the Council's job, spec `06`).
- **Budget:** Two-Brain cost ≈ `rounds × 2 turns × avg_tokens + referee`. The Receptionist's preflight (spec `03` §5) must estimate this and downgrade to solo if it won't fit.
- Keep persona system prompts in editable files (`config/prompts/divergent.md`, `convergent.md`, `referee.md`) referenced from config, so operators tune them without a rebuild.

---

## 5. Acceptance Criteria

- [ ] A hard-code task routed to two-brain runs the full protocol: divergent proposals → convergent critique → (round 2) → referee synthesis, producing a non-empty `State.Draft`.
- [ ] Divergent turns use the configured high temperature and convergent turns the low temperature (asserted via the fake provider capturing `Request.Temperature`).
- [ ] `brain.turn` events are emitted for every persona turn with correct `role`/`round`; the referee sees the full transcript.
- [ ] `rounds` is honored and hard-capped (config value >3 is clamped or rejected).
- [ ] Per-turn `Usage` is accumulated into the budget; preflight downgrade works when the estimate exceeds remaining budget.
- [ ] Persona prompts load from external files referenced in config.
