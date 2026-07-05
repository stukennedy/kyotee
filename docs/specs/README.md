# Harness Specs

Numbered specs for the Kyotee multi-model harness. Provided in this set:

- `01-engine-core.md` — Provider/Stage/Event contracts, executor, state store
- `03-receptionist.md` — classifier, routing rules, budget, handoff envelope
- `04-thinking-modes.md` — fast/slow gate, tool-need pre-pass
- `06-council.md` — multi-model consensus, synthesis
- `08-tui.md` — Tooey front-end

Referenced but not yet provided (implementations are inferred from
cross-references and marked as such in package docs):

- **02** — vendor adapters + HTTP/SSE surface → `internal/provider`
  (anthropic.go, openai.go), `internal/server`
- **05** — two-brain divergent/convergent strategy → `internal/twobrain`
- **07** — config schema & per-task overrides → `internal/config`,
  `receptionist.Overrides`

When the missing specs land, reconcile those packages against them.
