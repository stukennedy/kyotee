// Package provider defines the vendor-agnostic model surface. Adapters wrap
// vendor HTTP APIs; no vendor SDKs are imported anywhere in the engine.
package provider

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Provider is the single abstraction over every LLM, regardless of vendor.
type Provider interface {
	// Name is a stable identifier, e.g. "claude-opus-4-8", "gpt-5".
	Name() string
	// Vendor is the family, e.g. "anthropic", "openai", "google", "local".
	Vendor() string
	// Generate runs a single completion. Streaming is delivered via the
	// Stream callback in Request; the returned Response is the final aggregate.
	Generate(ctx context.Context, req Request) (Response, error)
	// Capabilities reports what this model supports (tools, vision, etc.).
	Capabilities() Capabilities
	// CostPer1M returns input/output USD cost per 1M tokens for budgeting.
	CostPer1M() (inputUSD, outputUSD float64)
}

type Request struct {
	System      string
	Messages    []Message
	Tools       []ToolDef // provider-agnostic tool declarations
	Temperature float64
	MaxTokens   int
	// ReasoningEffort is a normalized knob: "minimal" | "low" | "medium" | "high".
	// Adapters map this to vendor-specific reasoning/thinking params.
	ReasoningEffort string
	// Stream, if non-nil, receives incremental deltas for observability.
	Stream func(Delta)
	// Metadata is opaque pass-through for tracing (task ID, stage, etc.).
	Metadata map[string]string
}

type Response struct {
	Content    []Block // text and tool_use blocks, in order
	StopReason string
	Usage      Usage
}

// Text concatenates all text blocks in the response.
func (r Response) Text() string {
	var out string
	for _, b := range r.Content {
		if b.Type == "text" {
			out += b.Text
		}
	}
	return out
}

// ToolCalls returns all tool_use blocks in the response.
func (r Response) ToolCalls() []*ToolCall {
	var calls []*ToolCall
	for _, b := range r.Content {
		if b.Type == "tool_use" && b.ToolCall != nil {
			calls = append(calls, b.ToolCall)
		}
	}
	return calls
}

type Block struct {
	Type       string      `json:"type"`                  // "text" | "tool_use" | "tool_result"
	Text       string      `json:"text,omitempty"`        // when Type == "text"
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`   // when Type == "tool_use"
	ToolResult *ToolResult `json:"tool_result,omitempty"` // when Type == "tool_result"
}

type Usage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"` // computed by adapter from CostPer1M
}

func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CostUSD += o.CostUSD
}

type Capabilities struct {
	Tools      bool
	Vision     bool
	Reasoning  bool // supports explicit reasoning-effort control
	MaxContext int
}

type Message struct {
	Role    string  `json:"role"` // "user" | "assistant" | "tool"
	Content []Block `json:"content"`
}

// UserText is a convenience constructor for a plain user message.
func UserText(text string) Message {
	return Message{Role: "user", Content: []Block{{Type: "text", Text: text}}}
}

// AssistantText is a convenience constructor for a plain assistant message.
func AssistantText(text string) Message {
	return Message{Role: "assistant", Content: []Block{{Type: "text", Text: text}}}
}

type Delta struct {
	Type string // "text" | "tool_use_start" | "reasoning" | "done"
	Text string
}

// ToolDef, ToolCall, ToolResult are provider-agnostic tool types.
type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any // JSON schema of parameters
}

type ToolCall struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type ToolResult struct {
	CallID  string `json:"call_id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
}

// CostFor computes USD cost for a token count against a provider's rates.
func CostFor(p Provider, inputTokens, outputTokens int) float64 {
	in, out := p.CostPer1M()
	return float64(inputTokens)/1e6*in + float64(outputTokens)/1e6*out
}

// Registry maps Name() → Provider. Built from config at startup; all
// mechanisms resolve models by name through it.
type Registry interface {
	Get(name string) (Provider, error)
	List() []Provider
}

type MapRegistry struct {
	mu sync.RWMutex
	m  map[string]Provider
}

func NewRegistry(providers ...Provider) *MapRegistry {
	r := &MapRegistry{m: make(map[string]Provider)}
	for _, p := range providers {
		r.Register(p)
	}
	return r
}

func (r *MapRegistry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[p.Name()] = p
}

func (r *MapRegistry) Get(name string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.m[name]
	if !ok {
		return nil, fmt.Errorf("provider %q not registered", name)
	}
	return p, nil
}

func (r *MapRegistry) List() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, 0, len(r.m))
	for _, p := range r.m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}
