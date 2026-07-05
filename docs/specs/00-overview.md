# Harness — System Overview & Architecture

**Codename:** Harness
**Purpose:** A configuration-driven orchestration layer for AI coding and reasoning tasks that dynamically composes four cognitive mechanisms — a routing Receptionist, dynamic Fast/Slow thinking, Two-Brain divergent/convergent debate, and a multi-model Council — over any set of LLM providers.

This document is the map. Read it first, then the numbered specs in order. Each numbered spec is self-contained enough to hand to a coding agent as a discrete work package, but they share the contracts defined in `01-ENGINE-CORE.md`.

---

## 1. Design Goals (in priority order)

1. **Config over code.** Behaviour is declared in a config file (see `07-CONFIG-SCHEMA.md`) and hot-reloadable at runtime. Adding a council member, toggling slow-mode, or changing routing rules must never require a recompile.
2. **Provider-agnostic.** Anthropic, OpenAI, Google, and local models sit behind one `Provider` interface. No mechanism may import a provider SDK directly.
3. **Cost-bounded.** Every task carries a budget. The Receptionist enforces it. Heavy machinery (council, two-brain, slow) is opt-in per-task, never default-on.
4. **Observable.** Every stage emits structured events over an SSE stream. The TUI is a pure consumer of this stream. Nothing happens invisibly.
5. **Resumable.** Task state is a persisted state machine. A crashed or paused run can be resumed from the last checkpoint.

## 2. The Two Deliverables

The system ships as **one engine** with **two front-ends**, built in this order:

| Layer | What | Spec |
|---|---|---|
| **Engine** | Provider-agnostic library + local HTTP/SSE server. All four mechanisms live here. Pure Go, no UI. | `01`–`06`, `07` |
| **TUI** | Tooey (Go) app. Server-driven via SSE. Watches routing, council debate, cost meter; supports on-the-fly config edits. | `08` |
| **Skill adapter** | A `SKILL.md` + thin CLI shim so Claude Code can invoke Harness patterns mid-session. | `09` |

Build the engine to completion first (it is the product). The TUI and Skill are consumers of the same public surface.

## 3. Request Lifecycle

Every task flows through a **stage pipeline**. Stages are optional and composed per-task based on the Receptionist's classification.

```
                        ┌─────────────────────────────────────────┐
   Task ──▶ Receptionist ─▶ classify ─▶ build pipeline ─▶ execute  ─▶ Synthesis ─▶ Output
                        │      │              │                          ▲
                        │      │              │                          │
                        │   budget        selects which                  │
                        │   gate          stages run                     │
                        └──────┼──────────────┼──────────────────────────┘
                               ▼              ▼
                        cheap Haiku    ┌──────────────┐
                        classifier     │ Thinking mode │  fast | slow
                                       ├──────────────┤
                                       │  Two-Brain    │  right ⇄ left ⇄ referee
                                       ├──────────────┤
                                       │  Council      │  N models debate → consensus
                                       └──────────────┘
```

A trivial task's pipeline is just `[Thinking:fast → Output]`. A hard reasoning task might be `[Thinking:slow → Council → Synthesis → Output]`. The pipeline is data — an ordered list of stage instances — assembled by the Receptionist and executed by the engine.

## 4. How the Four Mechanisms Relate

- **Receptionist** is the entry point and the only always-on mechanism. It decides which of the others activate. See `03`.
- **Fast/Slow Thinking** is a *modifier* on any generation step — it governs reasoning effort and, critically, forces a pre-generation tool-need check. It can wrap a solo model, a two-brain persona, or a council member. See `04`.
- **Two-Brain** is a *solving strategy* for problems needing creative tension: a high-temperature divergent persona and a low-temperature convergent persona iterate, then a referee synthesises. Single provider is fine. See `05`.
- **Council** is a *solving strategy* for high-stakes decisions needing genuine perspective diversity: multiple **different-provider** models debate to consensus. Expensive; gated hard. See `06`.

Two-Brain and Council are mutually exclusive per task (you pick a solving strategy). Fast/Slow composes with either.

## 5. Repository Layout (target)

```
harness/
├── cmd/
│   ├── harnessd/          # the engine server (main)
│   └── harness-cli/       # thin CLI, also the Skill shim
├── internal/
│   ├── provider/          # Provider interface + adapters (anthropic, openai, google, local)
│   ├── pipeline/          # Stage contract + executor
│   ├── receptionist/      # routing, classification, budget
│   ├── thinking/          # fast/slow gate + tool-need pre-pass
│   ├── twobrain/          # divergent/convergent/referee
│   ├── council/           # debate protocol + consensus detection
│   ├── budget/            # cost accounting + telemetry
│   ├── state/             # persisted task state machine
│   └── events/            # SSE event bus + schema
├── config/
│   └── harness.example.yaml
├── tui/                   # Tooey app (may be a separate module)
└── skill/
    └── SKILL.md
```

## 6. Cross-Cutting Contracts (defined in `01`)

Three types are referenced by every other spec. They are defined once in `01-ENGINE-CORE.md`:

- `Provider` — the model abstraction.
- `Stage` — the pipeline unit.
- `Event` — the observability record.

Do not redefine these in other specs; import them.

## 7. Non-Goals (v1)

- No distributed execution — single-node, single-process engine.
- No fine-tuning or training. Inference orchestration only.
- No persistent vector store / RAG layer. Tools are pluggable but knowledge retrieval is out of scope for v1.
- No auth/multi-tenant. Local-first, single operator.

## 8. Reading Order for the Coding Agent

1. `01-ENGINE-CORE.md` — the contracts. Everything depends on this.
2. `02-PROVIDERS.md` — implement the abstraction + at least Anthropic and one other.
3. `03-RECEPTIONIST.md` — the router that drives everything.
4. `04-THINKING-MODES.md` — the fast/slow modifier.
5. `05-TWO-BRAIN.md` and `06-COUNCIL.md` — the two solving strategies (parallelisable).
6. `07-CONFIG-SCHEMA.md` — the config contract binding it together.
7. `08-TUI.md` — the Tooey front-end.
8. `09-SKILL-ADAPTER.md` — the Claude Code integration.

Each spec ends with **Acceptance Criteria**. Treat those as the definition of done.
