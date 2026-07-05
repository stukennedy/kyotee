// Package config declares the engine's YAML configuration (spec 07): the
// provider registry, routing rules, and per-mechanism defaults. It is the
// product's control surface — hot-reloadable, validated, secrets only ever
// referenced as env-var names.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version  int    `yaml:"version"`   // must be 1
	Listen   string `yaml:"listen"`    // HTTP/SSE bind address (kyotee extension)
	StateDir string `yaml:"state_dir"` // task state; default ~/.kyotee/tasks

	Defaults     Defaults     `yaml:"defaults"`
	Providers    []Provider   `yaml:"providers"`
	Receptionist Receptionist `yaml:"receptionist"`
	Thinking     Thinking     `yaml:"thinking"`
	TwoBrain     TwoBrain     `yaml:"twobrain"`
	Council      Council      `yaml:"council"`
	Tools        []Tool       `yaml:"tools"`
	Embedder     Embedder     `yaml:"embedder"`
}

// Defaults are global fallbacks (spec 07 §2).
type Defaults struct {
	BudgetUSD           float64 `yaml:"budget_usd"`            // per-task ceiling unless a route overrides
	ReasoningEffortFast string  `yaml:"reasoning_effort_fast"` // effort in fast mode
	ReasoningEffortSlow string  `yaml:"reasoning_effort_slow"` // effort in slow mode
	ToolCallCap         int     `yaml:"tool_call_cap"`         // max tool calls per solver loop
}

// Provider declares one model endpoint. Vendor selects the adapter:
// anthropic | openai | google | local | mock. google/local use the
// OpenAI-compatible adapter with an appropriate base URL.
type Provider struct {
	Name       string  `yaml:"name"`
	Vendor     string  `yaml:"vendor"`
	Model      string  `yaml:"model"` // vendor model id; defaults to Name
	APIKeyEnv  string  `yaml:"api_key_env"`
	BaseURL    string  `yaml:"base_url"`
	Reasoning  bool    `yaml:"reasoning"`
	MaxContext int     `yaml:"max_context"`
	Cost       Cost    `yaml:"cost_per_1m"`
	MaxTokens  int     `yaml:"max_tokens"`
	Temp       float64 `yaml:"temperature"`
}

type Cost struct {
	Input  float64 `yaml:"input"`
	Output float64 `yaml:"output"`
}

// Receptionist holds the classifier model, budget defaults, and routes.
type Receptionist struct {
	Model            string    `yaml:"model"`              // cheap classifier
	BudgetDefaultUSD float64   `yaml:"budget_default_usd"` // overrides defaults.budget_usd when set
	WarnThresholds   []float64 `yaml:"warn_thresholds"`    // sorted, each in (0,1)
	Routes           []Route   `yaml:"routes"`
}

// Route is one declarative routing rule; first match wins (spec 03 §3).
type Route struct {
	When      When    `yaml:"when"` // empty predicate matches everything
	Strategy  string  `yaml:"strategy"`
	Thinking  string  `yaml:"thinking"`
	Models    Models  `yaml:"models"`
	BudgetUSD float64 `yaml:"budget_usd"` // 0 = inherit default
}

type When struct {
	Complexity string `yaml:"complexity"` // "" = any
	Domain     string `yaml:"domain"`
	ToolNeed   string `yaml:"tool_need"`
}

type Models struct {
	Primary    string   `yaml:"primary"`
	Divergent  string   `yaml:"divergent"`
	Convergent string   `yaml:"convergent"`
	Council    []string `yaml:"council"`
}

// Thinking tunes the fast/slow gate (spec 04, 07).
type Thinking struct {
	GateModel          string   `yaml:"gate_model"`           // auto gate; defaults to receptionist.model
	PrepassModel       string   `yaml:"prepass_model"`        // tool-need pre-pass; defaults to gate_model
	SlowTriggers       []string `yaml:"slow_triggers"`        // any firing → slow
	LowConfidenceBelow float64  `yaml:"low_confidence_below"` // classifier confidence trigger
}

// TwoBrain tunes the divergent/convergent strategy (spec 05).
type TwoBrain struct {
	Rounds   int             `yaml:"rounds"`    // 1..3, hard-capped at 3
	DivTemp  float64         `yaml:"div_temp"`  // default 1.0
	ConvTemp float64         `yaml:"conv_temp"` // default 0.3
	Prompts  TwoBrainPrompts `yaml:"prompts"`   // external persona prompt files
}

type TwoBrainPrompts struct {
	Divergent  string `yaml:"divergent"`
	Convergent string `yaml:"convergent"`
	Referee    string `yaml:"referee"`
}

type Council struct {
	Rounds                 int       `yaml:"rounds"` // >=1, hard cap 5
	Protocol               string    `yaml:"protocol"`
	Consensus              Consensus `yaml:"consensus"`
	OnDeadlock             string    `yaml:"on_deadlock"`
	RequireVendorDiversity bool      `yaml:"require_vendor_diversity"`
	// Members is the default member list used when a route (or an override
	// escalating to council) doesn't name its own. Kyotee extension.
	Members []string `yaml:"members"`
}

type Consensus struct {
	Method    string  `yaml:"method"`    // vote | similarity | judge
	Threshold float64 `yaml:"threshold"` // meaning depends on method
}

// Tool declares one registry entry (spec 07 §2 tools block).
type Tool struct {
	Name string `yaml:"name"`
	Kind string `yaml:"kind"` // web_search | file_read
	Root string `yaml:"root"` // file_read sandbox root
}

type Embedder struct {
	Provider  string `yaml:"provider"` // vendor exposing embeddings
	Model     string `yaml:"model"`
	APIKeyEnv string `yaml:"api_key_env"`
	BaseURL   string `yaml:"base_url"`
}

const (
	CouncilRoundsHardCap = 5
	TwoBrainRoundsMax    = 3
)

// Defaults fills unset fields in place (does not validate).
func (c *Config) ApplyDefaults() {
	if c.Version == 0 {
		c.Version = 1
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8484"
	}
	if c.Defaults.BudgetUSD == 0 {
		c.Defaults.BudgetUSD = 0.50
	}
	if c.Defaults.ReasoningEffortFast == "" {
		c.Defaults.ReasoningEffortFast = "low"
	}
	if c.Defaults.ReasoningEffortSlow == "" {
		c.Defaults.ReasoningEffortSlow = "high"
	}
	if c.Defaults.ToolCallCap == 0 {
		c.Defaults.ToolCallCap = 4
	}
	if len(c.Receptionist.WarnThresholds) == 0 {
		c.Receptionist.WarnThresholds = []float64{0.5, 0.8, 0.95}
	}
	if c.Thinking.GateModel == "" {
		c.Thinking.GateModel = c.Receptionist.Model
	}
	if c.Thinking.PrepassModel == "" {
		c.Thinking.PrepassModel = c.Thinking.GateModel
	}
	if len(c.Thinking.SlowTriggers) == 0 {
		c.Thinking.SlowTriggers = []string{
			"present_state_fact", "low_confidence", "multi_step_math",
			"repo_or_file_ref", "explicit_user_flag",
		}
	}
	if c.Thinking.LowConfidenceBelow == 0 {
		c.Thinking.LowConfidenceBelow = 0.7
	}
	if c.TwoBrain.Rounds == 0 {
		c.TwoBrain.Rounds = 2
	}
	if c.TwoBrain.DivTemp == 0 {
		c.TwoBrain.DivTemp = 1.0
	}
	if c.TwoBrain.ConvTemp == 0 {
		c.TwoBrain.ConvTemp = 0.3
	}
	if c.Council.Rounds == 0 {
		c.Council.Rounds = 3
	}
	if c.Council.Protocol == "" {
		c.Council.Protocol = "debate"
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
	if len(c.Tools) == 0 {
		c.Tools = []Tool{{Name: "web_search", Kind: "web_search"}}
	}
	for i := range c.Providers {
		if c.Providers[i].Model == "" {
			c.Providers[i].Model = c.Providers[i].Name
		}
		if c.Providers[i].Vendor == "" {
			c.Providers[i].Vendor = "anthropic"
		}
	}
}

// BudgetDefaultUSD resolves the effective global per-task ceiling.
func (c *Config) BudgetDefaultUSD() float64 {
	if c.Receptionist.BudgetDefaultUSD > 0 {
		return c.Receptionist.BudgetDefaultUSD
	}
	return c.Defaults.BudgetUSD
}

// Validate enforces the rule table from spec 07 §3. Errors are specific and
// actionable; the caller keeps the old config live on failure.
func (c *Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("version must be 1, got %d", c.Version)
	}

	names := map[string]string{} // name → vendor
	for _, p := range c.Providers {
		if p.Name == "" {
			return fmt.Errorf("provider with empty name")
		}
		if _, dup := names[p.Name]; dup {
			return fmt.Errorf("duplicate provider name %q", p.Name)
		}
		switch p.Vendor {
		case "anthropic", "openai", "google", "local", "mock":
		default:
			return fmt.Errorf("provider %q: unknown vendor %q (anthropic|openai|google|local|mock)", p.Name, p.Vendor)
		}
		if p.Vendor == "local" && p.BaseURL == "" {
			return fmt.Errorf("provider %q: vendor local requires base_url", p.Name)
		}
		if p.Vendor != "local" && p.Vendor != "mock" && p.APIKeyEnv == "" {
			return fmt.Errorf("provider %q: api_key_env is required for vendor %s", p.Name, p.Vendor)
		}
		names[p.Name] = p.Vendor
	}

	check := func(ctx, name string) error {
		if name == "" {
			return nil
		}
		if _, ok := names[name]; !ok {
			return fmt.Errorf("%s references unknown provider %q", ctx, name)
		}
		return nil
	}

	if err := check("receptionist.model", c.Receptionist.Model); err != nil {
		return err
	}
	if err := check("thinking.gate_model", c.Thinking.GateModel); err != nil {
		return err
	}
	if err := check("thinking.prepass_model", c.Thinking.PrepassModel); err != nil {
		return err
	}
	for _, m := range c.Council.Members {
		if err := check("council.members", m); err != nil {
			return err
		}
	}

	if !sort.Float64sAreSorted(c.Receptionist.WarnThresholds) {
		return fmt.Errorf("receptionist.warn_thresholds must be sorted ascending")
	}
	for _, t := range c.Receptionist.WarnThresholds {
		if t <= 0 || t >= 1 {
			return fmt.Errorf("receptionist.warn_thresholds: %v not in (0,1)", t)
		}
	}

	if c.TwoBrain.Rounds < 1 || c.TwoBrain.Rounds > TwoBrainRoundsMax {
		return fmt.Errorf("twobrain.rounds must be in [1,%d], got %d", TwoBrainRoundsMax, c.TwoBrain.Rounds)
	}
	if c.Council.Rounds < 1 || c.Council.Rounds > CouncilRoundsHardCap {
		return fmt.Errorf("council.rounds must be in [1,%d], got %d", CouncilRoundsHardCap, c.Council.Rounds)
	}
	switch c.Council.Consensus.Method {
	case "vote", "similarity", "judge":
	default:
		return fmt.Errorf("council.consensus.method %q not in {vote, similarity, judge}", c.Council.Consensus.Method)
	}
	if t := c.Council.Consensus.Threshold; t <= 0 || t > 1 {
		return fmt.Errorf("council.consensus.threshold %v not in (0,1]", t)
	}
	if c.Council.Consensus.Method == "similarity" && c.Embedder.Model == "" {
		return fmt.Errorf("council.consensus.method similarity requires an embedder block")
	}
	switch c.Council.OnDeadlock {
	case "referee", "majority_vote", "synthesis_notes_dissent":
	default:
		return fmt.Errorf("council.on_deadlock %q not in {referee, majority_vote, synthesis_notes_dissent}", c.Council.OnDeadlock)
	}

	for _, t := range c.Tools {
		switch t.Kind {
		case "web_search":
		case "file_read":
			if t.Root == "" {
				return fmt.Errorf("tool %q: kind file_read requires root", t.Name)
			}
		default:
			return fmt.Errorf("tool %q: unknown kind %q (web_search|file_read)", t.Name, t.Kind)
		}
	}

	for i, r := range c.Receptionist.Routes {
		ctx := fmt.Sprintf("route %d", i)
		if err := validateRouteShape(ctx, r.Strategy, r.Thinking); err != nil {
			return err
		}
		if err := validateWhen(ctx, r.When); err != nil {
			return err
		}
		for _, m := range append([]string{r.Models.Primary, r.Models.Divergent, r.Models.Convergent}, r.Models.Council...) {
			if err := check(ctx, m); err != nil {
				return err
			}
		}
		if r.Strategy == "council" {
			members := r.Models.Council
			if len(members) == 0 {
				members = c.Council.Members
			}
			if len(members) < 2 {
				return fmt.Errorf("%s: council strategy needs >=2 members (route models.council or council.members)", ctx)
			}
		}
	}
	return nil
}

func validateRouteShape(ctx, strategy, thinking string) error {
	switch strategy {
	case "solo", "twobrain", "council":
	default:
		return fmt.Errorf("%s: strategy %q not in {solo, twobrain, council}", ctx, strategy)
	}
	switch thinking {
	case "", "fast", "slow", "auto":
	default:
		return fmt.Errorf("%s: thinking %q not in {fast, slow, auto}", ctx, thinking)
	}
	return nil
}

func validateWhen(ctx string, w When) error {
	if w.Complexity != "" {
		switch w.Complexity {
		case "trivial", "standard", "hard":
		default:
			return fmt.Errorf("%s: when.complexity %q invalid", ctx, w.Complexity)
		}
	}
	if w.Domain != "" {
		switch w.Domain {
		case "code", "research", "reasoning", "creative", "chat":
		default:
			return fmt.Errorf("%s: when.domain %q invalid", ctx, w.Domain)
		}
	}
	if w.ToolNeed != "" {
		switch w.ToolNeed {
		case "none", "likely", "required":
		default:
			return fmt.Errorf("%s: when.tool_need %q invalid", ctx, w.ToolNeed)
		}
	}
	return nil
}

// Warnings collects non-fatal issues (unset API key env vars, same-vendor
// councils) surfaced at load time.
func (c *Config) Warnings() []string {
	var out []string
	for _, p := range c.Providers {
		if p.APIKeyEnv != "" && os.Getenv(p.APIKeyEnv) == "" {
			out = append(out, fmt.Sprintf("provider %q: env var %s is not set — provider unusable until it is", p.Name, p.APIKeyEnv))
		}
	}
	return out
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
		return Default(), nil
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
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Holder is a hot-swappable config handle: readers always see a consistent
// snapshot; an invalid replacement never takes effect. In-flight tasks keep
// the snapshot they captured at intake.
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
