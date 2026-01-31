package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stukennedy/kyotee/internal/skills"
)

func TestIntegrationVerifyPipeline(t *testing.T) {
	dir := t.TempDir()

	// 1. Generate AGENTS.md
	spec := map[string]any{
		"goal":     "Build CLI tool",
		"language": "Go",
	}
	skill := &skills.Skill{
		Name:        "Go CLI",
		Description: "Go CLI patterns",
		Tags:        []string{"go", "cli"},
	}

	content, err := GenerateAgentsFile(spec, skill, dir)
	if err != nil {
		t.Fatalf("GenerateAgentsFile: %v", err)
	}
	if !strings.Contains(content, "Build CLI tool") {
		t.Error("agents file missing goal")
	}

	// Verify we can load it back
	loaded, err := LoadAgentsFile(dir)
	if err != nil {
		t.Fatalf("LoadAgentsFile: %v", err)
	}
	if loaded != content {
		t.Error("loaded content doesn't match generated")
	}

	// 2. Create test files - one clean, one stubby
	os.WriteFile(filepath.Join(dir, "clean.go"), []byte(`package main

import "fmt"

func main() {
	fmt.Println("Hello, world!")
}

func Add(a, b int) int {
	return a + b
}
`), 0644)

	os.WriteFile(filepath.Join(dir, "stubby.go"), []byte(`package main

// TODO: implement this properly
func Process() error {
	return nil
}
`), 0644)

	// 3. Run stub detection
	cleanStubs, err := detectStubs(filepath.Join(dir, "clean.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cleanStubs) > 0 {
		t.Errorf("clean file had stubs: %v", cleanStubs)
	}

	stubbyStubs, err := detectStubs(filepath.Join(dir, "stubby.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(stubbyStubs) == 0 {
		t.Error("stubby file should have stubs")
	}

	// Verify the stub mentions TODO
	found := false
	for _, s := range stubbyStubs {
		if strings.Contains(s, "TODO") {
			found = true
		}
	}
	if !found {
		t.Error("expected TODO in stub detection output")
	}

	// 4. Test empty file detection
	os.WriteFile(filepath.Join(dir, "empty.go"), []byte(""), 0644)
	isEmpty, detail := checkEmptyFile(filepath.Join(dir, "empty.go"))
	if !isEmpty {
		t.Error("empty file not detected")
	}
	if !strings.Contains(detail, "0 bytes") {
		t.Errorf("unexpected detail: %s", detail)
	}

	// 5. Test wiring — clean.go exports Add, referenced by a main file
	os.WriteFile(filepath.Join(dir, "caller.go"), []byte(`package main

func init() {
	_ = Add(1, 2)
}
`), 0644)

	e := &Engine{RepoRoot: dir}
	issues := e.checkWiring([]string{"clean.go"})
	if len(issues) > 0 {
		t.Errorf("unexpected wiring issues: %v", issues)
	}
}
