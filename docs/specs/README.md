# Harness Specs

Numbered specs for the Kyotee multi-model harness. Read `00-overview.md`
first; `10-build-sequence.md` is the dependency-ordered build plan.

All eleven specs (00–10) are present.

Kyotee-specific adaptations from the specs (deliberate, small):

- Single `kyotee` binary instead of `harnessd` + `harness-cli`
  (`kyotee serve` = harnessd; `kyotee ask/resume/status/providers/config
  validate` = the CLI shim from spec 09, honouring `KYOTEE_URL` first and
  `HARNESS_URL` as a fallback; `kyotee ask --local` additionally runs a
  one-shot in-process engine, no daemon needed).
- State and config live under `~/.kyotee/` rather than `~/.harness/`.
- `google`/`local` vendors ride the OpenAI-compatible adapter (Gemini via
  Google's compat endpoint) instead of bespoke adapters — same contract,
  fewer moving parts. Anthropic has its own Messages-API adapter.
- `council.members` is a config-level default member list, used when a route
  (or an override escalating to council) doesn't name its own.
