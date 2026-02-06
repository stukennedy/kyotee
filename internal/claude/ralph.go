package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RalphRunner implements the Ralph Wiggum pattern:
// Each iteration runs in a fresh context window.
// State persists on disk, not in conversation history.
// The model stays sharp because context never accumulates.
type RalphRunner struct {
	Client       *Client
	Executor     *ToolExecutor
	SystemPrompt string
	Tools        []Tool
	WorkDir      string
	StateFile    string

	// Callbacks
	OnIteration  func(iteration int, state *RalphState)
	OnMessage    func(content string)
	OnToolCall   func(name string, input any)
	OnToolResult func(name string, result string, isError bool)
	OnComplete   func(result string)

	// Limits
	MaxIterations int
	MaxToolCalls  int // per iteration
}

// RalphState is the persistent state between iterations
type RalphState struct {
	Task           string            `json:"task"`
	Phase          string            `json:"phase"`
	Iteration      int               `json:"iteration"`
	Status         string            `json:"status"` // running, completed, failed
	CompletedSteps []string          `json:"completed_steps"`
	CurrentStep    string            `json:"current_step"`
	LastOutput     string            `json:"last_output"`
	LastError      string            `json:"last_error,omitempty"`
	Context        map[string]string `json:"context"` // key-value context for next iteration
	StartedAt      time.Time         `json:"started_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// NewRalphRunner creates a new Ralph Wiggum pattern runner
func NewRalphRunner(client *Client, executor *ToolExecutor, workDir string, systemPrompt string) *RalphRunner {
	return &RalphRunner{
		Client:        client,
		Executor:      executor,
		SystemPrompt:  systemPrompt,
		Tools:         GetTools(),
		WorkDir:       workDir,
		StateFile:     filepath.Join(workDir, ".kyotee", "ralph-state.json"),
		MaxIterations: 100,
		MaxToolCalls:  20,
	}
}

// Run executes the task using the Ralph Wiggum pattern
func (r *RalphRunner) Run(ctx context.Context, task string) (string, error) {
	// Initialize or load state
	state, err := r.loadOrCreateState(task)
	if err != nil {
		return "", fmt.Errorf("failed to load state: %w", err)
	}

	// Main iteration loop - each iteration is a FRESH context
	for state.Status == "running" {
		select {
		case <-ctx.Done():
			state.Status = "cancelled"
			r.saveState(state)
			return "", ctx.Err()
		default:
		}

		state.Iteration++
		if state.Iteration > r.MaxIterations {
			state.Status = "failed"
			state.LastError = fmt.Sprintf("max iterations (%d) exceeded", r.MaxIterations)
			r.saveState(state)
			return "", fmt.Errorf(state.LastError)
		}

		if r.OnIteration != nil {
			r.OnIteration(state.Iteration, state)
		}

		// Run single iteration with fresh context
		completed, output, err := r.runSingleIteration(ctx, state)
		if err != nil {
			state.LastError = err.Error()
			// Don't fail immediately - let the next iteration try to recover
			// unless it's a critical error
			if state.Iteration > 3 && state.LastError == err.Error() {
				// Same error 3 times = give up
				state.Status = "failed"
				r.saveState(state)
				return "", err
			}
		}

		state.LastOutput = output
		state.UpdatedAt = time.Now()

		if completed {
			state.Status = "completed"
			r.saveState(state)
			if r.OnComplete != nil {
				r.OnComplete(output)
			}
			return output, nil
		}

		r.saveState(state)
	}

	return state.LastOutput, nil
}

// runSingleIteration runs one iteration with a completely fresh context
func (r *RalphRunner) runSingleIteration(ctx context.Context, state *RalphState) (completed bool, output string, err error) {
	// Build the prompt from current state - this is the ONLY context the model sees
	prompt := r.buildIterationPrompt(state)

	// Single user message - fresh context every time
	messages := []Message{
		{
			Role: "user",
			Content: []ContentBlock{
				{Type: "text", Text: prompt},
			},
		},
	}

	// Tool call loop within this iteration (limited)
	toolCalls := 0
	for {
		select {
		case <-ctx.Done():
			return false, "", ctx.Err()
		default:
		}

		req := &Request{
			System:   r.SystemPrompt,
			Messages: messages,
			Tools:    r.Tools,
		}

		resp, err := r.Client.Call(req)
		if err != nil {
			return false, "", fmt.Errorf("API call failed: %w", err)
		}

		// Add assistant response
		messages = append(messages, Message{
			Role:    "assistant",
			Content: resp.Content,
		})

		// Extract text output
		textContent := resp.GetTextContent()
		if textContent != "" && r.OnMessage != nil {
			r.OnMessage(textContent)
		}

		// No more tool calls = iteration complete
		if !resp.HasToolUse() {
			// Check if the model signaled completion
			completed := r.checkCompletion(textContent, state)
			return completed, textContent, nil
		}

		// Process tool calls
		toolCalls++
		if toolCalls > r.MaxToolCalls {
			// Too many tool calls in one iteration - force a fresh context
			state.Context["last_note"] = "Iteration ended due to tool call limit. Continue from current state."
			return false, textContent, nil
		}

		toolResults := []ContentBlock{}
		for _, toolUse := range resp.GetToolUses() {
			if r.OnToolCall != nil {
				r.OnToolCall(toolUse.Name, toolUse.Input)
			}

			inputJSON, _ := json.Marshal(toolUse.Input)
			result, err := r.Executor.ExecuteTool(toolUse.Name, inputJSON)

			isError := err != nil
			if isError {
				result = fmt.Sprintf("Error: %v", err)
			}

			if r.OnToolResult != nil {
				r.OnToolResult(toolUse.Name, result, isError)
			}

			// Update state context with important results (truncated)
			r.updateStateFromTool(state, toolUse.Name, result, isError)

			toolResults = append(toolResults, ContentBlock{
				Type:      "tool_result",
				ToolUseID: toolUse.ID,
				Content:   result,
			})
		}

		messages = append(messages, Message{
			Role:    "user",
			Content: toolResults,
		})
	}
}

// buildIterationPrompt constructs the prompt from disk state only
func (r *RalphRunner) buildIterationPrompt(state *RalphState) string {
	prompt := fmt.Sprintf(`# Current Task
%s

# Progress
- Iteration: %d
- Phase: %s
- Current Step: %s
- Completed Steps: %v

`, state.Task, state.Iteration, state.Phase, state.CurrentStep, state.CompletedSteps)

	if state.LastOutput != "" {
		// Include a summary, not the full output
		summary := state.LastOutput
		if len(summary) > 1000 {
			summary = summary[:1000] + "\n...(truncated)"
		}
		prompt += fmt.Sprintf("# Last Iteration Output (summary)\n%s\n\n", summary)
	}

	if state.LastError != "" {
		prompt += fmt.Sprintf("# Last Error (fix this)\n%s\n\n", state.LastError)
	}

	if len(state.Context) > 0 {
		prompt += "# Context from previous iterations\n"
		for k, v := range state.Context {
			// Truncate long values
			if len(v) > 500 {
				v = v[:500] + "..."
			}
			prompt += fmt.Sprintf("- %s: %s\n", k, v)
		}
		prompt += "\n"
	}

	prompt += `# Instructions
Continue the task from where you left off. Read files to understand current state if needed.
When the task is fully complete, include "TASK_COMPLETE" in your response.
If you need to update context for the next iteration, use write_file to update .kyotee/context.md.
Focus on making progress - do not repeat work already done.`

	return prompt
}

// checkCompletion determines if the task is complete
func (r *RalphRunner) checkCompletion(output string, state *RalphState) bool {
	// Explicit completion signal
	if contains(output, "TASK_COMPLETE") {
		return true
	}

	// Check for common completion phrases
	completionPhrases := []string{
		"implementation is complete",
		"all tasks completed",
		"successfully completed",
		"finished implementing",
	}
	for _, phrase := range completionPhrases {
		if containsIgnoreCase(output, phrase) {
			return true
		}
	}

	return false
}

// updateStateFromTool extracts important info from tool results
func (r *RalphRunner) updateStateFromTool(state *RalphState, toolName, result string, isError bool) {
	if state.Context == nil {
		state.Context = make(map[string]string)
	}

	// Track what operations were done
	switch toolName {
	case "write_file":
		state.Context["last_write"] = truncate(result, 200)
	case "bash":
		if isError {
			state.Context["last_bash_error"] = truncate(result, 300)
		} else if contains(result, "error") || contains(result, "Error") {
			state.Context["last_bash_warning"] = truncate(result, 300)
		}
	}
}

// loadOrCreateState loads existing state or creates new
func (r *RalphRunner) loadOrCreateState(task string) (*RalphState, error) {
	// Ensure .kyotee directory exists
	dir := filepath.Dir(r.StateFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	// Try to load existing state
	data, err := os.ReadFile(r.StateFile)
	if err == nil {
		var state RalphState
		if err := json.Unmarshal(data, &state); err == nil {
			// Check if it's the same task
			if state.Task == task && state.Status == "running" {
				return &state, nil
			}
		}
	}

	// Create new state
	state := &RalphState{
		Task:           task,
		Phase:          "execute",
		Iteration:      0,
		Status:         "running",
		CompletedSteps: []string{},
		Context:        make(map[string]string),
		StartedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	return state, r.saveState(state)
}

// saveState persists state to disk
func (r *RalphRunner) saveState(state *RalphState) error {
	state.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.StateFile, data, 0644)
}

// Helper functions
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func containsIgnoreCase(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
