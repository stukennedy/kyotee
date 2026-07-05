---
name: harness
description: Delegate a hard decision or conundrum to the Kyotee harness engine —
  a cross-vendor Council that debates to consensus, a Two-Brain divergent/
  convergent debate, or a forced tool-checking solo run. Use when a decision
  is high-stakes, genuinely contested, or benefits from multiple model
  perspectives, and you want a synthesised answer (with dissent noted) back
  in this session. Requires a running kyotee engine (see setup).
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
into a council — it delegates to the external Kyotee engine and brings the
synthesised result back. Think "consult a panel," not "become the panel."

## Setup

- Ensure the engine is running (`kyotee serve`) and `KYOTEE_URL` points at it
  (default http://127.0.0.1:8484; `HARNESS_URL` is honoured too).
- Ensure provider API keys are configured in the engine's config
  (`~/.kyotee/config.yaml`; check with `kyotee providers`).

## Usage

Run the CLI and capture the synthesised answer (progress streams to stderr;
stdout carries only the answer):

    kyotee ask "Should we adopt event sourcing for the billing service?" \
        --strategy council --consensus judge --budget 3.00 --wait

Then incorporate the printed answer (and any noted dissent) into your work.

For machine-readable output add `--json`, which emits a stable contract:

    {
      "task_id": "…",
      "answer": "…",
      "strategy": "council",
      "consensus": { "reached": true, "method": "judge", "rounds_used": 2 },
      "dissent": ["gemini-3-pro favored a simpler queue-based approach"],
      "total_cost_usd": 1.87,
      "total_tokens": 41230
    }

`dissent` is populated when the council deadlocked with noted disagreement or
a judge flagged holdouts — surface it rather than presenting false confidence.

Other subcommands: `kyotee resume <task_id> --wait`, `kyotee status <task_id>`,
`kyotee providers`, `kyotee config validate <file>`.

The CLI exits non-zero on engine errors or when the budget was exhausted
before any answer was produced; without a running engine it fails fast with a
"start `kyotee serve` / check KYOTEE_URL" message.
