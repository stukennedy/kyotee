package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectStubs(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantStub bool
	}{
		{"TODO marker", "func foo() {\n\t// TODO: implement\n}", true},
		{"FIXME marker", "x := 1\nFIXME: broken\n", true},
		{"return null", "func bar() *int {\n\treturn nil\n}", true},
		{"return empty obj", "function x() {\n  return {}\n}", true},
		{"return empty array", "function x() {\n  return []\n}", true},
		{"python pass", "def foo():\n    pass\n", true},
		{"panic not implemented", `panic("not implemented")`, true},
		{"throw not implemented", `throw new Error("not implemented")`, true},
		{"placeholder text", "lorem ipsum dolor sit amet", true},
		{"not implemented text", "this is not implemented yet", true},
		{"coming soon", "feature coming soon", true},
		{"clean code", "func Add(a, b int) int {\n\treturn a + b\n}\n", false},
		{"clean multiline", "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			f := filepath.Join(dir, "test.go")
			if err := os.WriteFile(f, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}
			stubs, err := detectStubs(f)
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantStub && len(stubs) == 0 {
				t.Errorf("expected stubs but got none")
			}
			if !tt.wantStub && len(stubs) > 0 {
				t.Errorf("expected no stubs but got: %v", stubs)
			}
		})
	}
}

func TestCheckEmptyFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		isEmpty bool
	}{
		{"empty file", "", true},
		{"only comments", "// just a comment\n// another\n", true},
		{"one line", "package main\n", true},
		{"real code", "package main\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			f := filepath.Join(dir, "test.go")
			if err := os.WriteFile(f, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}
			empty, _ := checkEmptyFile(f)
			if empty != tt.isEmpty {
				t.Errorf("checkEmptyFile = %v, want %v", empty, tt.isEmpty)
			}
		})
	}
}

func TestCheckWiring(t *testing.T) {
	dir := t.TempDir()

	// Create a Go file with exported symbol
	os.MkdirAll(filepath.Join(dir, "pkg"), 0755)
	os.WriteFile(filepath.Join(dir, "pkg", "handler.go"), []byte(`package pkg

func HandleRequest() string {
	return "ok"
}
`), 0644)

	// Create a file that references it
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

import "pkg"

func main() {
	pkg.HandleRequest()
}
`), 0644)

	// Create an unreferenced file
	os.WriteFile(filepath.Join(dir, "pkg", "orphan.go"), []byte(`package pkg

func OrphanFunc() string {
	return "nobody calls me"
}
`), 0644)

	e := &Engine{RepoRoot: dir}

	// Test wiring with referenced symbol — should pass
	issues := e.checkWiring([]string{"pkg/handler.go"})
	if len(issues) > 0 {
		t.Errorf("expected no wiring issues for referenced symbol, got: %v", issues)
	}

	// Test wiring with unreferenced symbol — should fail
	issues = e.checkWiring([]string{"pkg/orphan.go"})
	if len(issues) == 0 {
		t.Errorf("expected wiring issues for unreferenced symbol")
	}
}

func TestCheckWiringJS(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "utils.ts"), []byte(`export function formatDate(d: Date): string {
	return d.toISOString()
}
`), 0644)

	os.WriteFile(filepath.Join(dir, "app.ts"), []byte(`import { formatDate } from './utils'
console.log(formatDate(new Date()))
`), 0644)

	e := &Engine{RepoRoot: dir}
	issues := e.checkWiring([]string{"utils.ts"})
	if len(issues) > 0 {
		t.Errorf("expected no wiring issues for JS export, got: %v", issues)
	}
}

func TestCheckWiringPython(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "models.py"), []byte(`class UserModel:
    def __init__(self):
        self.name = ""

def create_user():
    return UserModel()
`), 0644)

	os.WriteFile(filepath.Join(dir, "app.py"), []byte(`from models import UserModel, create_user
user = create_user()
`), 0644)

	e := &Engine{RepoRoot: dir}
	issues := e.checkWiring([]string{"models.py"})
	if len(issues) > 0 {
		t.Errorf("expected no wiring issues for Python, got: %v", issues)
	}
}
