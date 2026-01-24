package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	DefaultModel   = "claude-sonnet-4-20250514"
	DefaultBaseURL = "https://api.anthropic.com/v1"
	APIVersion     = "2023-06-01"
)

// Client is the Claude API client
type Client struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
	MaxTokens  int
}

// NewClient creates a new Claude API client
func NewClient() (*Client, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY environment variable not set")
	}

	return &Client{
		APIKey:  apiKey,
		Model:   DefaultModel,
		BaseURL: DefaultBaseURL,
		HTTPClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
		MaxTokens: 8192,
	}, nil
}

// Message represents a conversation message
type Message struct {
	Role    string        `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ContentBlock is a content block in a message
type ContentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

// Tool represents a tool definition
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"input_schema"`
}

// InputSchema is the JSON schema for tool input
type InputSchema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties"`
	Required   []string            `json:"required,omitempty"`
}

// Property is a property in the input schema
type Property struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Enum        []string `json:"enum,omitempty"`
}

// Request is the API request body
type Request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
	Tools     []Tool    `json:"tools,omitempty"`
}

// Response is the API response
type Response struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Content      []ContentBlock `json:"content"`
	Model        string         `json:"model"`
	StopReason   string         `json:"stop_reason"`
	StopSequence string         `json:"stop_sequence,omitempty"`
	Usage        Usage          `json:"usage"`
}

// Usage contains token usage info
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Call makes a request to the Claude API
func (c *Client) Call(req *Request) (*Response, error) {
	if req.Model == "" {
		req.Model = c.Model
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = c.MaxTokens
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", c.BaseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.APIKey)
	httpReq.Header.Set("anthropic-version", APIVersion)

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var response Response
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &response, nil
}

// GetTextContent extracts text content from response
func (r *Response) GetTextContent() string {
	for _, block := range r.Content {
		if block.Type == "text" {
			return block.Text
		}
	}
	return ""
}

// GetToolUses extracts tool use blocks from response
func (r *Response) GetToolUses() []ContentBlock {
	var tools []ContentBlock
	for _, block := range r.Content {
		if block.Type == "tool_use" {
			tools = append(tools, block)
		}
	}
	return tools
}

// HasToolUse returns true if response contains tool use
func (r *Response) HasToolUse() bool {
	return r.StopReason == "tool_use"
}
