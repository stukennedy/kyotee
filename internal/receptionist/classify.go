package receptionist

import (
	"context"

	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/jsonx"
	"github.com/stukennedy/kyotee/internal/pipeline"
	"github.com/stukennedy/kyotee/internal/provider"
)

const classifierSystem = `You classify incoming tasks for an AI orchestration engine. You do NOT solve the task.

Dimensions:
- complexity: "trivial" (greetings, one-fact questions, tiny edits) | "standard" (typical single-skill work) | "hard" — ONLY for genuinely multi-constraint, high-stakes, or ambiguous problems; "hard" unlocks expensive multi-model strategies.
- domain: "code" | "research" | "reasoning" | "creative" | "chat"
- tool_need: "required" for ANY request hinging on current/present-state facts (dates, prices, who holds a role, "latest", "current", live status), file/repo access, or math you could not do reliably in your head. "likely" if tools would probably help. "none" otherwise.
- confidence: 0..1 self-estimate of this classification.
- rationale: one line, for the event log.

Respond with JSON ONLY, no prose, no fences:
{"complexity": "...", "domain": "...", "tool_need": "...", "confidence": 0.0, "rationale": "..."}`

// fallbackClass is the safe default when classification fails: never block
// the task on classifier failure (spec 03 §2).
var fallbackClass = pipeline.Classification{
	Complexity: "standard", Domain: "chat", ToolNeed: "likely", Confidence: 0.0,
	Rationale: "classifier fallback",
}

// Classify runs the cheap classifier model and parses its strict-JSON verdict
// defensively, falling back to a safe default on any failure.
func (r *Receptionist) Classify(ctx context.Context, st *pipeline.State, emit events.Emitter) pipeline.Classification {
	cfg := r.Cfg.Get()
	model, err := r.resolve(cfg.Models.Receptionist, cfg)
	if err != nil {
		r.classifyWarn(emit, "no receptionist model: "+err.Error())
		return fallbackClass
	}

	resp, err := model.Generate(ctx, provider.Request{
		System:    classifierSystem,
		Messages:  []provider.Message{provider.UserText("Task: " + st.Original)},
		MaxTokens: 300,
		Metadata:  map[string]string{"task_id": st.TaskID, "stage": "classify"},
	})
	if err != nil {
		r.classifyWarn(emit, "classifier error: "+err.Error())
		return fallbackClass
	}
	st.AddTurn("receptionist", "classifier", resp.Text(), resp.Usage)

	var class pipeline.Classification
	if err := jsonx.Parse(resp.Text(), &class); err != nil {
		r.classifyWarn(emit, "classifier parse failure: "+err.Error())
		return fallbackClass
	}
	if !valid(class.Complexity, "trivial", "standard", "hard") {
		class.Complexity = "standard"
	}
	if !valid(class.Domain, "code", "research", "reasoning", "creative", "chat") {
		class.Domain = "chat"
	}
	if !valid(class.ToolNeed, "none", "likely", "required") {
		class.ToolNeed = "likely"
	}
	return class
}

func (r *Receptionist) classifyWarn(emit events.Emitter, msg string) {
	emit(events.Event{
		Kind: events.KindError, Actor: "receptionist",
		Payload: map[string]any{"level": "warn", "message": msg + " — using safe default classification"},
	})
}

func valid(s string, allowed ...string) bool {
	for _, a := range allowed {
		if s == a {
			return true
		}
	}
	return false
}
