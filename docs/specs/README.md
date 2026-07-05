# Harness Specs

Numbered specs for the Kyotee multi-model harness. Read `00-overview.md`
first; `10-build-sequence.md` is the dependency-ordered build plan.

Provided: 00, 01, 02, 03, 04, 05, 06, 07, 08, 10.

Still missing: **09 — Skill adapter** (the `SKILL.md` + thin CLI shim so
Claude Code can invoke Harness patterns mid-session). The engine surface it
needs (`ask`/`resume`/`status`/`providers` over HTTP) is already in place;
implement `skill/SKILL.md` + shim once the spec lands.

Kyotee-specific adaptations from the specs (deliberate, small):

- Single `kyotee` binary instead of `harnessd` + `harness-cli`
  (`kyotee serve` = harnessd; `kyotee ask/config validate` = the CLI).
- State and config live under `~/.kyotee/` rather than `~/.harness/`.
- `google`/`local` vendors ride the OpenAI-compatible adapter (Gemini via
  Google's compat endpoint) instead of bespoke adapters — same contract,
  fewer moving parts. Anthropic has its own Messages-API adapter.
- `council.members` is a config-level default member list, used when a route
  (or an override escalating to council) doesn't name its own.
