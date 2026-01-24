package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/stukennedy/kyotee/internal/skills"
)

// DiscoveryMessage represents a conversation message
type DiscoveryMessage struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// Discovery handles the requirements gathering conversation
type Discovery struct {
	AgentDir      string
	RepoRoot      string
	History       []DiscoveryMessage
	Spec          map[string]any
	SkillRegistry *skills.Registry
	ActiveSkill   *skills.Skill
	BuildingSkill bool // True when building a new skill
	NewSkill      *skills.Skill
}

// NewDiscovery creates a new discovery session
func NewDiscovery(agentDir, repoRoot string) *Discovery {
	// Load skill registry
	skillsDir := filepath.Join(agentDir, "skills")
	registry, _ := skills.NewRegistry(skillsDir) // Ignore error, will work without skills

	return &Discovery{
		AgentDir:      agentDir,
		RepoRoot:      repoRoot,
		History:       []DiscoveryMessage{},
		SkillRegistry: registry,
	}
}

// LoadHistory loads previous conversation history into the discovery session
// This is used when resuming a conversation from .kyotee/conversation.json
func (d *Discovery) LoadHistory(messages []DiscoveryMessage) {
	d.History = messages
	// Try to match a skill from the loaded conversation
	if d.ActiveSkill == nil && d.SkillRegistry != nil {
		for _, msg := range messages {
			if msg.Role == "user" {
				d.tryMatchSkill(msg.Content)
				if d.ActiveSkill != nil {
					break // Found a match
				}
			}
		}
	}
}

// SendMessage sends a user message and gets a response
func (d *Discovery) SendMessage(userMsg string) (string, error) {
	// Add user message to history
	d.History = append(d.History, DiscoveryMessage{
		Role:    "user",
		Content: userMsg,
	})

	// Try to match a skill from user's message if we don't have one yet
	if d.ActiveSkill == nil && d.SkillRegistry != nil {
		d.tryMatchSkill(userMsg)
	}

	// Build the prompt
	prompt := d.buildPrompt()

	// Call claude
	response, err := d.callClaude(prompt)
	if err != nil {
		return "", err
	}

	// Add assistant response to history
	d.History = append(d.History, DiscoveryMessage{
		Role:    "assistant",
		Content: response,
	})

	// Check if spec is ready
	d.parseSpec(response)

	// Check for skill building commands
	d.parseSkillBuilding(response)

	return response, nil
}

// tryMatchSkill attempts to find a matching skill from user input
func (d *Discovery) tryMatchSkill(userMsg string) {
	// Extract potential tech stack terms
	terms := extractTechTerms(userMsg)
	if len(terms) == 0 {
		return
	}

	skill := d.SkillRegistry.Find(terms...)
	if skill != nil {
		d.ActiveSkill = skill
	}
}

// extractTechTerms pulls out technology-related words from text
func extractTechTerms(text string) []string {
	// Common tech keywords to look for
	keywords := []string{
		"go", "golang", "python", "rust", "typescript", "javascript", "node",
		"react", "vue", "angular", "svelte", "next", "nextjs", "nuxt",
		"gin", "echo", "fiber", "fastapi", "flask", "django", "express",
		"tailwind", "bootstrap", "css", "sass",
		"postgres", "mysql", "sqlite", "mongodb", "redis",
		"rest", "api", "graphql", "grpc",
		"cli", "web", "backend", "frontend",
	}

	textLower := strings.ToLower(text)
	var found []string

	for _, kw := range keywords {
		if strings.Contains(textLower, kw) {
			found = append(found, kw)
		}
	}

	return found
}

// IsSpecReady returns true if a spec has been generated
func (d *Discovery) IsSpecReady() bool {
	return d.Spec != nil
}

// GetSpec returns the parsed spec
func (d *Discovery) GetSpec() map[string]any {
	return d.Spec
}

// GetActiveSkill returns the matched skill
func (d *Discovery) GetActiveSkill() *skills.Skill {
	return d.ActiveSkill
}

// ListSkills returns available skills
func (d *Discovery) ListSkills() []*skills.Skill {
	if d.SkillRegistry == nil {
		return nil
	}
	return d.SkillRegistry.List()
}

func (d *Discovery) buildPrompt() string {
	// Load discovery prompt
	promptPath := filepath.Join(d.AgentDir, "prompts", "discovery.md")
	systemPrompt, err := os.ReadFile(promptPath)
	if err != nil {
		systemPrompt = []byte("You are Kyotee, an AI assistant helping users define software projects.")
	}

	// Build conversation history
	var historyParts []string
	for _, msg := range d.History {
		if msg.Role == "user" {
			historyParts = append(historyParts, "User: "+msg.Content)
		} else {
			historyParts = append(historyParts, "Assistant: "+msg.Content)
		}
	}

	// Get repo context
	repoContext := d.getRepoContext()

	// Get skill context
	skillContext := d.getSkillContext()

	// Get available skills list
	availableSkills := d.getAvailableSkillsList()

	parts := []string{
		string(systemPrompt),
		"",
		"## Available Tech Stack Skills",
		availableSkills,
		"",
	}

	if skillContext != "" {
		parts = append(parts, "## Active Skill (Use this for implementation guidance)", skillContext, "")
	}

	parts = append(parts,
		"## Repository Context",
		repoContext,
		"",
		"## Conversation",
		strings.Join(historyParts, "\n\n"),
		"",
		"Assistant:",
	)

	return strings.Join(parts, "\n")
}

func (d *Discovery) getAvailableSkillsList() string {
	if d.SkillRegistry == nil {
		return "No skills loaded. You can help the user build a new skill."
	}

	skillsList := d.SkillRegistry.List()
	if len(skillsList) == 0 {
		return "No skills available yet. Offer to learn a new tech stack with the user."
	}

	var lines []string
	lines = append(lines, "Available skills (mention if user's tech choice matches one):")
	for _, s := range skillsList {
		lines = append(lines, fmt.Sprintf("- **%s**: %s (tags: %s)", s.Name, s.Description, strings.Join(s.Tags, ", ")))
	}
	lines = append(lines, "")
	lines = append(lines, "If the user wants a tech stack not listed, offer to learn it together by:")
	lines = append(lines, "1. Asking about their preferences (project structure, patterns, testing, etc.)")
	lines = append(lines, "2. Fetching official documentation if they provide URLs")
	lines = append(lines, "3. Building a new skill file they can reuse")

	return strings.Join(lines, "\n")
}

func (d *Discovery) getSkillContext() string {
	if d.ActiveSkill == nil {
		return ""
	}
	return d.ActiveSkill.ToPromptContext()
}

func (d *Discovery) getRepoContext() string {
	var context strings.Builder

	// List top-level files
	entries, err := os.ReadDir(d.RepoRoot)
	if err == nil {
		context.WriteString("Files in repo:\n")
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), ".") {
				if e.IsDir() {
					context.WriteString(fmt.Sprintf("  %s/\n", e.Name()))
				} else {
					context.WriteString(fmt.Sprintf("  %s\n", e.Name()))
				}
			}
		}
	}

	// Check for common config files
	configFiles := []string{"package.json", "go.mod", "Cargo.toml", "pyproject.toml", "requirements.txt"}
	for _, cf := range configFiles {
		path := filepath.Join(d.RepoRoot, cf)
		if data, err := os.ReadFile(path); err == nil {
			context.WriteString(fmt.Sprintf("\n%s:\n```\n%s\n```\n", cf, truncate(string(data), 500)))
		}
	}

	return context.String()
}

func (d *Discovery) callClaude(prompt string) (string, error) {
	// Use --dangerously-skip-permissions so Claude can make edits if user requests
	// during the conversation (e.g., "can you fix this file?")
	cmd := exec.Command("claude", "-p", "--dangerously-skip-permissions")
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Dir = d.RepoRoot

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("claude error: %s", string(exitErr.Stderr))
		}
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}

var specJSONRe = regexp.MustCompile(`(?s)` + "```" + `json\s*(\{[^` + "`" + `]*"spec_ready"\s*:\s*true[^` + "`" + `]*\})\s*` + "```")
var skillJSONRe = regexp.MustCompile(`(?s)` + "```" + `json\s*(\{[^` + "`" + `]*"save_skill"\s*:\s*true[^` + "`" + `]*\})\s*` + "```")

func (d *Discovery) parseSpec(response string) {
	// Look for JSON spec block
	matches := specJSONRe.FindStringSubmatch(response)
	if len(matches) < 2 {
		return
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(matches[1]), &parsed); err != nil {
		return
	}

	// Check if spec_ready is true
	if ready, ok := parsed["spec_ready"].(bool); ok && ready {
		if spec, ok := parsed["spec"].(map[string]any); ok {
			d.Spec = spec
		}
	}
}

func (d *Discovery) parseSkillBuilding(response string) {
	// Look for skill save block
	matches := skillJSONRe.FindStringSubmatch(response)
	if len(matches) < 2 {
		return
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(matches[1]), &parsed); err != nil {
		return
	}

	// Check if save_skill is true
	if save, ok := parsed["save_skill"].(bool); ok && save {
		if skillData, ok := parsed["skill"].(map[string]any); ok {
			skill := &skills.Skill{
				Name:        getString(skillData, "name"),
				Description: getString(skillData, "description"),
				Tags:        getStringSlice(skillData, "tags"),
			}
			if skill.Name != "" {
				// Save the skill
				skillsDir := filepath.Join(d.AgentDir, "skills")
				if err := skills.SaveSkill(skillsDir, skill, true); err == nil {
					d.SkillRegistry.Add(skill)
					d.ActiveSkill = skill
				}
			}
		}
	}
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getStringSlice(m map[string]any, key string) []string {
	if v, ok := m[key].([]any); ok {
		result := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
