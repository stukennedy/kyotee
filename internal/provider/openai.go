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

// OpenAICompat adapts any OpenAI-compatible chat-completions endpoint
// (OpenAI, Google's compat layer, xAI, Mistral, Ollama, vLLM, ...) to the
// Provider interface. This one adapter covers most non-Anthropic vendors,
// which is what makes cross-vendor councils practical in v1.
type OpenAICompat struct {
	ModelName  string // registry name
	ModelID    string // vendor model id, e.g. "gpt-5"
	VendorTag  string // "openai" | "google" | "local" | ...
	APIKey     string
	BaseURL    string // e.g. https://api.openai.com/v1
	InUSD      float64
	OutUSD     float64
	MaxCtx     int
	Reasoning  bool // model accepts reasoning_effort
	HTTPClient *http.Client
}

func (o *OpenAICompat) Name() string { return o.ModelName }

func (o *OpenAICompat) Vendor() string {
	if o.VendorTag == "" {
		return "openai"
	}
	return o.VendorTag
}

func (o *OpenAICompat) Capabilities() Capabilities {
	maxCtx := o.MaxCtx
	if maxCtx == 0 {
		maxCtx = 128000
	}
	return Capabilities{Tools: true, Reasoning: o.Reasoning, MaxContext: maxCtx}
}

func (o *OpenAICompat) CostPer1M() (float64, float64) { return o.InUSD, o.OutUSD }

func (o *OpenAICompat) Generate(ctx context.Context, req Request) (Response, error) {
	msgs := o.encodeMessages(req.System, req.Messages)
	body := map[string]any{
		"model":    o.ModelID,
		"messages": msgs,
	}
	if req.MaxTokens > 0 {
		body["max_completion_tokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}
	if o.Reasoning && req.ReasoningEffort != "" {
		// OpenAI accepts "minimal" | "low" | "medium" | "high" — 1:1 mapping.
		body["reasoning_effort"] = req.ReasoningEffort
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := t.Schema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  schema,
				},
			})
		}
		body["tools"] = tools
	}

	raw, err := o.post(ctx, body)
	if err != nil {
		return Response{}, err
	}

	var apiResp struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &apiResp); err != nil {
		return Response{}, fmt.Errorf("%s: decode response: %w", o.Vendor(), err)
	}
	if len(apiResp.Choices) == 0 {
		return Response{}, fmt.Errorf("%s: empty choices", o.Vendor())
	}

	choice := apiResp.Choices[0]
	resp := Response{StopReason: choice.FinishReason}
	if choice.Message.Content != "" {
		resp.Content = append(resp.Content, Block{Type: "text", Text: choice.Message.Content})
	}
	for _, tc := range choice.Message.ToolCalls {
		input := map[string]any{}
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
		resp.Content = append(resp.Content, Block{Type: "tool_use", ToolCall: &ToolCall{
			ID: tc.ID, Name: tc.Function.Name, Input: input,
		}})
	}
	resp.Usage = Usage{
		InputTokens:  apiResp.Usage.PromptTokens,
		OutputTokens: apiResp.Usage.CompletionTokens,
		CostUSD:      CostFor(o, apiResp.Usage.PromptTokens, apiResp.Usage.CompletionTokens),
	}
	if req.Stream != nil {
		req.Stream(Delta{Type: "text", Text: resp.Text()})
		req.Stream(Delta{Type: "done"})
	}
	return resp, nil
}

func (o *OpenAICompat) encodeMessages(system string, msgs []Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs)+1)
	if system != "" {
		out = append(out, map[string]any{"role": "system", "content": system})
	}
	for _, m := range msgs {
		switch m.Role {
		case "tool":
			for _, b := range m.Content {
				if b.Type == "tool_result" && b.ToolResult != nil {
					out = append(out, map[string]any{
						"role":         "tool",
						"tool_call_id": b.ToolResult.CallID,
						"content":      b.ToolResult.Content,
					})
				}
			}
		case "assistant":
			msg := map[string]any{"role": "assistant"}
			var toolCalls []map[string]any
			var text string
			for _, b := range m.Content {
				switch b.Type {
				case "text":
					text += b.Text
				case "tool_use":
					if b.ToolCall != nil {
						args, _ := json.Marshal(b.ToolCall.Input)
						toolCalls = append(toolCalls, map[string]any{
							"id":   b.ToolCall.ID,
							"type": "function",
							"function": map[string]any{
								"name":      b.ToolCall.Name,
								"arguments": string(args),
							},
						})
					}
				}
			}
			if text != "" || len(toolCalls) == 0 {
				msg["content"] = text
			}
			if len(toolCalls) > 0 {
				msg["tool_calls"] = toolCalls
			}
			out = append(out, msg)
		default: // user
			var text string
			for _, b := range m.Content {
				if b.Type == "text" {
					text += b.Text
				}
			}
			out = append(out, map[string]any{"role": "user", "content": text})
		}
	}
	return out
}

func (o *OpenAICompat) post(ctx context.Context, body map[string]any) ([]byte, error) {
	baseURL := o.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	client := o.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", o.Vendor(), err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: status %d: %s", o.Vendor(), resp.StatusCode, truncate(string(raw), 500))
	}
	return raw, nil
}

// OpenAIEmbedder implements the council's Embedder interface against an
// OpenAI-compatible /embeddings endpoint.
type OpenAIEmbedder struct {
	ModelID    string
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	baseURL := e.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	client := e.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	payload, err := json.Marshal(map[string]any{"model": e.ModelID, "input": texts})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+e.APIKey)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings: status %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var apiResp struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &apiResp); err != nil {
		return nil, err
	}
	out := make([][]float32, len(apiResp.Data))
	for i, d := range apiResp.Data {
		out[i] = d.Embedding
	}
	return out, nil
}
