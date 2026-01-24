package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// DiscoveryMessage represents a conversation message
type DiscoveryMessage struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// Discovery handles the requirements gathering conversation
type Discovery struct {
	AgentDir string
	RepoRoot string
	History  []DiscoveryMessage
	Spec     map[string]any
}

// NewDiscovery creates a new discovery session
func NewDiscovery(agentDir, repoRoot string) *Discovery {
	return &Discovery{
		AgentDir: agentDir,
		RepoRoot: repoRoot,
		History:  []DiscoveryMessage{},
	}
}

// SendMessage sends a user message and gets a response
func (d *Discovery) SendMessage(userMsg string) (string, error) {
	// Add user message to history
	d.History = append(d.History, DiscoveryMessage{
		Role:    "user",
		Content: userMsg,
	})

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

	return response, nil
}

// IsSpecReady returns true if a spec has been generated
func (d *Discovery) IsSpecReady() bool {
	return d.Spec != nil
}

// GetSpec returns the parsed spec
func (d *Discovery) GetSpec() map[string]any {
	return d.Spec
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

	parts := []string{
		string(systemPrompt),
		"",
		"## Repository Context",
		repoContext,
		"",
		"## Conversation",
		strings.Join(historyParts, "\n\n"),
		"",
		"Assistant:",
	}

	return strings.Join(parts, "\n")
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
	cmd := exec.Command("claude", "-p")
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

var specJSONRe = regexp.MustCompile(`(?s)\x60\x60\x60json\s*(\{[^` + "`" + `]*"spec_ready"\s*:\s*true[^` + "`" + `]*\})\s*\x60\x60\x60`)

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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
