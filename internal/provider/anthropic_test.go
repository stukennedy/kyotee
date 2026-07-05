package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// anthropicRoundTrip spins up a stub Messages API endpoint that captures the
// request body and returns a canned response.
func anthropicRoundTrip(t *testing.T, respBody string) (*Anthropic, *map[string]any) {
	t.Helper()
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured = map[string]any{} // fresh per request; Unmarshal merges into non-nil maps
		json.Unmarshal(raw, &captured)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return &Anthropic{ModelName: "claude", ModelID: "claude-test", APIKey: "k", BaseURL: srv.URL}, &captured
}

// Thinking blocks must decode and re-encode intact: with extended thinking
// on, tool-loop continuations that drop them are rejected by the API.
func TestAnthropicThinkingBlockRoundTrip(t *testing.T) {
	a, captured := anthropicRoundTrip(t, `{
		"content": [
			{"type": "thinking", "thinking": "let me search", "signature": "sig123"},
			{"type": "tool_use", "id": "t1", "name": "web_search", "input": {"query": "pm"}}
		],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`)

	resp, err := a.Generate(context.Background(), Request{
		Messages:        []Message{UserText("who is the pm?")},
		ReasoningEffort: "high",
		Tools:           []ToolDef{{Name: "web_search"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Content) != 2 || resp.Content[0].Type != "thinking" ||
		resp.Content[0].Signature != "sig123" {
		t.Fatalf("thinking block not decoded: %+v", resp.Content)
	}

	// Echo the assistant turn back (as the tool loop does) and verify the
	// wire format retains the signed thinking block.
	_, err = a.Generate(context.Background(), Request{
		Messages: []Message{
			UserText("who is the pm?"),
			{Role: "assistant", Content: resp.Content},
		},
		ReasoningEffort: "high",
		Tools:           []ToolDef{{Name: "web_search"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs := (*captured)["messages"].([]any)
	assistant := msgs[1].(map[string]any)
	blocks := assistant["content"].([]any)
	first := blocks[0].(map[string]any)
	if first["type"] != "thinking" || first["thinking"] != "let me search" || first["signature"] != "sig123" {
		t.Fatalf("thinking block not re-encoded on the wire: %+v", first)
	}
}

// An explicit temperature (the two-brain mechanism) must win over the
// effort→thinking mapping: extended thinking forbids temperature.
func TestAnthropicTemperatureSuppressesThinking(t *testing.T) {
	a, captured := anthropicRoundTrip(t, `{"content": [{"type": "text", "text": "ok"}], "stop_reason": "end_turn", "usage": {"input_tokens": 1, "output_tokens": 1}}`)

	_, err := a.Generate(context.Background(), Request{
		Messages:        []Message{UserText("diverge")},
		ReasoningEffort: "high",
		Temperature:     1.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, hasThinking := (*captured)["thinking"]; hasThinking {
		t.Fatal("thinking must be disabled when an explicit temperature is set")
	}
	if temp, ok := (*captured)["temperature"].(float64); !ok || temp != 1.0 {
		t.Fatalf("temperature not sent: %v", (*captured)["temperature"])
	}

	// And without a temperature, effort maps to a thinking budget.
	_, err = a.Generate(context.Background(), Request{
		Messages:        []Message{UserText("think")},
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, hasThinking := (*captured)["thinking"]; !hasThinking {
		t.Fatal("thinking should be enabled for effort=high with no temperature")
	}
	if _, hasTemp := (*captured)["temperature"]; hasTemp {
		t.Fatal("temperature must be absent when thinking is enabled")
	}
}

// ToolChoice="none" must keep tools defined but forbid further calls.
func TestAnthropicToolChoiceNone(t *testing.T) {
	a, captured := anthropicRoundTrip(t, `{"content": [{"type": "text", "text": "final"}], "stop_reason": "end_turn", "usage": {"input_tokens": 1, "output_tokens": 1}}`)

	_, err := a.Generate(context.Background(), Request{
		Messages:   []Message{UserText("answer now")},
		Tools:      []ToolDef{{Name: "web_search"}},
		ToolChoice: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	if (*captured)["tools"] == nil {
		t.Fatal("tools must stay defined when tool history is present")
	}
	tc, _ := (*captured)["tool_choice"].(map[string]any)
	if tc == nil || tc["type"] != "none" {
		t.Fatalf("tool_choice none not sent: %v", (*captured)["tool_choice"])
	}
}
