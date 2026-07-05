# Kyotee

A multi-model AI harness. Every task walks in the front door, gets classified
by a cheap model, and is routed to the cheapest strategy that can actually
solve it — one model, a divergent/convergent two-brain pair, or a council of
models from different vendors that debate to consensus. Thinking speed is a
structural decision, not a hope: present-state facts trigger a tool pre-pass
so the solver searches instead of answering from stale weights. Everything is
budgeted, checkpointed, resumable, and observable live in a terminal UI.

```
            ┌──────────────┐   task.classified   ┌─────────────────────────┐
  prompt ──►│ Receptionist │────────────────────►│ Route (config rules)    │
            │ (cheap model)│                     │ first match wins        │
            └──────────────┘                     └───────────┬─────────────┘
                                                             │ preflight: too pricey? downgrade
                     ┌───────────────────────────────────────┼──────────────┐
                     ▼                                       ▼              ▼
              ┌────────────┐                         ┌────────────┐  ┌────────────┐
              │  Thinking  │  fast/slow gate +       │  Thinking  │  │  Thinking  │
              │            │  tool-need pre-pass     │            │  │            │
              ├────────────┤                         ├────────────┤  ├────────────┤
              │    Solo    │                         │  Two-Brain │  │  Council   │
              │            │                         │ div ⇄ conv │  │ ≥3 vendors │
              │            │                         │  referee   │  │  debate    │
              └─────┬──────┘                         └─────┬──────┘  ├────────────┤
                    │                                      │         │ Synthesis  │
                    ▼                                      ▼         └─────┬──────┘
                 Output ◄──────────────────────────────────┴───────────────┘
              (Draft→Final, task.final, cost breakdown)
```

## Quick start

```bash
go build -o kyotee .
./kyotee init                 # write ~/.kyotee/config.yaml
export ANTHROPIC_API_KEY=...  # plus OPENAI_API_KEY / GEMINI_API_KEY for councils
./kyotee                      # engine + TUI
```

One-shot, no TUI:

```bash
./kyotee ask "who is the current UK prime minister?"      # → slow mode, web_search, grounded answer
./kyotee ask --strategy council --budget 5 "monolith or microservices for a 4-person team?"
```

Headless engine + separate TUI:

```bash
./kyotee serve                              # HTTP/SSE on 127.0.0.1:8484
./kyotee tui --url http://127.0.0.1:8484    # attach from another terminal
```

## Why

- **Fast/slow thinking is structural.** LLMs answer present-state questions
  from stale training data because nothing forces them to pause. Kyotee runs
  a cheap gate before the solver: time-sensitive facts, multi-step math, or
  low classifier confidence flip the task to slow mode, and a tool-need
  pre-pass tells the solver *exactly* what it must look up (`web_search`
  ships in v1). "Hope the model searches" becomes "the harness already
  decided a search is required."
- **Councils need vendor diversity.** Same-family models agree with each
  other — false consensus. Council routes want ≥2 vendors (the engine warns
  otherwise), debate across capped rounds, detect consensus by vote,
  embedding similarity, or a judge model, and always end in a synthesis that
  is honest about unresolved dissent.
- **Budget is load-bearing.** Every route carries a USD ceiling. Expensive
  strategies are preflighted and downgraded to solo when they can't fit;
  warns fire at 50/80/95%; the executor halts at 100% and promotes the best
  draft so far. Better a cheaper answer than a refusal.
- **Everything is an event.** Classification, routing, thinking decisions,
  every tool call, every council rebuttal and vote, every dollar — one event
  bus, replayable from sequence 0, streamed over SSE. The TUI holds zero
  orchestration logic.

## TUI

Prompt, routing, and cost meter up top; a strategy-dependent center pane
(streamed answer / divergent-vs-convergent columns / one pane per council
member with live votes and a consensus indicator); thinking decisions and the
raw event log below.

Keys: `Enter` submit · `o` override & escalate (force strategy/thinking/budget
for the next task) · `c` view/edit config with hot reload · `r` resume a
persisted task · `q` quit.

## Config

`~/.kyotee/config.yaml` declares providers (Anthropic and any
OpenAI-compatible endpoint — OpenAI, Gemini, local), model roles, strategy
tuning, and the routing table:

```yaml
routes:
  - when: {complexity: trivial}
    strategy: solo
    thinking: fast
    models: {primary: claude-haiku}
    max_cost_usd: 0.10
  - when: {domain: code, complexity: hard}
    strategy: twobrain
    thinking: slow
    models: {primary: claude-opus, divergent: gpt, convergent: claude-sonnet}
  - when: {domain: reasoning, complexity: hard}
    strategy: council
    thinking: slow
    models: {primary: claude-opus, council: [claude-sonnet, gpt, gemini]}
  - strategy: solo        # default
    thinking: auto
    models: {primary: claude-sonnet}
```

Rules match top-to-bottom, first match wins; `tool_need: required` from the
classifier forces slow mode regardless. `PUT /v1/config` (or `c` in the TUI)
hot-reloads; invalid config is rejected with a 400 and the old config stays
live.

## HTTP API

| Method & path | Purpose |
|---|---|
| `POST /v1/tasks` | submit `{text, overrides?}` → `{task_id}` |
| `GET /v1/tasks` | list persisted tasks |
| `GET /v1/tasks/{id}` | full persisted state (transcript, cost, checkpoints) |
| `GET /v1/tasks/{id}/events` | SSE stream, full replay from seq 0, `id:` = seq |
| `POST /v1/tasks/{id}/resume` | re-run remaining stages from checkpoints |
| `GET /v1/config` / `PUT /v1/config` | effective YAML / validated hot reload |

## Development

```bash
go test ./...        # everything runs on scripted fake providers — no keys
go test -race ./...  # council openings/rebuttals are parallel
```

Specs are in [`docs/specs/`](docs/specs/); architecture notes in
[`CLAUDE.md`](CLAUDE.md).
