# 07 — Configuration Schema

The config file is the product's control surface: it declares providers, routing rules, and per-mechanism defaults. It is hot-reloadable (spec `02` §5). This spec defines the schema, validation, and a complete annotated example. Depends conceptually on all prior specs.

Format: YAML (parsed into an immutable Go struct held behind `atomic.Pointer`). Secrets are **never** inline — only env-var references.

---

## 1. Top-Level Shape

```yaml
version: 1
defaults:        # global fallbacks
providers:       # model registry
receptionist:    # classifier + routing rules + budget defaults
thinking:        # fast/slow gate tuning
twobrain:        # divergent/convergent defaults
council:         # council defaults
tools:           # tool registry (web_search, etc.)
embedder:        # optional, for council similarity consensus
```

---

## 2. Complete Annotated Example

```yaml
version: 1

defaults:
  budget_usd: 0.50            # global per-task ceiling unless a route overrides
  reasoning_effort_fast: low  # effort used in fast mode
  reasoning_effort_slow: high # effort used in slow mode
  tool_call_cap: 4            # max tool calls in a single solver loop

# --- Model registry -------------------------------------------------------
# Model names are OPERATOR-SUPPLIED strings. Verify current identifiers
# against each vendor's live docs at implementation time; do not assume.
providers:
  - name: claude-opus-4-8      # strong reasoning / referee / synthesis
    vendor: anthropic
    api_key_env: ANTHROPIC_API_KEY
    reasoning: true
    max_context: 200000
    cost_per_1m: { input: 5.00, output: 25.00 }   # example figures; set real ones
  - name: claude-sonnet-5      # balanced default coder
    vendor: anthropic
    api_key_env: ANTHROPIC_API_KEY
    reasoning: true
    max_context: 200000
    cost_per_1m: { input: 3.00, output: 15.00 }
  - name: claude-haiku-4-5     # cheap, fast — receptionist + gate
    vendor: anthropic
    api_key_env: ANTHROPIC_API_KEY
    reasoning: false
    max_context: 200000
    cost_per_1m: { input: 0.80, output: 4.00 }
  - name: gpt-5                # cross-vendor council member
    vendor: openai
    api_key_env: OPENAI_API_KEY
    reasoning: true
    max_context: 400000
    cost_per_1m: { input: 2.50, output: 10.00 }
  - name: gemini-3-pro         # cross-vendor council member
    vendor: google
    api_key_env: GOOGLE_API_KEY
    reasoning: true
    max_context: 1000000
    cost_per_1m: { input: 2.00, output: 8.00 }
  - name: local-qwen           # optional local, OpenAI-compatible
    vendor: local
    base_url: http://localhost:8000/v1
    reasoning: false
    max_context: 32768
    cost_per_1m: { input: 0.00, output: 0.00 }

# --- Receptionist ---------------------------------------------------------
receptionist:
  model: claude-haiku-4-5      # cheap classifier
  budget_default_usd: 0.50
  warn_thresholds: [0.5, 0.8, 0.95]

  # Routing rules: first match wins. `when` is a predicate over the
  # Classification fields (complexity, domain, tool_need, confidence).
  routes:
    - when: { complexity: trivial }
      strategy: solo
      thinking: fast
      models: { primary: claude-haiku-4-5 }
      budget_usd: 0.05

    - when: { domain: code, complexity: standard }
      strategy: solo
      thinking: auto
      models: { primary: claude-sonnet-5 }
      budget_usd: 0.30

    - when: { domain: code, complexity: hard }
      strategy: twobrain
      thinking: slow
      models:
        divergent: claude-sonnet-5
        convergent: claude-sonnet-5
        primary: claude-opus-4-8        # referee
      budget_usd: 1.50

    - when: { domain: reasoning, complexity: hard }
      strategy: council
      thinking: slow
      models:
        council: [claude-opus-4-8, gpt-5, gemini-3-pro]
        primary: claude-opus-4-8        # synthesiser
      budget_usd: 3.00

    # tool_need==required forces slow regardless of the matched route's mode.
    - when: { tool_need: required }
      strategy: solo
      thinking: slow
      models: { primary: claude-sonnet-5 }
      budget_usd: 0.40

    - when: {}                          # default catch-all
      strategy: solo
      thinking: auto
      models: { primary: claude-sonnet-5 }
      budget_usd: 0.30

# --- Thinking (fast/slow) -------------------------------------------------
thinking:
  gate_model: claude-haiku-4-5   # cheap model for the auto gate + pre-pass
  slow_triggers:                 # any firing → slow mode
    - present_state_fact
    - low_confidence
    - multi_step_math
    - repo_or_file_ref
    - explicit_user_flag
  low_confidence_below: 0.7
  prepass_model: claude-haiku-4-5  # tool-need pre-pass model

# --- Two-Brain ------------------------------------------------------------
twobrain:
  rounds: 2                     # 1..3, hard-capped at 3
  div_temp: 1.0
  conv_temp: 0.3
  prompts:
    divergent: config/prompts/divergent.md
    convergent: config/prompts/convergent.md
    referee: config/prompts/referee.md

# --- Council --------------------------------------------------------------
council:
  rounds: 3                     # hard-capped
  protocol: debate
  consensus:
    method: vote                # vote | similarity | judge
    threshold: 0.66             # meaning depends on method
  on_deadlock: synthesis_notes_dissent  # referee | majority_vote | synthesis_notes_dissent
  require_vendor_diversity: true         # warn (not fail) if members share a vendor

# --- Tools ----------------------------------------------------------------
tools:
  - name: web_search
    kind: web_search            # built-in
  # - name: read_file
  #   kind: file_read
  #   root: /path/to/repo

# --- Embedder (only needed if council.consensus.method == similarity) ------
embedder:
  provider: openai              # or any vendor exposing embeddings
  model: text-embedding-3-large
  api_key_env: OPENAI_API_KEY
```

---

## 3. Validation Rules

The loader must reject invalid config (return 400 on `PUT`, keep old config live):

- `version` must be `1`.
- Every model referenced in any route (`primary`, `divergent`, `convergent`, `council[]`), in `receptionist.model`, `thinking.*_model`, and `embedder` **must exist** in `providers`.
- Every `providers[].api_key_env` (or `base_url` for local) must be present; if an env var is named but unset at startup, warn loudly (the provider is unusable until set).
- `twobrain.rounds` ∈ [1,3]; `council.rounds` ≥ 1 and ≤ configured hard cap (e.g. 5) — clamp or reject per policy (reject, with a clear message).
- `council.consensus.method == similarity` requires an `embedder` block.
- `council.consensus.threshold` ∈ (0,1]; `receptionist.warn_thresholds` sorted, each ∈ (0,1).
- `strategy` ∈ {solo, twobrain, council}; `thinking` ∈ {fast, slow, auto}; `on_deadlock` ∈ the allowed set; `consensus.method` ∈ {vote, similarity, judge}.
- Route `when` keys ∈ {complexity, domain, tool_need, confidence}; values ∈ the allowed enums for each.
- If `require_vendor_diversity` is true and a council route lists members all sharing a vendor → **warn**, don't fail (operator may intend it).

Provide a `harness-cli config validate <file>` command that runs the same validation and prints errors, for pre-flight checking before hot-reload.

---

## 4. Per-Task Overrides

`POST /v1/tasks` accepts an `overrides` object that shallow-merges onto the effective config **for that task only** (spec `02` §3). Overridable at minimum: `strategy`, `thinking`, `models`, `budget_usd`, `council.rounds`, `council.consensus`. This is what powers on-the-fly control from the TUI ("escalate this one to council") without editing the file. Overrides are validated with the same rules; invalid override → 400, task not started.

---

## 5. Acceptance Criteria

- [ ] A valid example config loads into the immutable struct and builds the provider registry.
- [ ] Each validation rule rejects a crafted invalid config with a clear, specific error (table test).
- [ ] A model referenced in a route but absent from `providers` is rejected.
- [ ] `similarity` consensus without an `embedder` block is rejected.
- [ ] `harness-cli config validate` reports errors and exits non-zero on invalid config.
- [ ] Per-task `overrides` merge correctly and are independently validated; an invalid override returns 400 without starting the task.
- [ ] Hot-reload swaps config atomically; in-flight tasks keep old config, new tasks get new config (integration with spec `02`).
