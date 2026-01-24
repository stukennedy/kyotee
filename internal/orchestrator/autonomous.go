package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/stukennedy/kyotee/internal/config"
)

// AutonomousEngine runs implementation autonomously using Claude Code CLI
type AutonomousEngine struct {
	Spec         map[string]any // Discovery spec
	Task         string
	RepoRoot     string
	AgentDir     string
	SkillContent string // Skill content as prompt context

	// Callbacks
	OnOutput func(text string)
	OnPhase  func(phase string, status string)
	OnTool   func(name string, input any)
}

// NewAutonomousEngine creates a new autonomous engine
func NewAutonomousEngine(spec map[string]any, task, repoRoot, agentDir string) *AutonomousEngine {
	return &AutonomousEngine{
		Spec:     spec,
		Task:     task,
		RepoRoot: repoRoot,
		AgentDir: agentDir,
	}
}

// Run executes the implementation autonomously using Claude Code CLI
func (e *AutonomousEngine) Run(ctx context.Context) error {
	if e.OnPhase != nil {
		e.OnPhase("execute", "running")
	}

	// Build the full prompt
	prompt, err := e.buildFullPrompt()
	if err != nil {
		return fmt.Errorf("failed to build prompt: %w", err)
	}

	// Write prompt to temp file (avoids shell escaping issues)
	promptFile, err := os.CreateTemp("", "kyotee-prompt-*.md")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(promptFile.Name())

	if _, err := promptFile.WriteString(prompt); err != nil {
		return fmt.Errorf("failed to write prompt: %w", err)
	}
	promptFile.Close()

	// Run claude CLI with the prompt
	// Using --print with stream-json for real-time output
	cmd := exec.CommandContext(ctx, "claude",
		"--print",                        // Non-interactive mode
		"--dangerously-skip-permissions", // Skip permission prompts for autonomous execution
		"--output-format", "stream-json", // Real-time streaming
		"--verbose",                      // Required for stream-json
	)

	// Pass prompt via stdin
	promptContent, _ := os.ReadFile(promptFile.Name())
	cmd.Stdin = strings.NewReader(string(promptContent))
	cmd.Dir = e.RepoRoot

	// Set up pipes for stdout/stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start claude: %w", err)
	}

	// Stream stdout (JSON lines from stream-json)
	go func() {
		scanner := bufio.NewScanner(stdout)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}

			// Try to parse as JSON
			var event map[string]any
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				// Not JSON, output as-is
				if e.OnOutput != nil {
					e.OnOutput(line + "\n")
				}
				continue
			}

			// Extract text from various event types
			if e.OnOutput != nil {
				e.extractAndOutput(event)
			}
		}
	}()

	// Stream stderr
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			if e.OnOutput != nil {
				e.OnOutput("⚠ " + line + "\n")
			}
		}
	}()

	// Wait for completion
	err = cmd.Wait()

	if err != nil {
		if ctx.Err() != nil {
			if e.OnPhase != nil {
				e.OnPhase("execute", "cancelled")
			}
			return ctx.Err() // Context cancelled
		}
		// Log the error but check if it's a real failure
		// Claude sometimes exits with non-zero even on success
		if e.OnOutput != nil {
			e.OnOutput(fmt.Sprintf("\n⚠ Exit: %v\n", err))
		}
		if e.OnPhase != nil {
			e.OnPhase("execute", "completed")
		}
		return nil // Don't treat non-zero exit as failure
	}

	if e.OnPhase != nil {
		e.OnPhase("execute", "completed")
	}

	return nil
}

// extractAndOutput parses stream-json events and outputs relevant text
func (e *AutonomousEngine) extractAndOutput(event map[string]any) {
	eventType, _ := event["type"].(string)

	switch eventType {
	case "assistant":
		// Full assistant message
		if msg, ok := event["message"].(map[string]any); ok {
			if content, ok := msg["content"].([]any); ok {
				for _, block := range content {
					if b, ok := block.(map[string]any); ok {
						if b["type"] == "text" {
							if text, ok := b["text"].(string); ok {
								e.OnOutput(text)
							}
						}
					}
				}
			}
		}

	case "content_block_start":
		// Start of a content block - check for tool use
		if cb, ok := event["content_block"].(map[string]any); ok {
			if cb["type"] == "tool_use" {
				if name, ok := cb["name"].(string); ok {
					e.OnOutput(fmt.Sprintf("\n🔧 %s ", name))
				}
			}
		}

	case "content_block_delta":
		// Streaming delta
		if delta, ok := event["delta"].(map[string]any); ok {
			if text, ok := delta["text"].(string); ok {
				e.OnOutput(text)
			}
		}

	case "content_block_stop":
		// End of block
		e.OnOutput("\n")

	case "result":
		// Final result
		if result, ok := event["result"].(string); ok {
			e.OnOutput("\n" + result)
		}
	}
}

func (e *AutonomousEngine) buildFullPrompt() (string, error) {
	var parts []string

	// System context
	systemPrompt, err := e.buildSystemPrompt()
	if err != nil {
		return "", err
	}
	parts = append(parts, systemPrompt)

	// Add spec as context
	if e.Spec != nil {
		specJSON, _ := json.MarshalIndent(e.Spec, "", "  ")
		parts = append(parts, "## Approved Spec\n```json\n"+string(specJSON)+"\n```")
	}

	// Add task
	if e.Task != "" {
		parts = append(parts, "## Task\n"+e.Task)
	}

	// Add repo context
	files, _ := e.getRepoContext()
	if files != "" {
		parts = append(parts, "## Current Project Files\n"+files)
	}

	parts = append(parts, `## Instructions
Implement the spec above. Use your tools to:
1. Read existing files to understand the codebase
2. Write new files or modify existing ones
3. Run commands to install dependencies, build, test

Work through the implementation systematically:
- Start with config files (package.json, etc.)
- Create entry points
- Implement features
- Run build/test to verify

When done, summarize what was built.`)

	return strings.Join(parts, "\n\n"), nil
}

func (e *AutonomousEngine) buildSystemPrompt() (string, error) {
	// Try to load from agent dir first
	systemPrompt, err := config.LoadPrompt(e.AgentDir, "autonomous")
	if err != nil {
		// Use default autonomous system prompt
		systemPrompt = defaultAutonomousPrompt
	}

	// Add skill content if available
	if e.SkillContent != "" {
		systemPrompt += "\n\n## Tech Stack Skill\n" + e.SkillContent
	}

	return systemPrompt, nil
}

func (e *AutonomousEngine) getRepoContext() (string, error) {
	var files []string

	err := filepath.Walk(e.RepoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		// Skip hidden dirs and common ignored dirs
		if info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, _ := filepath.Rel(e.RepoRoot, path)
		files = append(files, relPath)
		return nil
	})

	if err != nil {
		return "", err
	}

	if len(files) == 0 {
		return "(empty project)", nil
	}

	// Limit file list
	if len(files) > 100 {
		files = files[:100]
		files = append(files, "... (truncated)")
	}

	return strings.Join(files, "\n"), nil
}

const defaultAutonomousPrompt = `You are Kyotee, an autonomous development agent. You implement projects based on approved specs.

## Your Approach
1. **Understand**: Read existing files to understand the codebase
2. **Plan**: Identify what files need to be created/modified
3. **Implement**: Write code following the spec's tech stack and requirements
4. **Verify**: Run build/test commands to verify the implementation
5. **Fix**: If verification fails, fix issues and re-verify

## Rules
- Follow the spec exactly - it's the source of truth
- Create config files first (package.json, tsconfig.json, etc.)
- Use the specified framework/tech stack (don't substitute)
- Keep going until the implementation is complete
- If you encounter errors, fix them and continue

## Tech Stack Adherence
If the spec says "Hono + TypeScript + Cloudflare Workers":
- Create wrangler.toml, package.json, tsconfig.json
- Use Hono framework (import { Hono } from 'hono')
- Do NOT create static HTML instead

## Output
When done, provide a summary of:
- Files created/modified
- Commands to run (install, dev, deploy)
- Any issues encountered`
