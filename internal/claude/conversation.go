package claude

import (
	"context"
	"encoding/json"
	"fmt"
)

// Conversation manages an autonomous conversation with Claude
type Conversation struct {
	Client       *Client
	Executor     *ToolExecutor
	SystemPrompt string
	Messages     []Message
	Tools        []Tool

	// Callbacks
	OnMessage    func(role string, content string)
	OnToolCall   func(name string, input any)
	OnToolResult func(name string, result string, isError bool)
	OnComplete   func(response string)

	// Limits
	MaxIterations int
}

// NewConversation creates a new autonomous conversation
func NewConversation(client *Client, executor *ToolExecutor, systemPrompt string) *Conversation {
	return &Conversation{
		Client:        client,
		Executor:      executor,
		SystemPrompt:  systemPrompt,
		Messages:      []Message{},
		Tools:         GetTools(),
		MaxIterations: 50,
	}
}

// Run executes the conversation with the given user message
// It automatically handles tool calls until Claude stops calling tools
func (c *Conversation) Run(ctx context.Context, userMessage string) (string, error) {
	// Add user message
	c.Messages = append(c.Messages, Message{
		Role: "user",
		Content: []ContentBlock{
			{Type: "text", Text: userMessage},
		},
	})

	iterations := 0
	for {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		// Check iteration limit
		iterations++
		if iterations > c.MaxIterations {
			return "", fmt.Errorf("max iterations (%d) exceeded", c.MaxIterations)
		}

		// Make API call
		req := &Request{
			System:   c.SystemPrompt,
			Messages: c.Messages,
			Tools:    c.Tools,
		}

		resp, err := c.Client.Call(req)
		if err != nil {
			return "", fmt.Errorf("API call failed: %w", err)
		}

		// Add assistant response to messages
		c.Messages = append(c.Messages, Message{
			Role:    "assistant",
			Content: resp.Content,
		})

		// Check if we have text content to report
		if textContent := resp.GetTextContent(); textContent != "" && c.OnMessage != nil {
			c.OnMessage("assistant", textContent)
		}

		// If no tool use, we're done
		if !resp.HasToolUse() {
			finalText := resp.GetTextContent()
			if c.OnComplete != nil {
				c.OnComplete(finalText)
			}
			return finalText, nil
		}

		// Process tool calls
		toolResults := []ContentBlock{}
		for _, toolUse := range resp.GetToolUses() {
			// Notify about tool call
			if c.OnToolCall != nil {
				c.OnToolCall(toolUse.Name, toolUse.Input)
			}

			// Execute tool
			inputJSON, _ := json.Marshal(toolUse.Input)
			result, err := c.Executor.ExecuteTool(toolUse.Name, inputJSON)

			isError := err != nil
			if isError {
				result = fmt.Sprintf("Error: %v", err)
			}

			// Notify about tool result
			if c.OnToolResult != nil {
				c.OnToolResult(toolUse.Name, result, isError)
			}

			// Add tool result
			toolResults = append(toolResults, ContentBlock{
				Type:      "tool_result",
				ToolUseID: toolUse.ID,
				Content:   result,
			})
		}

		// Add tool results as user message
		c.Messages = append(c.Messages, Message{
			Role:    "user",
			Content: toolResults,
		})
	}
}

// AddMessage adds a message to the conversation history
func (c *Conversation) AddMessage(role string, content string) {
	c.Messages = append(c.Messages, Message{
		Role: role,
		Content: []ContentBlock{
			{Type: "text", Text: content},
		},
	})
}

// Reset clears the conversation history
func (c *Conversation) Reset() {
	c.Messages = []Message{}
}

// AutonomousRunner runs phases autonomously without user interaction
type AutonomousRunner struct {
	Client   *Client
	WorkDir  string
	OnOutput func(phase string, text string)
	OnPhase  func(phase string, status string)
}

// NewAutonomousRunner creates a new autonomous runner
func NewAutonomousRunner(workDir string) (*AutonomousRunner, error) {
	client, err := NewClient()
	if err != nil {
		return nil, err
	}

	return &AutonomousRunner{
		Client:  client,
		WorkDir: workDir,
	}, nil
}

// RunPhase executes a single phase autonomously
func (r *AutonomousRunner) RunPhase(ctx context.Context, phaseName string, systemPrompt string, userPrompt string) (string, error) {
	if r.OnPhase != nil {
		r.OnPhase(phaseName, "running")
	}

	executor := NewToolExecutor(r.WorkDir)
	executor.OnToolCall = func(name string, input any) {
		if r.OnOutput != nil {
			inputJSON, _ := json.MarshalIndent(input, "", "  ")
			r.OnOutput(phaseName, fmt.Sprintf("→ Tool: %s\n%s", name, string(inputJSON)))
		}
	}
	executor.OnToolResult = func(name string, result string, isError bool) {
		if r.OnOutput != nil {
			status := "✓"
			if isError {
				status = "✗"
			}
			// Truncate long results
			if len(result) > 500 {
				result = result[:500] + "..."
			}
			r.OnOutput(phaseName, fmt.Sprintf("%s %s: %s", status, name, result))
		}
	}

	conv := NewConversation(r.Client, executor, systemPrompt)
	conv.OnMessage = func(role string, content string) {
		if r.OnOutput != nil && role == "assistant" {
			r.OnOutput(phaseName, content)
		}
	}

	result, err := conv.Run(ctx, userPrompt)

	if err != nil {
		if r.OnPhase != nil {
			r.OnPhase(phaseName, "failed")
		}
		return "", err
	}

	if r.OnPhase != nil {
		r.OnPhase(phaseName, "passed")
	}

	return result, nil
}
