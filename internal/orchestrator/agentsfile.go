package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stukennedy/kyotee/internal/skills"
)

// GenerateAgentsFile produces a comprehensive AGENTS.md and saves it to .kyotee/AGENTS.md
// This single file becomes the primary context for ALL subsequent Claude calls.
func GenerateAgentsFile(spec map[string]any, skill *skills.Skill, repoRoot string) (string, error) {
	var sections []string

	sections = append(sections, "# AGENTS.md — Project Context\n\nThis file is the SINGLE source of truth for this project. Every instruction, pattern, and convention is here. Do not search for additional context — everything you need is in this document.")

	// Section A: Project Spec
	sections = append(sections, buildSpecSection(spec))

	// Section B: Tech Stack Patterns
	if skill != nil {
		sections = append(sections, buildTechStackSection(skill))
	}

	// Section C: Coding Conventions
	if skill != nil {
		sections = append(sections, buildConventionsSection(skill))
	}

	// Section D: Execution Rules
	sections = append(sections, buildExecutionRules())

	// Section E: Project Structure
	sections = append(sections, buildProjectStructure(repoRoot))

	// Section F: API Reference (from skill)
	if skill != nil {
		sections = append(sections, buildAPIReference(skill))
	}

	content := strings.Join(sections, "\n\n---\n\n")

	// Save to .kyotee/AGENTS.md
	kyoteeDir := filepath.Join(repoRoot, ".kyotee")
	if err := os.MkdirAll(kyoteeDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create .kyotee dir: %w", err)
	}
	agentsPath := filepath.Join(kyoteeDir, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to write AGENTS.md: %w", err)
	}

	return content, nil
}

// LoadAgentsFile reads the generated AGENTS.md from a project directory
func LoadAgentsFile(repoRoot string) (string, error) {
	agentsPath := filepath.Join(repoRoot, ".kyotee", "AGENTS.md")
	data, err := os.ReadFile(agentsPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// MatchSkillFromSpec deterministically matches a skill based on spec fields
func MatchSkillFromSpec(spec map[string]any, registry *skills.Registry) *skills.Skill {
	if registry == nil || spec == nil {
		return nil
	}

	var terms []string

	// Extract tech stack terms from spec fields
	for _, key := range []string{"language", "framework", "tech_stack", "stack", "runtime"} {
		if v, ok := spec[key].(string); ok && v != "" {
			// Split on common delimiters
			for _, part := range strings.FieldsFunc(v, func(r rune) bool {
				return r == '+' || r == ',' || r == '/' || r == ' '
			}) {
				part = strings.TrimSpace(part)
				if part != "" {
					terms = append(terms, strings.ToLower(part))
				}
			}
		}
	}

	// Also check tags if present
	if tags, ok := spec["tags"].([]any); ok {
		for _, t := range tags {
			if s, ok := t.(string); ok {
				terms = append(terms, strings.ToLower(s))
			}
		}
	}

	if len(terms) == 0 {
		return nil
	}

	return registry.Find(terms...)
}

func buildSpecSection(spec map[string]any) string {
	var b strings.Builder
	b.WriteString("## Project Spec\n\n")

	if goal, ok := spec["goal"].(string); ok {
		b.WriteString(fmt.Sprintf("**Goal:** %s\n\n", goal))
	}
	if name, ok := spec["project_name"].(string); ok && name != "" {
		b.WriteString(fmt.Sprintf("**Project Name:** %s\n\n", name))
	}
	if lang, ok := spec["language"].(string); ok && lang != "" {
		b.WriteString(fmt.Sprintf("**Language:** %s\n\n", lang))
	}
	if fw, ok := spec["framework"].(string); ok && fw != "" {
		b.WriteString(fmt.Sprintf("**Framework:** %s\n\n", fw))
	}

	if features, ok := spec["features"].([]any); ok && len(features) > 0 {
		b.WriteString("**Features:**\n")
		for _, f := range features {
			if fs, ok := f.(string); ok {
				b.WriteString(fmt.Sprintf("- %s\n", fs))
			}
		}
		b.WriteString("\n")
	}

	if reqs, ok := spec["requirements"].([]any); ok && len(reqs) > 0 {
		b.WriteString("**Requirements:**\n")
		for _, r := range reqs {
			if rs, ok := r.(string); ok {
				b.WriteString(fmt.Sprintf("- %s\n", rs))
			}
		}
		b.WriteString("\n")
	}

	// Dump full spec as JSON for completeness
	specJSON, _ := json.MarshalIndent(spec, "", "  ")
	b.WriteString("**Full Spec:**\n```json\n")
	b.WriteString(string(specJSON))
	b.WriteString("\n```\n")

	return b.String()
}

func buildTechStackSection(skill *skills.Skill) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("## Tech Stack: %s\n\n", skill.Name))
	b.WriteString(skill.Description + "\n\n")

	if len(skill.Patterns) > 0 {
		b.WriteString("### Patterns\n\n")
		for name, pattern := range skill.Patterns {
			b.WriteString(fmt.Sprintf("**%s:**\n```\n%s\n```\n\n", name, pattern))
		}
	}

	if skill.Docs.Fetched != "" {
		b.WriteString("### Reference Documentation\n\n")
		b.WriteString(skill.Docs.Fetched)
		b.WriteString("\n\n")
	}

	if len(skill.Dependencies) > 0 {
		b.WriteString(fmt.Sprintf("### Dependencies\n\n%s\n\n", strings.Join(skill.Dependencies, ", ")))
	}

	if len(skill.Preferences) > 0 {
		b.WriteString("### Preferences\n\n")
		for key, val := range skill.Preferences {
			b.WriteString(fmt.Sprintf("- **%s:** %s\n", key, val))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func buildConventionsSection(skill *skills.Skill) string {
	var b strings.Builder
	b.WriteString("## Coding Conventions\n\n")

	if skill.Conventions.ProjectStructure != "" {
		b.WriteString("### Project Structure\n```\n")
		b.WriteString(skill.Conventions.ProjectStructure)
		b.WriteString("\n```\n\n")
	}

	if skill.Conventions.Naming != "" {
		b.WriteString(fmt.Sprintf("### Naming: %s\n\n", skill.Conventions.Naming))
	}

	if skill.Conventions.FileExtensions != "" {
		b.WriteString(fmt.Sprintf("### File Extensions: %s\n\n", skill.Conventions.FileExtensions))
	}

	return b.String()
}

func buildExecutionRules() string {
	return `## Execution Rules

### The Spec Is Law
- The spec is the source of truth. Implement exactly what it says.
- Use the specified framework and tech stack. Do NOT substitute.
- If something is ambiguous, make a reasonable choice and note it.

### No Stubs. No Placeholders. No Shortcuts.
Every piece of code must be COMPLETE and FUNCTIONAL.

Banned patterns:
- ` + "`return null`" + ` / ` + "`return {}`" + ` / ` + "`return []`" + ` as placeholders
- ` + "`// TODO: implement`" + ` without the implementation
- ` + "`console.log('handler called')`" + ` as an entire function body
- Empty catch blocks: ` + "`catch (e) {}`" + `
- Empty event handlers that do nothing
- ` + "`pass`" + ` as a Python function body (without real logic)
- Placeholder UI text: "Lorem ipsum", "TODO: add content"
- Functions that return hardcoded sample data instead of real logic

If you genuinely cannot implement something (needs an external API key, depends on a service that doesn't exist yet), throw/return a descriptive error — never a silent no-op.

### Error Handling Is Not Optional
- Every function that can fail MUST handle errors explicitly
- HTTP handlers MUST return proper status codes (400, 404, 500 — not 200 for everything)
- Database operations MUST handle connection failures and query errors
- File operations MUST handle missing files and permission errors
- User input MUST be validated before processing
- Never swallow errors silently — log or propagate them

### Security by Default
- Sanitize user input that touches HTML, SQL, or shell commands
- Use parameterized queries (never string concatenation for SQL)
- Don't hardcode secrets — use environment variables
- Validate file paths to prevent directory traversal

### Deviation Rules
**AUTO-FIX (just do it):**
- Missing imports, wrong import paths, typos
- Type errors with obvious fixes
- Missing error handling, input validation
- Missing dependencies (install them)
- Missing directories (create them)

**ADD AUTOMATICALLY (not in spec but critical):**
- Error handling on all fallible operations
- Input validation on public-facing functions
- Proper HTTP status codes and error responses
- Graceful shutdown for servers
- Logging for important operations

**STOP AND ASK (architectural changes beyond spec):**
- Adding database tables or schemas not in the spec
- Switching frameworks or major libraries
- Changing the API contract (routes, response shapes)
- Adding authentication/authorization not specified

### Commit Protocol
- Stage files individually: never ` + "`git add .`" + `
- Use conventional commit format: ` + "`feat(scope): short description`" + `
- Each logical task = one commit
- Commit message describes WHAT changed, not HOW

### Verification
After implementing each feature:
1. Run the build command
2. Run tests if they exist
3. Run lint if configured
4. Read the output. Fix errors before moving on.
5. Do NOT mark something as done if it doesn't build.

### File Change Discipline
- Always read a file before modifying it
- Follow existing code style
- Create directories before writing files (mkdir -p)
- Write complete files — no half-implemented modules`
}

func buildProjectStructure(repoRoot string) string {
	var b strings.Builder
	b.WriteString("## Project Structure\n\n")

	var files []string
	err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		relPath, _ := filepath.Rel(repoRoot, path)
		files = append(files, relPath)
		return nil
	})

	if err != nil || len(files) == 0 {
		b.WriteString("(empty project — starting fresh)\n")
		return b.String()
	}

	if len(files) > 100 {
		files = files[:100]
		files = append(files, "... (truncated)")
	}

	b.WriteString("```\n")
	b.WriteString(strings.Join(files, "\n"))
	b.WriteString("\n```\n")

	return b.String()
}

func buildAPIReference(skill *skills.Skill) string {
	var b strings.Builder
	b.WriteString("## API Reference\n\n")

	// The API reference comes from the skill's patterns and docs
	// If the skill has fetched docs, those are already in the tech stack section
	// Here we provide a compact index of key patterns

	if len(skill.Patterns) > 0 {
		b.WriteString("Key patterns available in this stack (see Tech Stack section for full code):\n\n")
		for name := range skill.Patterns {
			b.WriteString(fmt.Sprintf("- **%s**\n", name))
		}
		b.WriteString("\n")
	}

	if len(skill.Docs.URLs) > 0 {
		b.WriteString("### Reference URLs\n")
		for _, url := range skill.Docs.URLs {
			b.WriteString(fmt.Sprintf("- %s\n", url))
		}
	}

	return b.String()
}
