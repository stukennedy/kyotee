# 02 — Provider Adapters & Engine HTTP/SSE Surface

Implements the `Provider` interface (spec `01` §1) for each vendor, and the engine's HTTP/SSE server that the TUI and Skill consume. Depends on `01`.

---

## 1. Adapter Requirements (all vendors)

Each adapter lives in `internal/provider/<vendor>` and:

1. Translates `provider.Request` → vendor request payload.
2. Streams vendor deltas → `provider.Delta` via `req.Stream` (if set).
3. Aggregates the final `provider.Response` including ordered `Block`s (text + tool_use).
4. Computes `Usage.CostUSD` from returned token counts and the model's `CostPer1M`.
5. Maps normalized `ReasoningEffort` ("minimal"/"low"/"medium"/"high") to the vendor knob.
6. Maps provider-agnostic `ToolDef`/`ToolCall` to/from the vendor's tool schema.
7. Never leaks vendor types above the interface boundary.

**Config-driven construction.** Adapters are built from the `providers` block of config (spec `07`): model name, vendor, API key ref (env var name, never inline), cost table, capability flags. Costs live in config so they can be updated without a release.

### Reasoning-effort mapping table (guidance)

| Normalized | Anthropic | OpenAI reasoning models | Google | Local |
|---|---|---|---|---|
| `minimal` | thinking disabled | `reasoning_effort: minimal` | disable thinking | n/a |
| `low` | small thinking budget | `low` | low | n/a |
| `medium` | medium thinking budget | `medium` | medium | n/a |
| `high` | large thinking budget | `high` | high | n/a |

If a model lacks the knob (`Capabilities.Reasoning == false`), the adapter no-ops the field and the Thinking stage (spec `04`) compensates by using an explicit tool-check pre-pass rather than relying on internal reasoning.

> **Model names are config, not constants.** Do not hardcode model identifiers in adapters. The example config in `07` lists current model names (Anthropic Opus/Sonnet/Haiku, plus OpenAI and Google entries) but the coding agent must treat these as operator-supplied strings resolved through the registry. Verify current model identifiers against each vendor's live docs at implementation time.

---

## 2. Vendors to Implement (v1)

Priority order:

1. **Anthropic** (`anthropic`) — reference adapter. Messages API; extended thinking mapped from `ReasoningEffort`; native tool-use blocks map cleanly to `Block`/`ToolCall`.
2. **OpenAI** (`openai`) — second adapter, proves the abstraction across vendors. Reasoning-effort models supported.
3. **Google** (`google`) — Gemini family. Needed for genuine Council diversity.
4. **Local** (`local`) — OpenAI-compatible endpoint (e.g. llama.cpp / vLLM server) via base-URL config. No reasoning knob; cost = 0.

Each adapter needs a contract test (see §4).

---

## 3. Engine HTTP/SSE Surface

The engine runs as `harnessd`, exposing a local HTTP API. The Tooey TUI is **server-driven** (it has a built-in SSE client + HTTP action posting — confirmed from the Tooey feature set), so this surface is the integration seam for both TUI and Skill.

### Endpoints

```
POST /v1/tasks
  body: { "text": string, "overrides": { ...partial config... }? }
  → 201 { "task_id": string }
  Starts a task. `overrides` shallow-merges onto loaded config for THIS task
  only (e.g. force strategy=council, or bump budget). Enables on-the-fly control.

GET /v1/tasks/{id}/events           (SSE)
  → text/event-stream
  Streams events.Event as `data: <json>\n\n`, in Seq order. Replays from Seq 0
  if the task already has history (so a late-connecting TUI sees the full run),
  then live-tails. Sends `event: done` when the task reaches task.final or error.

GET /v1/tasks/{id}
  → 200 pipeline.State (JSON)   # snapshot, for resume/inspection

POST /v1/tasks/{id}/resume
  → 202                          # rebuild remaining pipeline, continue

GET  /v1/config                   # current effective config (redacted secrets)
PUT  /v1/config                   # replace config; triggers hot-reload (§5)
POST /v1/config/reload            # re-read config file from disk

GET  /v1/providers                # list registered models + capabilities + cost
GET  /v1/healthz
```

### SSE framing

- One event per message: `data: {json}\n\n`.
- Include a comment heartbeat `: ping\n\n` every 15s to keep the connection alive.
- On replay-then-tail, the server reads persisted events for the task (persist events alongside state, or reconstruct from the bus ring-buffer) before subscribing to live.

> Event persistence: keep a per-task append-only `events.ndjson` next to the state file so `/events` can replay after a reconnect or engine restart. This is cheap and makes the TUI robust.

---

## 4. Testing

- **Contract test harness:** a table-driven suite that every adapter must pass, using recorded fixtures (record real responses once, replay in CI). Asserts: text extraction, tool_use round-trip, usage/cost computation, reasoning-effort field mapping, streaming delta ordering.
- **Fake provider:** an in-memory `Provider` (`internal/provider/fake`) with scripted responses and configurable latency/cost. Used by every downstream spec's tests so they never hit the network.
- **SSE test:** start `harnessd` with the fake provider, POST a task, assert the event stream contains the expected `kind` sequence and terminates with `done`.

---

## 5. Config Hot-Reload

- `PUT /v1/config` and `POST /v1/config/reload` rebuild the provider registry, Receptionist rules, and mechanism defaults **atomically** — swap a new immutable config struct behind an `atomic.Pointer`. In-flight tasks keep their captured config; new tasks use the new one.
- Invalid config is rejected (validated against schema in `07`) with a 400 and the old config stays live. Never leave the engine in a half-updated state.

---

## 6. Acceptance Criteria

- [ ] Anthropic and one other vendor adapter pass the shared contract test with fixtures.
- [ ] `Usage.CostUSD` is computed correctly from token counts and config cost tables (unit-tested with known values).
- [ ] `ReasoningEffort` maps to the correct vendor field per adapter; absent-capability models no-op without error.
- [ ] `POST /v1/tasks` starts a task and returns an ID; `GET /events` streams a valid SSE sequence ending in `done`.
- [ ] A TUI/client connecting *after* a task finished still receives the full replayed history from Seq 0.
- [ ] `PUT /v1/config` with invalid config returns 400 and leaves the running config unchanged; with valid config, new tasks observe the change and in-flight tasks do not.
- [ ] The `fake` provider exists and is used by at least one end-to-end SSE test.
