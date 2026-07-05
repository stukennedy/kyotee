# 10 — Build Sequence & Milestones

Dependency-ordered plan for the coding agent. Each milestone is shippable and testable in isolation using the `fake` provider, so you never need live API keys to make progress or run CI.

---

## Dependency Graph

```
01 ENGINE-CORE ──┬──▶ 02 PROVIDERS ──┬──▶ 03 RECEPTIONIST ──▶ 04 THINKING ──┬──▶ 05 TWO-BRAIN
                 │                    │                                      └──▶ 06 COUNCIL
                 │                    └──▶ 08 TUI (needs 02 HTTP/SSE + 01 events)
                 │
                 └──▶ 07 CONFIG (schema referenced everywhere; stub early, harden with 03/05/06)
                                        └──▶ 09 SKILL (needs 02 + 07)
```

`05` and `06` are parallelisable once `04` lands. `08` can start as soon as `02` exposes the SSE surface (mock events first). `07` should be **stubbed early** (minimal struct load) and hardened as the mechanisms it configures come online.

---

## Milestone 1 — Skeleton that runs a no-op task end to end
**Specs:** `01`, `02` (fake provider + SSE only), `07` (minimal loader)
- `Provider`, `Stage`/`State`, `Event`/`Bus`, `state.Store` types (spec `01`).
- Pipeline Executor with checkpointing + budget halt.
- `fake` provider; `harnessd` with `POST /v1/tasks`, `GET /events` (replay+tail), `GET /tasks/{id}`.
- Minimal config load (providers + defaults only).
- **Demo:** POST a task that runs a single no-op stage; watch `stage.start`→`stage.end`→`task.final` over SSE; state file on disk.
- **Exit:** spec `01` + spec `02` (fake/SSE portions) acceptance criteria pass.

## Milestone 2 — Real solo answers with fast/slow + tools
**Specs:** `02` (Anthropic + one more adapter), `03` (receptionist), `04` (thinking + web_search)
- Anthropic + OpenAI (or Google) adapters passing the contract test.
- Receptionist: classify → route → assemble → budget, with config routing rules.
- Thinking stage: auto gate, tool-need pre-pass, `web_search` tool, Solo stage with tool-use loop.
- **Demo (the headline):** "who is the prime minister?" → auto gate flags slow → pre-pass says use `web_search` → Solo searches → grounded answer. And "what is a hash map?" → fast path, no tool cost.
- **Exit:** spec `03` + spec `04` acceptance criteria pass; the prime-minister and hash-map cases verified via events.

## Milestone 3 — The two solving strategies
**Specs:** `05` (two-brain), `06` (council) — parallelisable
- Two-Brain: divergent/convergent temperature split, referee synthesis, `brain.turn` events.
- Council: parallel openings, rebuttal rounds, all three consensus methods, deadlock handling, Synthesis stage, `council.*` events.
- Budget preflight downgrades wired for both.
- **Demo:** a hard-code task runs two-brain; a hard-reasoning task convenes a 3-vendor council reaching consensus (use fakes for deterministic tests, real providers for a live demo).
- **Exit:** spec `05` + spec `06` acceptance criteria pass.

## Milestone 4 — Config hardening + hot-reload
**Specs:** `07` (full validation + overrides + hot-reload), `02` (`PUT /config`, reload, `/providers`)
- Full schema validation with the rule table; `harness-cli config validate`.
- Per-task `overrides` merge + validation.
- Atomic hot-reload behind `atomic.Pointer`.
- **Exit:** spec `07` acceptance criteria pass.

## Milestone 5 — TUI
**Specs:** `08`
- Tooey app: SSE consumer, multi-pane layout, strategy-dependent center pane, cost meter.
- On-the-fly control: submit, `o` override/escalate, `c` config hot-reload, `r` resume.
- Reconnect + `Seq` de-dup.
- **Exit:** spec `08` acceptance criteria pass.

## Milestone 6 — Skill adapter
**Specs:** `09`
- `harness-cli ask/resume/status/providers` against the running engine.
- `SKILL.md` with trigger-focused description + nesting limitation.
- **Exit:** spec `09` acceptance criteria pass.

---

## Cross-Cutting Engineering Standards

- **Everything testable with `fake`.** No milestone's tests may require network/API keys. Live providers are for manual demos only.
- **Events are the source of truth for behaviour.** Prefer asserting on emitted events over internal state in integration tests — it also validates the TUI contract.
- **Budget is not optional.** Preflight estimation and mid-run halt must be in place the moment two-brain/council land (Milestone 3), not bolted on later.
- **No provider SDK above the interface.** Enforce with an architecture test (grep/lint) that fails if `internal/{pipeline,receptionist,thinking,twobrain,council}` import a vendor SDK.
- **Model names are config, never constants.** An architecture test should flag hardcoded model-identifier string literals outside adapters/tests.
- **Verify live model IDs and costs at implementation time.** The example config's model names and prices are placeholders to be confirmed against vendor docs.

---

## Suggested First PR

Milestone 1, split as: (a) `01` types + Executor + tests; (b) `events` bus + `state` store + tests; (c) `fake` provider + `harnessd` SSE skeleton + one end-to-end test. That gives a running, observable spine to hang everything else on.
