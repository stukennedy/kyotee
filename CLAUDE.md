# Kyotee

Multi-model AI harness: an engine that routes each task through a cheap
classifier to a solving strategy — solo, two-brain, or council of
vendor-diverse models — with structural fast/slow thinking gates, a tool-need
pre-pass, hard budget enforcement, and a Tooey TUI observing everything over
SSE.

## Build & Test

```bash
go build ./...
go test ./...          # all packages; fakes only, no network or API keys
go test -race ./...    # council/executor are concurrent — keep this green
```

## Run

```bash
kyotee init                      # write default config to ~/.kyotee/config.yaml
kyotee                           # engine + TUI in one process
kyotee serve                     # headless engine (HTTP/SSE on :8484)
kyotee tui --url http://...      # attach TUI to a running engine
kyotee ask "prompt" [--strategy council] [--thinking slow] [--budget 5]
```

Provider API keys come from env vars named in the config (`ANTHROPIC_API_KEY`,
`OPENAI_API_KEY`, `GOOGLE_API_KEY` by default). `vendor: mock` providers run
without keys (used by all tests).

## Architecture

Specs live in `docs/specs/` (00–08 and 10 provided; 09 skill-adapter still
pending — see docs/specs/README.md for Kyotee-specific adaptations).
Dependency order:

- `internal/provider` — vendor-agnostic `Provider` interface, registry,
  Anthropic + OpenAI-compatible adapters, scriptable `Fake` for tests.
- `internal/events` — `Event` catalog + in-memory `Bus` with per-task Seq
  numbering and full-history replay (feeds SSE).
- `internal/pipeline` — `Stage`, `State` (the persisted envelope), `Executor`
  (checkpoints after every stage, halts on budget, promotes Draft→Final).
- `internal/state` — atomic JSON file store, `~/.kyotee/tasks/`.
- `internal/budget` — 50/80/95% warns + worst-case preflight estimates.
- `internal/config` — spec-07 YAML schema (version 1), full validation
  table, hot-reloadable `Holder`, registry/tools/embedder builders.
- `internal/receptionist` — classify (cheap model, strict JSON, safe
  fallback) → first-match-wins route → preflight downgrade → assemble stages.
- `internal/thinking` — fast/slow auto gate, tool-need pre-pass, tool
  registry (`web_search`), shared tool-use loop, Solo stage.
- `internal/twobrain` — divergent/convergent rounds (temperature split is
  the mechanism: div ~1.0, conv ~0.3), external persona prompt files,
  referee synthesis.
- `internal/council` — parallel openings, rebuttal rounds, consensus via
  vote/similarity/judge, deadlock handling, Synthesis stage.
- `internal/server` — engine lifecycle + HTTP/SSE surface (`/v1/tasks`,
  `/v1/tasks/{id}/events` with `event: done` + ndjson replay across
  restarts, `/v1/config[/reload]`, `/v1/providers`, `/v1/healthz`).
- `internal/tui` — Tooey (v0.5, generic Elm API) front-end; pure SSE
  consumer + HTTP action poster; modals are Overlay + focus scopes
  (Escape → DismissMsg); golden-frame tests via tooeytest.
- `remote.go` (main package) — spec-09 CLI shim: `ask/resume/status/
  providers` as stateless HTTP clients with `--wait` SSE tailing and the
  stable `--json` contract (answer, consensus, dissent, cost); consumed by
  `skill/SKILL.md`.

## Conventions

- Model verdicts are strict JSON: prompt demands JSON-only, parse with
  `internal/jsonx` (defensive: fences, embedded objects, last-object votes).
- Stages communicate through `State.Meta` (`thinking.mode`, `thinking.tools`,
  `council.outcome`, …), never through package globals.
- Every observable behaviour must emit an event from the catalog in
  `internal/events/events.go`; the TUI renders only what the bus carries.
- Fail open toward slow thinking and safe defaults: classifier/gate failures
  must never block a task.
