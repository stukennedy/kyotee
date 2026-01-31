package orchestrator

import (
	"testing"

	"github.com/stukennedy/kyotee/internal/types"
)

func TestExtractPlanSteps(t *testing.T) {
	e := &Engine{}

	tests := []struct {
		name    string
		input   map[string]any
		wantN   int
		wantErr bool
	}{
		{
			"valid plan",
			map[string]any{
				"plan": map[string]any{
					"steps": []any{
						map[string]any{
							"id":             "1",
							"goal":           "Setup project",
							"actions":        []any{"init", "install"},
							"expected_files": []any{"go.mod", "main.go"},
							"checks":         []any{"go build"},
						},
						map[string]any{
							"id":   "2",
							"goal": "Add handler",
						},
					},
				},
			},
			2, false,
		},
		{
			"missing plan field",
			map[string]any{"other": "data"},
			0, true,
		},
		{
			"missing steps",
			map[string]any{"plan": map[string]any{"summary": "x"}},
			0, true,
		},
		{
			"empty steps",
			map[string]any{"plan": map[string]any{"steps": []any{}}},
			0, false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps, err := e.extractPlanSteps(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(steps) != tt.wantN {
				t.Errorf("got %d steps, want %d", len(steps), tt.wantN)
			}
		})
	}

	// Verify fields parsed correctly
	plan := map[string]any{
		"plan": map[string]any{
			"steps": []any{
				map[string]any{
					"id":             "s1",
					"goal":           "Do thing",
					"actions":        []any{"act1"},
					"expected_files": []any{"file.go"},
					"checks":         []any{"check1"},
				},
			},
		},
	}
	steps, _ := e.extractPlanSteps(plan)
	if steps[0].ID != "s1" || steps[0].Goal != "Do thing" {
		t.Error("fields not parsed correctly")
	}
	if len(steps[0].Actions) != 1 || steps[0].Actions[0] != "act1" {
		t.Error("actions not parsed")
	}
	if len(steps[0].ExpectedFiles) != 1 || steps[0].ExpectedFiles[0] != "file.go" {
		t.Error("expected_files not parsed")
	}
}

func TestChunkSteps(t *testing.T) {
	makeSteps := func(n int) []types.PlanStep {
		steps := make([]types.PlanStep, n)
		for i := range steps {
			steps[i] = types.PlanStep{ID: string(rune('A' + i))}
		}
		return steps
	}

	tests := []struct {
		name      string
		n         int
		chunkSize int
		wantN     int
		wantLast  int
	}{
		{"empty", 0, 3, 0, 0},
		{"single", 1, 3, 1, 1},
		{"exact", 6, 3, 2, 3},
		{"remainder", 7, 3, 3, 1},
		{"chunk of 1", 5, 1, 5, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := chunkSteps(makeSteps(tt.n), tt.chunkSize)
			if len(chunks) != tt.wantN {
				t.Errorf("got %d chunks, want %d", len(chunks), tt.wantN)
			}
			if tt.wantN > 0 && len(chunks[len(chunks)-1]) != tt.wantLast {
				t.Errorf("last chunk has %d items, want %d", len(chunks[len(chunks)-1]), tt.wantLast)
			}
		})
	}
}

func TestDetectCheckpoint(t *testing.T) {
	// From control JSON
	cp := detectCheckpoint("", map[string]any{
		"checkpoint": map[string]any{
			"type":    "decision",
			"message": "Use A or B?",
			"options": []any{"Option A", "Option B"},
		},
	})
	if cp == nil {
		t.Fatal("expected checkpoint from JSON")
	}
	if cp.Type != "decision" || cp.Message != "Use A or B?" {
		t.Errorf("wrong checkpoint: %+v", cp)
	}
	if len(cp.Options) != 2 {
		t.Errorf("expected 2 options, got %d", len(cp.Options))
	}

	// From raw output regex
	raw := `Some text {"checkpoint": {"type": "human-verify", "message": "Check it"}} more text`
	cp = detectCheckpoint(raw, map[string]any{})
	if cp == nil {
		t.Fatal("expected checkpoint from raw output")
	}
	if cp.Type != "human-verify" {
		t.Errorf("wrong type: %s", cp.Type)
	}

	// No checkpoint
	cp = detectCheckpoint("normal output", map[string]any{"phase": "implement"})
	if cp != nil {
		t.Error("expected nil checkpoint")
	}
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantKey string
		wantErr bool
	}{
		{
			"markdown block",
			"Here's the output:\n```json\n{\"phase\": \"plan\"}\n```\n",
			"phase", false,
		},
		{
			"markdown no lang",
			"```\n{\"key\": \"val\"}\n```",
			"key", false,
		},
		{
			"raw JSON",
			`{"status": "ok"}`,
			"status", false,
		},
		{
			"JSON with surrounding text",
			"Here is the result: {\"data\": 42} and that's it.",
			"data", false,
		},
		{
			"no JSON",
			"just plain text with no braces",
			"", true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := extractJSON(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if _, ok := result[tt.wantKey]; !ok {
				t.Errorf("missing key %q in result %v", tt.wantKey, result)
			}
		})
	}
}
