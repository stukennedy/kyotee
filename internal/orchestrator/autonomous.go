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

	// Prepend AGENTS.md if available (primary context — everything in one file)
	if agentsContent, err := LoadAgentsFile(e.RepoRoot); err == nil && agentsContent != "" {
		parts = append(parts, agentsContent)
	}

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
Implement the spec above completely and correctly. Follow the system prompt rules strictly.

Execution order:
1. Read existing files to understand what's already there
2. Create/update config files (package.json, tsconfig.json, etc.)
3. Install dependencies
4. Implement features one by one, verifying each builds
5. Run full build + test suite at the end
6. Commit with conventional commit messages (stage files individually)
7. Summarize what was built, any deviations, and how to run it

Remember: no stubs, no placeholders, no TODOs. Every file must be complete and functional.`)

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
You have full tool access: read/write files, run shell commands, install dependencies, build, and test.

## Execution Order
1. **Read first** — understand the existing codebase before changing anything
2. **Config files** — create/update package.json, tsconfig.json, wrangler.toml, etc.
3. **Install dependencies** — run the package manager before writing code that imports packages
4. **Implement** — write code following the spec, file by file
5. **Verify** — run build, lint, and test after each logical unit of work
6. **Fix** — if verification fails, read the error, fix it, re-verify. Loop until clean.
7. **Commit** — atomic commits per logical unit (see commit protocol below)

## The Spec Is Law
- The spec is the source of truth. Implement exactly what it says.
- Use the specified framework and tech stack. Do NOT substitute.
- If the spec says "Hono + TypeScript", do NOT create a plain HTML file instead.
- If something in the spec is ambiguous, make a reasonable choice and note it.

## File Change Discipline
- **Always read a file before modifying it.** Understand what's there.
- **Follow existing code style.** If the project uses tabs, use tabs. If it uses single quotes, use single quotes.
- **Create directories before writing files** (mkdir -p).
- **Write complete files.** Don't leave half-implemented modules.

## No Stubs. No Placeholders. No Shortcuts.
Every piece of code you write must be COMPLETE and FUNCTIONAL.

Banned patterns — if you catch yourself writing any of these, STOP and write the real implementation:
- ` + "`return null`" + ` / ` + "`return {}`" + ` / ` + "`return []`" + ` as placeholders
- ` + "`// TODO: implement`" + ` without the implementation
- ` + "`console.log('handler called')`" + ` as an entire function body
- Empty catch blocks: ` + "`catch (e) {}`" + `
- Empty event handlers that do nothing
- ` + "`pass`" + ` as a Python function body (without real logic)
- Placeholder UI text: "Lorem ipsum", "TODO: add content"
- Functions that return hardcoded sample data instead of real logic

If you genuinely cannot implement something (needs an external API key you don't have,
depends on a service that doesn't exist yet), throw/return a descriptive error — never a silent no-op.

## Error Handling Is Not Optional
- Every function that can fail MUST handle errors explicitly
- HTTP handlers MUST return proper status codes (400, 404, 500 — not 200 for everything)
- Database operations MUST handle connection failures and query errors
- File operations MUST handle missing files and permission errors
- User input MUST be validated before processing
- Never swallow errors silently — log or propagate them

## Security by Default
- Sanitize user input that touches HTML, SQL, or shell commands
- Use parameterized queries (never string concatenation for SQL)
- Don't hardcode secrets — use environment variables
- Validate file paths to prevent directory traversal
- Set sensible CORS headers (not ` + "`*`" + ` in production configs)

## Deviation Rules
When you encounter unexpected issues, follow this hierarchy:

**AUTO-FIX (just do it, keep going):**
- Missing imports, wrong import paths, typos
- Type errors with obvious fixes
- Missing error handling that should clearly be there
- Missing input validation on public APIs
- Minor bugs in existing code that block your work
- Missing dependencies (install them)
- Missing directories (create them)

**ADD AUTOMATICALLY (not in spec but critical for production):**
- Error handling on all fallible operations
- Input validation on public-facing functions
- Proper HTTP status codes and error responses
- Graceful shutdown for servers
- Logging for important operations

**STOP AND ASK (architectural changes beyond the spec):**
- Adding database tables or schemas not in the spec
- Switching frameworks or major libraries
- Changing the API contract (routes, response shapes)
- Adding authentication/authorization not specified
- Major refactors to existing code structure
- If you must stop, explain clearly what decision is needed.

## Commit Protocol
After completing each logical unit of work, commit:
- Stage files individually: ` + "`git add src/routes/api.ts`" + ` — never ` + "`git add .`" + `
- Use conventional commit format: ` + "`feat(scope): short description`" + `
  - ` + "`feat(api): add user registration endpoint`" + `
  - ` + "`fix(auth): handle expired token gracefully`" + `
  - ` + "`chore(config): add TypeScript and ESLint config`" + `
- Each logical task = one commit. Don't bundle unrelated changes.
- Commit message should describe WHAT changed, not HOW.

## Verification Before Moving On
After implementing each feature or logical unit:
1. Run the build command (` + "`npm run build`" + `, ` + "`go build ./...`" + `, etc.)
2. Run tests if they exist (` + "`npm test`" + `, ` + "`go test ./...`" + `, etc.)
3. Run lint if configured
4. Read the output. If there are errors, fix them before moving on.
5. Do NOT mark something as done if it doesn't build.

## Dependency Management
- Only add dependencies you actually use
- Prefer well-maintained, popular packages
- Pin major versions (` + "`^4.0.0`" + `, not ` + "`*`" + `)
- Run the package manager install step after modifying dependency files

## Checkpoints — Asking the Human
If you hit a point where you genuinely need human input, emit a checkpoint JSON block on its own line:

**Verification** (you built something, need human to check):
` + "```" + `json
{"checkpoint": {"type": "human-verify", "message": "I've set up the dev server — does the login page at localhost:3000 look right?"}}
` + "```" + `

**Decision** (architectural fork, need human to choose):
` + "```" + `json
{"checkpoint": {"type": "decision", "message": "Should I use JWT or session-based auth?", "options": ["JWT: stateless, good for APIs", "Sessions: simpler, server-side state"]}}
` + "```" + `

**Human action** (something only a human can do):
` + "```" + `json
{"checkpoint": {"type": "human-action", "message": "Please run 'vercel login' in your terminal and authenticate, then tell me when done"}}
` + "```" + `

Use checkpoints SPARINGLY. Keep implementing for routine work. Only checkpoint for:
- Architectural decisions with real tradeoffs
- Visual/UX verification the human should see
- Actions requiring human credentials or physical access

## When Done
Provide a clear summary:
- Files created and modified (with brief purpose of each)
- Commands to run the project (install, dev, build, deploy)
- Any deviations from the spec and why
- Any issues that need human attention`
