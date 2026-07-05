# 09 — Claude Code Skill Adapter

A thin adapter that lets Claude Code invoke Harness patterns mid-session — chiefly "escalate this decision to a cross-vendor council" or "run this with forced tool-checking." It is a `SKILL.md` plus a small CLI shim (`harness-cli`) that talks to the running engine's HTTP API. Depends on `02` (HTTP surface), `07` (overrides).

---

## 1. What This Is (and Isn't)

- **Is:** a way to reach the Harness engine from inside a Claude Code session, so the operator can offload a hard sub-decision to the council/two-brain machinery and get a synthesised answer back into the coding session.
- **Isn't:** a re-implementation of orchestration. The Skill shells out to `harness-cli`, which calls the already-running `harnessd`. All logic stays in the engine.

**Honest limitation to document in the SKILL.md:** Claude Code is itself already a harness. Nesting orchestration is awkward — from inside one agent loop you cannot cleanly "become" a multi-provider council. So the Skill's realistic value is **delegation to the external engine** (spawn a council run, poll for the synthesised result, paste it back), not transforming Claude Code's own loop. Set that expectation explicitly.

---

## 2. CLI Shim: `harness-cli`

The same binary used for `config validate` (spec `07`) gains task subcommands. It is a stateless HTTP client for `harnessd`.

```
harness-cli ask "<prompt>" \
    [--strategy solo|twobrain|council] \
    [--thinking fast|slow|auto] \
    [--budget 3.00] \
    [--council-rounds 3] \
    [--consensus vote|similarity|judge] \
    [--json] \
    [--wait]

  Builds an `overrides` object from the flags, POSTs /v1/tasks, and (with
  --wait) streams events to stderr as a compact progress log while blocking,
  then prints the final answer to stdout (or full JSON with --json:
  { answer, total_cost_usd, total_tokens, consensus, dissent }).

harness-cli resume <task_id> [--wait]
harness-cli status <task_id>          # prints State snapshot
harness-cli config validate <file>
harness-cli providers                 # list registered models
```

- `--wait` consumes the SSE stream (replay-then-tail) and blocks until `task.final`/`error`, emitting a terse per-stage progress line to **stderr** (so stdout stays clean for capture). Without `--wait`, prints the `task_id` and returns immediately.
- Engine base URL from `HARNESS_URL` env (default `http://localhost:8787`).
- Exit non-zero on engine error or budget-exhausted-without-answer.

---

## 3. `SKILL.md`

Ships at `skill/SKILL.md`. Structure:

```markdown
---
name: harness
description: Delegate a hard decision or conundrum to the Harness engine — a
  cross-vendor Council that debates to consensus, a Two-Brain divergent/
  convergent debate, or a forced tool-checking solo run. Use when a decision
  is high-stakes, genuinely contested, or benefits from multiple model
  perspectives, and you want a synthesised answer (with dissent noted) back
  in this session. Requires a running harnessd (see setup).
---

# Harness Skill

## When to use
- A design/architecture decision with real trade-offs and no obvious winner
  → `--strategy council`.
- A creative-but-constrained problem needing option generation then rigorous
  pruning → `--strategy twobrain`.
- A question hinging on current/present-state facts you must not answer from
  memory → `--strategy solo --thinking slow` (forces the tool-check pre-pass).

## When NOT to use
- Routine coding you can do directly. This spins up external models and costs
  money; reserve it for genuinely hard sub-decisions.

## Limitation (read this)
Claude Code is already an agent loop. This skill does not turn *this* session
into a council — it delegates to the external Harness engine and brings the
synthesised result back. Think "consult a panel," not "become the panel."

## Setup
- Ensure `harnessd` is running and `HARNESS_URL` is set (default
  http://localhost:8787).
- Ensure provider API keys are configured in the engine's config.

## Usage
Run the CLI and capture the synthesised answer:

    harness-cli ask "Should we adopt event sourcing for the billing service?" \
        --strategy council --consensus judge --budget 3.00 --wait

Then incorporate the printed answer (and any noted dissent) into your work.
For machine-readable output add `--json`.
```

Keep the description tight and trigger-focused (it is what Claude Code matches on).

---

## 4. Result Handling

`--json` output shape (stable contract for the Skill to parse):

```json
{
  "task_id": "…",
  "answer": "…",              // the synthesised Draft/Final
  "strategy": "council",
  "consensus": { "reached": true, "method": "judge", "rounds_used": 2 },
  "dissent": ["gemini-3-pro favored a simpler queue-based approach"],
  "total_cost_usd": 1.87,
  "total_tokens": 41230
}
```

`dissent` is populated when deadlock resolution was `synthesis_notes_dissent` or a judge noted holdouts — surfacing genuine disagreement rather than a false-confident single answer.

---

## 5. Acceptance Criteria

- [ ] `harness-cli ask … --wait` starts a task on a running engine, streams progress to stderr, and prints the synthesised answer to stdout.
- [ ] Flags correctly build the `overrides` object (e.g. `--strategy council --council-rounds 3` reflected in the run, verifiable via `status`).
- [ ] `--json` emits the documented stable shape including `consensus` and `dissent`.
- [ ] Exit code is non-zero on engine error or budget-exhausted-without-answer.
- [ ] `SKILL.md` frontmatter description is trigger-focused and states the nesting limitation in the body.
- [ ] With no running engine, the CLI fails fast with a clear "is harnessd running / check HARNESS_URL" message.
