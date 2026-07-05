// Package config declares the engine's YAML configuration: providers,
// model roles, routing rules, budget, and strategy tuning (inferred spec 07).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen   string `yaml:"listen"`    // HTTP/SSE bind address
	StateDir string `yaml:"state_dir"` // task state; default ~/.kyotee/tasks

	Budget   Budget     `yaml:"budget"`
	Models   ModelRoles `yaml:"models"`
	Thinking Thinking   `yaml:"thinking"`
	Council  Council    `yaml:"council"`
	TwoBrain TwoBrain   `yaml:"twobrain"`
	Embedder Embedder   `yaml:"embedder"`

	Providers []Provider `yaml:"providers"`
	Routes    []Route    `yaml:"routes"`
}

type Budget struct {
	DefaultLimitUSD float64 `yaml:"default_limit_usd"`
}

// ModelRoles names the registry entries used for engine-internal roles.
type ModelRoles struct {
	Receptionist string `yaml:"receptionist"` // cheap classifier/gate model
	Default      string `yaml:"default"`      // fallback primary
}

type Thinking struct {
	FastEffort          string  `yaml:"fast_effort"`          // default "low"
	SlowEffort          string  `yaml:"slow_effort"`          // default "high"
	ConfidenceThreshold float64 `yaml:"confidence_threshold"` // low_confidence gate trigger
	MaxToolCalls        int     `yaml:"max_tool_calls"`       // tool-loop cap
}

type Council struct {
	Rounds     int       `yaml:"rounds"` // default 3, hard-capped
	Consensus  Consensus `yaml:"consensus"`
	OnDeadlock string    `yaml:"on_deadlock"` // referee | majority_vote | synthesis_notes_dissent
	// Members is the default member list used when a route (or an override
	// escalating to council) doesn't name its own.
	Members []string `yaml:"members"`
}

type Consensus struct {
	Method    string  `yaml:"method"`    // vote | similarity | judge
	Threshold float64 `yaml:"threshold"` // meaning depends on method
}

type TwoBrain struct {
	Rounds int `yaml:"rounds"` // divergent/convergent exchanges before referee
}

type Embedder struct {
	Model     string `yaml:"model"` // e.g. text-embedding-3-small; empty = similarity unavailable
	APIKeyEnv string `yaml:"api_key_env"`
	BaseURL   string `yaml:"base_url"`
}

// Provider declares one model endpoint for the registry.
type Provider struct {
	Name      string  `yaml:"name"`   // registry name, referenced by routes
	Kind      string  `yaml:"kind"`   // anthropic | openai | mock
	Vendor    string  `yaml:"vendor"` // family tag; defaults from kind
	Model     string  `yaml:"model"`  // vendor model id
	APIKeyEnv string  `yaml:"api_key_env"`
	BaseURL   string  `yaml:"base_url"`
	Reasoning bool    `yaml:"reasoning"` // supports reasoning-effort knob
	Cost      Cost    `yaml:"cost_per_1m"`
	MaxCtx    int     `yaml:"max_context"`
	MaxTokens int     `yaml:"max_tokens"`
	Temp      float64 `yaml:"temperature"`
}

type Cost struct {
	Input  float64 `yaml:"input"`
	Output float64 `yaml:"output"`
}

// Route is one declarative routing rule; first match wins (spec 03 §3).
type Route struct {
	When       When    `yaml:"when"`     // empty predicate matches everything
	Strategy   string  `yaml:"strategy"` // solo | twobrain | council
	Thinking   string  `yaml:"thinking"` // fast | slow | auto
	Models     Models  `yaml:"models"`
	MaxCostUSD float64 `yaml:"max_cost_usd"` // 0 = inherit global default
}

type When struct {
	Complexity string `yaml:"complexity"` // "" = any
	Domain     string `yaml:"domain"`
	ToolNeed   string `yaml:"tool_need"`
}

type Models struct {
	Primary    string   `yaml:"primary"`    // solo / referee / synthesis model
	Divergent  string   `yaml:"divergent"`  // two-brain right brain
	Convergent string   `yaml:"convergent"` // two-brain left brain
	Council    []string `yaml:"council"`    // council members
}

// Defaults fills unset fields in place.
func (c *Config) Defaults() {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8484"
	}
	if c.Budget.DefaultLimitUSD == 0 {
		c.Budget.DefaultLimitUSD = 3.0
	}
	if c.Thinking.FastEffort == "" {
		c.Thinking.FastEffort = "low"
	}
	if c.Thinking.SlowEffort == "" {
		c.Thinking.SlowEffort = "high"
	}
	if c.Thinking.ConfidenceThreshold == 0 {
		c.Thinking.ConfidenceThreshold = 0.4
	}
	if c.Thinking.MaxToolCalls == 0 {
		c.Thinking.MaxToolCalls = 5
	}
	if c.Council.Rounds == 0 {
		c.Council.Rounds = 3
	}
	if c.Council.Rounds > 5 {
		c.Council.Rounds = 5 // hard cap: councils can debate forever
	}
	if c.Council.Consensus.Method == "" {
		c.Council.Consensus.Method = "vote"
	}
	if c.Council.Consensus.Threshold == 0 {
		switch c.Council.Consensus.Method {
		case "similarity":
			c.Council.Consensus.Threshold = 0.85
		default:
			c.Council.Consensus.Threshold = 0.66
		}
	}
	if c.Council.OnDeadlock == "" {
		c.Council.OnDeadlock = "synthesis_notes_dissent"
	}
	if c.TwoBrain.Rounds == 0 {
		c.TwoBrain.Rounds = 2
	}
}

// Validate checks referential integrity (routes → providers, roles → providers).
func (c *Config) Validate() error {
	names := map[string]bool{}
	for _, p := range c.Providers {
		if p.Name == "" {
			return fmt.Errorf("provider with empty name")
		}
		if names[p.Name] {
			return fmt.Errorf("duplicate provider name %q", p.Name)
		}
		switch p.Kind {
		case "anthropic", "openai", "mock":
		default:
			return fmt.Errorf("provider %q: unknown kind %q", p.Name, p.Kind)
		}
		names[p.Name] = true
	}
	check := func(ctx, name string) error {
		if name != "" && !names[name] {
			return fmt.Errorf("%s references unknown provider %q", ctx, name)
		}
		return nil
	}
	if err := check("models.receptionist", c.Models.Receptionist); err != nil {
		return err
	}
	if err := check("models.default", c.Models.Default); err != nil {
		return err
	}
	for _, m := range c.Council.Members {
		if err := check("council.members", m); err != nil {
			return err
		}
	}
	for i, r := range c.Routes {
		ctx := fmt.Sprintf("route %d", i)
		switch r.Strategy {
		case "solo", "twobrain", "council":
		default:
			return fmt.Errorf("%s: unknown strategy %q", ctx, r.Strategy)
		}
		switch r.Thinking {
		case "", "fast", "slow", "auto":
		default:
			return fmt.Errorf("%s: unknown thinking mode %q", ctx, r.Thinking)
		}
		for _, m := range append([]string{r.Models.Primary, r.Models.Divergent, r.Models.Convergent}, r.Models.Council...) {
			if err := check(ctx, m); err != nil {
				return err
			}
		}
	}
	return nil
}

// DefaultPath returns ~/.kyotee/config.yaml.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".kyotee", "config.yaml")
	}
	return filepath.Join(home, ".kyotee", "config.yaml")
}

// Load reads, defaults, and validates a config file. A missing file yields
// the built-in default config.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath()
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		c := Default()
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes, defaults, and validates raw YAML.
func Parse(data []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.Defaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Holder is a hot-swappable config handle for PUT /v1/config: readers always
// see a consistent snapshot; an invalid replacement never takes effect.
type Holder struct {
	mu  sync.RWMutex
	cfg *Config
}

func NewHolder(c *Config) *Holder { return &Holder{cfg: c} }

func (h *Holder) Get() *Config {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cfg
}

func (h *Holder) Set(c *Config) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg = c
}
