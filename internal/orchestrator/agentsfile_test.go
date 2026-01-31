package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stukennedy/kyotee/internal/skills"
)

func TestGenerateAgentsFile(t *testing.T) {
	dir := t.TempDir()

	spec := map[string]any{
		"goal":         "Build a REST API",
		"project_name": "myapi",
		"language":     "Go",
		"framework":    "Gin",
		"features":     []any{"auth", "CRUD"},
		"requirements": []any{"PostgreSQL", "Docker"},
	}

	skill := &skills.Skill{
		Name:        "Go REST",
		Description: "Go REST API patterns",
		Patterns:    map[string]string{"handler": "func Handle(c *gin.Context) {}"},
		Conventions: skills.Conventions{
			ProjectStructure: "cmd/\ninternal/\npkg/",
			Naming:           "camelCase",
			FileExtensions:   ".go",
		},
		Docs: skills.Docs{
			URLs: []string{"https://gin-gonic.com/docs/"},
		},
		Dependencies: []string{"gin", "gorm"},
		Preferences:  map[string]string{"testing": "table-driven"},
	}

	content, err := GenerateAgentsFile(spec, skill, dir)
	if err != nil {
		t.Fatalf("GenerateAgentsFile failed: %v", err)
	}

	// Check file was created
	agentsPath := filepath.Join(dir, ".kyotee", "AGENTS.md")
	if _, err := os.Stat(agentsPath); os.IsNotExist(err) {
		t.Fatal("AGENTS.md was not created")
	}

	// Check expected sections
	sections := []string{
		"Project Spec",
		"Tech Stack",
		"Coding Conventions",
		"Execution Rules",
		"Project Structure",
		"API Reference",
	}
	for _, s := range sections {
		if !strings.Contains(content, s) {
			t.Errorf("missing section: %s", s)
		}
	}

	// Check spec fields are included
	if !strings.Contains(content, "Build a REST API") {
		t.Error("goal not in output")
	}
	if !strings.Contains(content, "myapi") {
		t.Error("project name not in output")
	}
}

func TestGenerateAgentsFileNoSkill(t *testing.T) {
	dir := t.TempDir()
	spec := map[string]any{"goal": "test"}

	content, err := GenerateAgentsFile(spec, nil, dir)
	if err != nil {
		t.Fatalf("GenerateAgentsFile failed: %v", err)
	}
	if !strings.Contains(content, "Project Spec") {
		t.Error("missing Project Spec section")
	}
	if !strings.Contains(content, "Execution Rules") {
		t.Error("missing Execution Rules section")
	}
}

func TestLoadAgentsFile(t *testing.T) {
	dir := t.TempDir()

	// Missing file returns error
	_, err := LoadAgentsFile(dir)
	if err == nil {
		t.Error("expected error for missing file")
	}

	// Create and load
	kyoteeDir := filepath.Join(dir, ".kyotee")
	os.MkdirAll(kyoteeDir, 0755)
	expected := "# Test AGENTS.md\nHello"
	os.WriteFile(filepath.Join(kyoteeDir, "AGENTS.md"), []byte(expected), 0644)

	content, err := LoadAgentsFile(dir)
	if err != nil {
		t.Fatalf("LoadAgentsFile failed: %v", err)
	}
	if content != expected {
		t.Errorf("got %q, want %q", content, expected)
	}
}

func TestMatchSkillFromSpec(t *testing.T) {
	// Create a registry with test skills
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	os.MkdirAll(skillsDir, 0755)

	goSkill := &skills.Skill{
		Name:        "Go REST API",
		Description: "Go with Gin framework",
		Tags:        []string{"go", "gin", "rest"},
	}
	pySkill := &skills.Skill{
		Name:        "Python FastAPI",
		Description: "Python with FastAPI",
		Tags:        []string{"python", "fastapi", "rest"},
	}

	registry, err := skills.NewRegistry(skillsDir)
	if err != nil {
		t.Fatal(err)
	}
	registry.Add(goSkill)
	registry.Add(pySkill)

	tests := []struct {
		name     string
		spec     map[string]any
		wantName string
		wantNil  bool
	}{
		{"match by language", map[string]any{"language": "Go"}, "Go REST API", false},
		{"match by framework", map[string]any{"framework": "fastapi"}, "Python FastAPI", false},
		{"match by tech_stack", map[string]any{"tech_stack": "go+gin"}, "Go REST API", false},
		{"no match", map[string]any{"language": "Rust"}, "", true},
		{"nil spec", nil, "", true},
		{"empty spec", map[string]any{}, "", true},
		{"case insensitive", map[string]any{"language": "PYTHON"}, "Python FastAPI", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MatchSkillFromSpec(tt.spec, registry)
			if tt.wantNil {
				if result != nil {
					t.Errorf("expected nil, got %s", result.Name)
				}
				return
			}
			if result == nil {
				t.Fatal("expected non-nil result")
			}
			if result.Name != tt.wantName {
				t.Errorf("got %s, want %s", result.Name, tt.wantName)
			}
		})
	}

	// nil registry
	if r := MatchSkillFromSpec(map[string]any{"language": "Go"}, nil); r != nil {
		t.Error("expected nil for nil registry")
	}
}
