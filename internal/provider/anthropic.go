package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Anthropic adapts the Anthropic Messages API to the Provider interface.
// v1 uses non-streaming HTTP; when Request.Stream is set the final text is
// delivered as a single synthetic delta.
type Anthropic struct {
	ModelName  string // registry name, e.g. "claude-sonnet"
	ModelID    string // vendor model id, e.g. "claude-sonnet-4-5"
	APIKey     string
	BaseURL    string // default https://api.anthropic.com/v1
	InUSD      float64
	OutUSD     float64
	MaxCtx     int
	DefMaxTok  int     // config default when Request.MaxTokens == 0
	DefTemp    float64 // config default when Request.Temperature == 0
	HTTPClient *http.Client
}

const anthropicVersion = "2023-06-01"

func (a *Anthropic) Name() string   { return a.ModelName }
func (a *Anthropic) Vendor() string { return "anthropic" }

func (a *Anthropic) Capabilities() Capabilities {
	maxCtx := a.MaxCtx
	if maxCtx == 0 {
		maxCtx = 200000
	}
	return Capabilities{Tools: true, Vision: true, Reasoning: true, MaxContext: maxCtx}
}

func (a *Anthropic) CostPer1M() (float64, float64) { return a.InUSD, a.OutUSD }

// effort → extended-thinking budget tokens. minimal disables thinking.
var anthropicThinkingBudget = map[string]int{
	"low":    2048,
	"medium": 8192,
	"high":   16384,
}

type anthropicMsg struct {
	Role    string           `json:"role"`
	Content []map[string]any `json:"content"`
}

func (a *Anthropic) Generate(ctx context.Context, req Request) (Response, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = a.DefMaxTok
	}
	if maxTokens == 0 {
		maxTokens = 4096
	}
	temperature := req.Temperature
	if temperature == 0 {
		temperature = a.DefTemp
	}

	body := map[string]any{
		"model":      a.ModelID,
		"max_tokens": maxTokens,
		"messages":   a.encodeMessages(req.Messages),
	}
	if req.System != "" {
		body["system"] = req.System
	}
	if temperature > 0 {
		body["temperature"] = temperature
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := t.Schema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": schema,
			})
		}
		body["tools"] = tools
		if req.ToolChoice == "none" {
			body["tool_choice"] = map[string]any{"type": "none"}
		}
	}
	// Extended thinking forbids temperature. When the caller set an explicit
	// temperature (e.g. the two-brain divergent/convergent split, where
	// sampling IS the mechanism — spec 05), temperature wins and thinking
	// stays off; otherwise the effort knob maps to a thinking budget.
	if budget, ok := anthropicThinkingBudget[req.ReasoningEffort]; ok && req.Temperature == 0 {
		mt, _ := body["max_tokens"].(int)
		if mt <= budget {
			body["max_tokens"] = budget + 4096
		}
		body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
		delete(body, "temperature")
	}

	raw, err := a.post(ctx, body)
	if err != nil {
		return Response{}, err
	}

	var apiResp struct {
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			Thinking  string          `json:"thinking"`
			Signature string          `json:"signature"`
			Data      string          `json:"data"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &apiResp); err != nil {
		return Response{}, fmt.Errorf("anthropic: decode response: %w", err)
	}

	resp := Response{StopReason: apiResp.StopReason}
	for _, c := range apiResp.Content {
		switch c.Type {
		case "text":
			resp.Content = append(resp.Content, Block{Type: "text", Text: c.Text})
		case "tool_use":
			input := map[string]any{}
			_ = json.Unmarshal(c.Input, &input)
			resp.Content = append(resp.Content, Block{Type: "tool_use", ToolCall: &ToolCall{
				ID: c.ID, Name: c.Name, Input: input,
			}})
		// Thinking blocks must be preserved: when extended thinking is on,
		// the API requires assistant tool_use turns echoed back in a tool
		// loop to retain their (signed) thinking blocks.
		case "thinking":
			resp.Content = append(resp.Content, Block{Type: "thinking", Text: c.Thinking, Signature: c.Signature})
		case "redacted_thinking":
			resp.Content = append(resp.Content, Block{Type: "redacted_thinking", Data: c.Data})
		}
	}
	resp.Usage = Usage{
		InputTokens:  apiResp.Usage.InputTokens,
		OutputTokens: apiResp.Usage.OutputTokens,
		CostUSD:      CostFor(a, apiResp.Usage.InputTokens, apiResp.Usage.OutputTokens),
	}
	if req.Stream != nil {
		req.Stream(Delta{Type: "text", Text: resp.Text()})
		req.Stream(Delta{Type: "done"})
	}
	return resp, nil
}

// encodeMessages maps provider-agnostic messages to the Messages API shape.
// Role "tool" becomes a user message carrying tool_result blocks.
func (a *Anthropic) encodeMessages(msgs []Message) []anthropicMsg {
	out := make([]anthropicMsg, 0, len(msgs))
	for _, m := range msgs {
		role := m.Role
		if role == "tool" {
			role = "user"
		}
		blocks := make([]map[string]any, 0, len(m.Content))
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				blocks = append(blocks, map[string]any{"type": "text", "text": b.Text})
			case "thinking":
				blocks = append(blocks, map[string]any{
					"type": "thinking", "thinking": b.Text, "signature": b.Signature,
				})
			case "redacted_thinking":
				blocks = append(blocks, map[string]any{"type": "redacted_thinking", "data": b.Data})
			case "tool_use":
				if b.ToolCall != nil {
					input := b.ToolCall.Input
					if input == nil {
						input = map[string]any{}
					}
					blocks = append(blocks, map[string]any{
						"type": "tool_use", "id": b.ToolCall.ID,
						"name": b.ToolCall.Name, "input": input,
					})
				}
			case "tool_result":
				if b.ToolResult != nil {
					blocks = append(blocks, map[string]any{
						"type":        "tool_result",
						"tool_use_id": b.ToolResult.CallID,
						"content":     b.ToolResult.Content,
						"is_error":    b.ToolResult.IsError,
					})
				}
			}
		}
		out = append(out, anthropicMsg{Role: role, Content: blocks})
	}
	return out
}

func (a *Anthropic) post(ctx context.Context, body map[string]any) ([]byte, error) {
	baseURL := a.BaseURL
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	}
	client := a.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.APIKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, truncate(string(raw), 500))
	}
	return raw, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
