# 04 — Thinking Modes: Fast/Slow Gate & Tool-Need Pre-Pass

Implements dynamic thinking effort and — critically — the mechanism that stops the model answering from working memory when it should reach for a tool. This is the direct solution to the "who is the prime minister?" failure. Depends on `01`, `02`.

Lives in `internal/thinking`.

---

## 1. The Problem This Solves

LLMs default to **fast thinking**: they emit whatever is readily available in-weights rather than pausing to ask whether they should look elsewhere. Asked for a present-state fact, the model confidently returns stale training data instead of searching. We cannot rely on the model to *spontaneously* slow down. So we make slowing-down a **cheap, explicit, structural step** that runs *before* the answer is generated.

Two levers:
- **Effort** — how hard the solver thinks (maps to `Request.ReasoningEffort`).
- **Tool-check pre-pass** — a forced metacognitive gate: *"before answering, what would I need to look up, and is anything here a present-state fact that could be stale?"*

---

## 2. The Thinking Stage

`thinking.Stage` implements `pipeline.Stage`. It runs before the solver stage and writes its decisions into `State.Meta` for the solver to read.

```go
// Keys written into State.Meta by the Thinking stage:
//   "thinking.mode"    = "fast" | "slow"
//   "thinking.effort"  = "minimal" | "low" | "medium" | "high"
//   "thinking.tools"   = comma-separated tool names the solver SHOULD use, or ""
```

### Mode resolution

Input mode comes from the Route (`fast` | `slow` | `auto`).

- `fast` → mode=fast, effort=minimal|low (config), **skip** pre-pass.
- `slow` → mode=slow, effort=high, **run** pre-pass.
- `auto` → run the **cheap gate** (below) to decide fast vs slow at runtime.

Emit `thinking.mode` with the chosen mode and a one-line reason.

### The auto gate (runtime fast/slow decision)

A single cheap classifier call (reuse the receptionist model) answering strict-JSON:

```json
{
  "needs_slow": true,
  "reasons": ["present_state_fact", "multi_step_math"],
  "suggested_tools": ["web_search"]
}
```

Slow-triggering signals the gate must check for (config-tunable `slow_triggers`):
- `present_state_fact` — office-holders, prices, "current/latest/now", live status, anything time-sensitive.
- `low_confidence` — the receptionist `Confidence` was below threshold.
- `multi_step_math` — arithmetic/derivation the model shouldn't do in-head.
- `repo_or_file_ref` — needs to read actual code/files.
- `explicit_user_flag` — user said "think hard", "be careful", "check".

If any fire → mode=slow, effort=high. Else → mode=fast.

> Cost note: the gate is one small call. It is far cheaper than a wrong confident answer or an unnecessary full slow-mode run on trivial input. This asymmetry is the whole justification.

---

## 3. The Tool-Need Pre-Pass (the core mechanism)

When mode=slow (or `auto` resolved to slow), before the solver generates its answer, run the pre-pass:

1. Prompt (cheap model or the solver itself with minimal effort) with the task and the **available tool list**, demanding strict-JSON:

```json
{
  "must_look_up": ["current UK prime minister"],
  "tools_to_use": ["web_search"],
  "safe_from_memory": ["definition of 'prime minister'"],
  "verdict": "use_tools"        // "use_tools" | "answer_directly"
}
```

2. Emit `thinking.tool_check` with `needs_tool` and the verdict.
3. If `verdict == use_tools`, write the tool names to `State.Meta["thinking.tools"]`. The solver stage (Solo / Two-Brain / Council member) **must** attempt those tools before finalising, and the solver's system prompt is augmented with an instruction: *"You have been flagged to use these tools before answering: <list>. Do not answer from memory for the flagged facts."*

This converts "hope the model searches" into "the harness has already determined a search is required and instructed the solver accordingly."

### Tooling

- Tools are provider-agnostic `provider.ToolDef`s registered in a `thinking.ToolRegistry` (v1: at minimum a `web_search` tool; repo/file tools pluggable). The engine executes tool calls returned by the solver and feeds `tool_result` blocks back — a standard tool-use loop.
- Emit `tool.call` / `tool.result` for each.
- v1 must ship at least one real tool (`web_search`) so the prime-minister case works end-to-end. Additional tools (file read, code exec) are pluggable via the same registry.

---

## 4. Interaction With Solver Stages

The solver stages (specs `05`, `06`, and the Solo stage) read `State.Meta`:
- Set `Request.ReasoningEffort` from `thinking.effort`.
- If `thinking.tools` is non-empty, inject the tool list + the "don't answer from memory" instruction, and run the tool-use loop until the model stops requesting tools or a call cap is hit.

Solo stage (minimal, defined here for completeness):

```go
// Solo runs one model to produce the Draft, honoring thinking.* meta and
// executing any tool-use loop. Writes result to State.Draft + a Turn.
type Solo struct{ Model string }
```

---

## 5. Acceptance Criteria

- [ ] With mode=`auto` and the prompt "who is the prime minister?", the auto gate returns `needs_slow=true` (present_state_fact), the pre-pass returns `verdict=use_tools` with `web_search`, the solver performs a `web_search` tool call, and the final answer is grounded in the tool result — **not** training data. Verify via the emitted `tool.call`/`tool.result` events.
- [ ] With mode=`auto` and a timeless prompt ("what is a hash map?"), the gate returns `needs_slow=false` and no tool pre-pass runs (fast path, no extra tool cost).
- [ ] `thinking.effort` is correctly propagated into `Request.ReasoningEffort` for the solver (asserted with the fake provider capturing the field).
- [ ] Explicit user flag "think hard" forces slow mode even on an otherwise-trivial prompt.
- [ ] Tool-use loop terminates at the configured call cap and does not infinite-loop.
- [ ] All decisions are observable via `thinking.mode`, `thinking.tool_check`, `tool.call`, `tool.result` events.
