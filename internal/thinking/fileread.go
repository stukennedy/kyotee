package thinking

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stukennedy/kyotee/internal/provider"
)

// FileRead is the pluggable file-reading tool (spec 07 tools block):
// reads files inside a configured sandbox root, never outside it.
type FileRead struct {
	name string
	root string
}

func NewFileRead(name, root string) *FileRead {
	if name == "" {
		name = "read_file"
	}
	return &FileRead{name: name, root: root}
}

func (f *FileRead) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        f.name,
		Description: fmt.Sprintf("Read a file (path relative to %s). Use for questions about actual code or file contents.", f.root),
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path relative to the sandbox root",
				},
			},
			"required": []any{"path"},
		},
	}
}

const fileReadLimit = 64 * 1024

func (f *FileRead) Exec(_ context.Context, input map[string]any) (string, error) {
	rel, _ := input["path"].(string)
	if strings.TrimSpace(rel) == "" {
		return "", fmt.Errorf("%s: empty path", f.name)
	}
	root, err := filepath.Abs(f.root)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(root, rel))
	if err != nil {
		return "", err
	}
	if target != root && !strings.HasPrefix(target, root+string(filepath.Separator)) {
		return "", fmt.Errorf("%s: path escapes sandbox root", f.name)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return "", err
	}
	if len(data) > fileReadLimit {
		data = data[:fileReadLimit]
	}
	return string(data), nil
}
