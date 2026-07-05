# 08 — TUI (Tooey Front-End)

A Tooey terminal app that drives and observes the engine. It is a **pure SSE consumer + HTTP action poster** — it holds no orchestration logic. This is the right home for watching council debates, routing decisions, and cost in real time, and for on-the-fly config/override control. Depends on `02` (HTTP/SSE surface), `01` (event schema).

Built with Tooey (`github.com/stukennedy/tooey`), Go 1.24+, Elm architecture (Init/Update/View), its built-in SSE client and HTTP action posting.

---

## 1. Why Tooey Fits

Confirmed from the Tooey feature set: Elm architecture, 30fps frame diffing, declarative nodes (Box/Row/Column/List/Pane/Spacer), async commands returning messages, focus management with Tab cycling, and **built-in server-driven UI via SSE + HTTP action posting**. That last point means the TUI maps directly onto the engine's `/v1/tasks/{id}/events` stream and `POST` endpoints with no custom transport.

---

## 2. Architecture

Elm-style model holding observed engine state, updated purely by messages:

```go
type Model struct {
    TaskID     string
    Input      string            // the prompt being composed
    Class      *Classification   // from task.classified
    Pipeline   []string          // stage IDs from task.routed
    Stage      string            // current stage (stage.start)
    Thinking   ThinkingView      // mode, tool-check verdict, tool calls
    Brains     []BrainTurn       // two-brain turns
    Council    map[string]MemberView // model → evolving position/vote
    Consensus  *ConsensusView
    Cost       CostView          // spent/limit/pct + per-stage breakdown
    Log        []events.Event    // raw event log (scrollable)
    Config     ConfigView        // current effective config (from GET /v1/config)
    Focus      string
}
```

Messages: `KeyMsg` (input), `SSEMsg{Event}` (one engine event → drives Update), `HTTPResultMsg` (task created, config saved), `TickMsg` (heartbeat/animation). Update is a pure switch over these; async commands perform the HTTP POSTs and open the SSE stream, delivering results back as messages (standard Tooey pattern).

**Event → view mapping:** each engine event `Kind` (spec `01` §Event catalog) updates exactly one region of `Model`. E.g. `council.rebuttal` updates `Council[model].Position`; `budget.warn` flips the cost meter to a warning colour; `task.final` populates the answer pane.

---

## 3. Layout

A multi-pane full-screen layout (Tooey `Column`/`Row`/`Pane`/`Box`):

```
┌ Harness ─────────────────────────────────── cost: $0.42 / $3.00 [██████░░] ┐
│ ┌ Prompt ───────────────────────────┐ ┌ Routing ───────────────────────┐ │
│ │ > <input line>                     │ │ class: reasoning / hard         │ │
│ │                                    │ │ strategy: council   mode: slow  │ │
│ └────────────────────────────────────┘ │ pipeline: thinking→council→…    │ │
│ ┌ Debate / Work ─────────────────────────────────────────────────────────┐│
│ │  (strategy-dependent center pane — see §4)                              ││
│ │                                                                          ││
│ └──────────────────────────────────────────────────────────────────────┘ │
│ ┌ Thinking ──────────────────┐ ┌ Event Log ────────────────────────────┐ │
│ │ mode: slow                 │ │ 12:04:01 task.classified …            │ │
│ │ tool-check: web_search ✓   │ │ 12:04:02 council.opening gpt-5 …      │ │
│ │ tools: web_search (1 call) │ │ 12:04:03 council.rebuttal …           │ │
│ └────────────────────────────┘ └───────────────────────────────────────┘ │
├ Tab: cycle focus · c: config · o: override&escalate · r: resume · q: quit ─┤
└────────────────────────────────────────────────────────────────────────────┘
```

The top-right cost meter is always visible and colour-shifts at the warn thresholds (50/80/95%).

---

## 4. Strategy-Dependent Center Pane

- **Solo:** streamed answer text (from `stage`/text deltas if exposed, else the final `task.final`).
- **Two-Brain:** two columns — divergent (left) vs convergent (right) — filled from `brain.turn` events per round, with the referee's synthesis rendered full-width below. Colour-code the two personas.
- **Council:** one column/pane per member (`council.opening`/`rebuttal`), each showing that model's evolving position and latest `vote` (choice + confidence). A consensus indicator (from `council.consensus`) shows converged/not and rounds used. Synthesis renders below once available.

Use Tooey `List` with `WithScrollToBottom` for the growing debate feeds; `Pane` per council member; `Box` with borders to delineate regions.

---

## 5. On-the-Fly Control (the flexibility requirement)

Keybindings that POST to the engine:

- **Enter** on the prompt → `POST /v1/tasks {text}` → open SSE on returned `task_id`.
- **`o` (override & escalate)** → opens a small overlay to set per-task overrides (strategy, thinking mode, budget, council rounds/consensus) and submit as the `overrides` object on a **new** task, or (if mid-run and not yet past routing) as guidance. This is the "escalate this one to council" affordance. Uses `POST /v1/tasks` with overrides (spec `07` §4).
- **`c` (config)** → opens the current effective config (`GET /v1/config`), allows editing key fields, and `PUT /v1/config` to hot-reload globally. Invalid edits surface the engine's 400 message inline; old config stays live.
- **`r` (resume)** → `POST /v1/tasks/{id}/resume` for a selected prior task (from `GET /v1/tasks` listing).

All of these are async Tooey commands returning `HTTPResultMsg`; the resulting behaviour is observed back through the SSE stream — the TUI never mutates orchestration state locally.

---

## 6. Robustness

- **Late connect / reconnect:** the engine replays events from Seq 0 (spec `02` §3), so opening the stream mid- or post-run reconstructs full state. On SSE drop, reconnect and de-dup by `Seq`.
- **Heartbeat:** consume the engine's `: ping` comments to detect a dead connection; show a disconnected indicator and auto-retry.
- **Backpressure:** the Update loop must drain `SSEMsg` promptly; buffer in the async command, not in the render path, to keep 30fps.

---

## 7. Acceptance Criteria

- [ ] Submitting a prompt creates a task via HTTP and streams its events, populating routing, thinking, center-pane, and cost regions live.
- [ ] A council task renders one pane per member with evolving positions and votes, plus a consensus indicator and final synthesis.
- [ ] A two-brain task renders divergent/convergent columns and the referee synthesis.
- [ ] The cost meter updates from `stage.end`/`budget.warn` and colour-shifts at 50/80/95%.
- [ ] `o` submits a task with overrides (e.g. force council) and the observed run reflects them.
- [ ] `c` edits and hot-reloads config; an invalid edit shows the engine's error and does not change running behaviour.
- [ ] Connecting to an already-finished task replays and renders the complete run from Seq 0.
- [ ] SSE disconnect triggers reconnect with `Seq` de-duplication; no duplicate log lines.
