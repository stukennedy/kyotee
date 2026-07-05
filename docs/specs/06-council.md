# 06 — Council: Multi-Model Consensus

Implements the council of different-provider models that debate a hard decision until they reach consensus, with a synthesis step to collapse the debate into one answer. The most expensive strategy; gated hard by budget. Depends on `01`, `02`, `03`, `04`.

Lives in `internal/council`.

---

## 1. Concept

For high-stakes or genuinely contested problems, convene **≥3 models from distinct vendors**. Cross-vendor is the point: same-family models tend to agree, which produces false consensus. Vendor diversity yields genuine perspective spread.

Each member: (1) states an opening position, (2) reads the others' positions, (3) rebuts/revises across rounds, until positions converge or a round cap is hit. A **Synthesis** stage (using `Models.Primary`) then produces the single final answer from the debate.

---

## 2. Protocol

```
opening:  each member answers the task independently (parallel, no cross-talk)
round r:  each member sees all other members' latest positions (distilled) and
          may revise or rebut, stating whether they now agree
check:    run consensus detection (below) after each round
stop when: consensus reached  OR  rounds == max  OR  budget preflight fails
finalize: Synthesis stage collapses the debate → State.Draft
```

```go
type Council struct {
    Members   []string // model names; SHOULD span >=2 vendors (warn if not)
    Rounds    int      // default 3, hard-capped
    Protocol  string   // "debate" (v1); room for "vote_only" later
    Consensus ConsensusConfig
    OnDeadlock string  // "referee" | "majority_vote" | "synthesis_notes_dissent"
    Synth     string   // Models.Primary
}
```

- **Opening turns run in parallel** (goroutines) — independent, no shared context — for latency and to avoid anchoring.
- **Rebuttal rounds** give each member the distilled `Envelope` + the other members' latest positions (distilled to key points, not full text, to bound cost).
- Members honor `thinking.effort` and any flagged tools from spec `04`.
- **Default rounds: 3, hard-capped.** Councils can debate forever; the cap and the budget preflight are the safety rails.

---

## 3. Consensus Detection (the hard part)

Provide three configurable methods; operator picks per config:

```go
type ConsensusConfig struct {
    Method    string  // "vote" | "similarity" | "judge"
    Threshold float64 // meaning depends on method
}
```

1. **`vote` (cheapest, default).** Each member emits a structured vote each round: `{ "choice": "<option/answer id or short label>", "confidence": 0..1, "agree_with_group": bool }`. Consensus = ≥ `Threshold` fraction converge on the same `choice` (e.g. 0.66). Requires the task to be framed so choices are comparable; for open-ended answers, members first agree on a small option set in round 1.
2. **`similarity`.** Embed each member's current conclusion; consensus when pairwise cosine similarity all exceed `Threshold` (e.g. 0.85). Needs an embedding provider (config); vendor-agnostic via a small `Embedder` interface.
3. **`judge`.** A referee model reads all current positions and returns strict-JSON `{ "converged": bool, "summary": "...", "dissent": ["..."] }`. Most flexible, one extra call per check.

Emit `council.vote` (per member, when method=vote) and `council.consensus` (`reached`, `method`, `rounds_used`) after each check.

```go
type Embedder interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
}
```

---

## 4. Deadlock Handling

If `rounds == max` and no consensus:

- `referee` — a designated referee model decides among the standing positions.
- `majority_vote` — take the plurality `choice`; ties broken by aggregate confidence.
- `synthesis_notes_dissent` — Synthesis produces the best answer **and explicitly records the dissent** in the output (often the most honest result for genuinely contested questions).

Config picks the default (`OnDeadlock`). Emit `council.consensus` with `reached=false` and the resolution method used.

---

## 5. Synthesis Stage

Always follows Council (assembled by the Receptionist, spec `03` §4). Uses `Models.Primary`. Reads the full `State.Transcript` (all openings + rebuttals + votes) and produces the single `State.Draft`. If deadlock resolution was `synthesis_notes_dissent`, the synthesis prompt must surface the disagreement rather than paper over it.

```go
type Synthesis struct{ Model string }
// Run implements pipeline.Stage. Collapses council transcript → State.Draft.
```

---

## 6. Cost Control

Council is the most expensive path: `members × (1 opening + rounds rebuttals) + checks + synthesis`. Therefore:

- The Receptionist's **preflight** (spec `03` §5) estimates worst-case (`members × (1+rounds) × avg_output_tokens × cost` + embedding/judge overhead) and **downgrades council→solo** if it won't fit the ceiling, emitting `budget.warn`.
- The Executor halts between rounds if the ceiling is hit mid-debate, and Synthesis runs on whatever positions exist (best-effort).
- Distilled envelopes (not full transcripts) in rebuttal rounds keep per-round token growth sub-linear.

---

## 7. Events → TUI

`council.opening`, `council.rebuttal`, `council.vote`, `council.consensus` drive the TUI's live debate view (spec `08`): one pane/column per member showing their evolving position, a consensus indicator, and per-member cost. The Synthesis output renders below.

---

## 8. Acceptance Criteria

- [ ] A council of ≥3 fake providers with scripted divergent-then-converging positions reaches consensus via the `vote` method and produces a synthesised `State.Draft`.
- [ ] `similarity` method reaches consensus when scripted conclusions are near-identical and does not when they diverge (uses a fake `Embedder`).
- [ ] `judge` method returns converged/not correctly from a fake referee.
- [ ] Vendor-diversity warning fires when all members share a vendor.
- [ ] Deadlock (positions never converge within `Rounds`) triggers the configured `OnDeadlock` path; `synthesis_notes_dissent` surfaces disagreement in the output.
- [ ] Opening turns execute in parallel (observable: overlapping timestamps in events).
- [ ] Preflight downgrades council→solo when budget can't cover the estimate; mid-debate budget halt still yields a best-effort synthesis.
- [ ] Full event sequence (`opening`→`rebuttal`→`vote`→`consensus`) is emitted in order per round.
