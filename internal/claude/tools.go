package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ToolExecutor handles tool execution
type ToolExecutor struct {
	WorkDir     string
	AllowedDirs []string // Directories where file operations are allowed
	OnToolCall  func(name string, input any)
	OnToolResult func(name string, result string, isError bool)
}

// NewToolExecutor creates a new tool executor
func NewToolExecutor(workDir string) *ToolExecutor {
	return &ToolExecutor{
		WorkDir:     workDir,
		AllowedDirs: []string{workDir},
	}
}

// GetTools returns the tool definitions for Claude
func GetTools() []Tool {
	return []Tool{
		{
			Name:        "read_file",
			Description: "Read the contents of a file at the specified path",
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"path": {
						Type:        "string",
						Description: "The file path to read (absolute or relative to working directory)",
					},
				},
				Required: []string{"path"},
			},
		},
		{
			Name:        "write_file",
			Description: "Write content to a file at the specified path. Creates directories if needed.",
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"path": {
						Type:        "string",
						Description: "The file path to write to",
					},
					"content": {
						Type:        "string",
						Description: "The content to write to the file",
					},
				},
				Required: []string{"path", "content"},
			},
		},
		{
			Name:        "list_directory",
			Description: "List files and directories at the specified path",
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"path": {
						Type:        "string",
						Description: "The directory path to list",
					},
				},
				Required: []string{"path"},
			},
		},
		{
			Name:        "run_command",
			Description: "Execute a shell command in the working directory",
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"command": {
						Type:        "string",
						Description: "The shell command to execute",
					},
				},
				Required: []string{"command"},
			},
		},
		{
			Name:        "search_files",
			Description: "Search for files matching a glob pattern",
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"pattern": {
						Type:        "string",
						Description: "Glob pattern to match (e.g., '**/*.go', 'src/*.ts')",
					},
				},
				Required: []string{"pattern"},
			},
		},
		{
			Name:        "grep",
			Description: "Search for a pattern in files",
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"pattern": {
						Type:        "string",
						Description: "The regex pattern to search for",
					},
					"path": {
						Type:        "string",
						Description: "Directory or file to search in (default: current directory)",
					},
				},
				Required: []string{"pattern"},
			},
		},
	}
}

// ExecuteTool executes a tool and returns the result
func (e *ToolExecutor) ExecuteTool(name string, input json.RawMessage) (string, error) {
	var params map[string]any
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("unmarshal tool input: %w", err)
	}

	if e.OnToolCall != nil {
		e.OnToolCall(name, params)
	}

	var result string
	var err error

	switch name {
	case "read_file":
		result, err = e.readFile(params)
	case "write_file":
		result, err = e.writeFile(params)
	case "list_directory":
		result, err = e.listDirectory(params)
	case "run_command":
		result, err = e.runCommand(params)
	case "search_files":
		result, err = e.searchFiles(params)
	case "grep":
		result, err = e.grep(params)
	default:
		err = fmt.Errorf("unknown tool: %s", name)
	}

	isError := err != nil
	if isError {
		result = fmt.Sprintf("Error: %v", err)
	}

	if e.OnToolResult != nil {
		e.OnToolResult(name, result, isError)
	}

	if err != nil {
		return result, nil // Return error as result, not as Go error
	}
	return result, nil
}

func (e *ToolExecutor) resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(e.WorkDir, path)
}

func (e *ToolExecutor) isPathAllowed(path string) bool {
	absPath, err := filepath.Abs(e.resolvePath(path))
	if err != nil {
		return false
	}
	for _, allowed := range e.AllowedDirs {
		allowedAbs, err := filepath.Abs(allowed)
		if err != nil {
			continue
		}
		if strings.HasPrefix(absPath, allowedAbs) {
			return true
		}
	}
	return false
}

func (e *ToolExecutor) readFile(params map[string]any) (string, error) {
	path, ok := params["path"].(string)
	if !ok {
		return "", fmt.Errorf("path is required")
	}

	fullPath := e.resolvePath(path)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (e *ToolExecutor) writeFile(params map[string]any) (string, error) {
	path, ok := params["path"].(string)
	if !ok {
		return "", fmt.Errorf("path is required")
	}
	content, ok := params["content"].(string)
	if !ok {
		return "", fmt.Errorf("content is required")
	}

	fullPath := e.resolvePath(path)

	if !e.isPathAllowed(fullPath) {
		return "", fmt.Errorf("path not allowed: %s", path)
	}

	// Create directories if needed
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create directory: %w", err)
	}

	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return "", err
	}

	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), path), nil
}

func (e *ToolExecutor) listDirectory(params map[string]any) (string, error) {
	path, ok := params["path"].(string)
	if !ok {
		path = "."
	}

	fullPath := e.resolvePath(path)
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return "", err
	}

	var result strings.Builder
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if entry.IsDir() {
			result.WriteString(fmt.Sprintf("[DIR]  %s/\n", entry.Name()))
		} else {
			result.WriteString(fmt.Sprintf("[FILE] %s (%d bytes)\n", entry.Name(), info.Size()))
		}
	}

	return result.String(), nil
}

func (e *ToolExecutor) runCommand(params map[string]any) (string, error) {
	command, ok := params["command"].(string)
	if !ok {
		return "", fmt.Errorf("command is required")
	}

	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = e.WorkDir

	output, err := cmd.CombinedOutput()
	result := string(output)

	if err != nil {
		return fmt.Sprintf("%s\nExit code: %v", result, err), nil
	}

	return result, nil
}

func (e *ToolExecutor) searchFiles(params map[string]any) (string, error) {
	pattern, ok := params["pattern"].(string)
	if !ok {
		return "", fmt.Errorf("pattern is required")
	}

	var matches []string
	err := filepath.Walk(e.WorkDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			// Skip hidden and common ignored directories
			name := info.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, _ := filepath.Rel(e.WorkDir, path)
		matched, _ := filepath.Match(pattern, filepath.Base(path))
		if matched {
			matches = append(matches, relPath)
		}
		return nil
	})

	if err != nil {
		return "", err
	}

	if len(matches) == 0 {
		return "No files found matching pattern: " + pattern, nil
	}

	return strings.Join(matches, "\n"), nil
}

func (e *ToolExecutor) grep(params map[string]any) (string, error) {
	pattern, ok := params["pattern"].(string)
	if !ok {
		return "", fmt.Errorf("pattern is required")
	}

	path := "."
	if p, ok := params["path"].(string); ok {
		path = p
	}

	fullPath := e.resolvePath(path)

	// Use grep command for simplicity
	cmd := exec.Command("grep", "-rn", "--include=*", pattern, fullPath)
	output, _ := cmd.CombinedOutput()

	result := string(output)
	if result == "" {
		return "No matches found for pattern: " + pattern, nil
	}

	// Limit output size
	if len(result) > 10000 {
		result = result[:10000] + "\n... (truncated)"
	}

	return result, nil
}
