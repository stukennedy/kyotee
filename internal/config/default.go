package config

// Default returns the built-in configuration used when no config file exists.
// Model IDs and prices are editable defaults, not authoritative — operators
// should pin the models and rates they actually use.
func Default() *Config {
	c := &Config{
		Providers: []Provider{
			{
				Name: "claude-haiku", Kind: "anthropic", Model: "claude-haiku-4-5-20251001",
				APIKeyEnv: "ANTHROPIC_API_KEY", Reasoning: false,
				Cost: Cost{Input: 1, Output: 5}, MaxTokens: 4096,
			},
			{
				Name: "claude-sonnet", Kind: "anthropic", Model: "claude-sonnet-5",
				APIKeyEnv: "ANTHROPIC_API_KEY", Reasoning: true,
				Cost: Cost{Input: 3, Output: 15}, MaxTokens: 8192,
			},
			{
				Name: "claude-opus", Kind: "anthropic", Model: "claude-opus-4-8",
				APIKeyEnv: "ANTHROPIC_API_KEY", Reasoning: true,
				Cost: Cost{Input: 15, Output: 75}, MaxTokens: 8192,
			},
			{
				Name: "gpt", Kind: "openai", Model: "gpt-5",
				APIKeyEnv: "OPENAI_API_KEY", Reasoning: true,
				Cost: Cost{Input: 1.25, Output: 10}, MaxTokens: 8192,
			},
			{
				Name: "gemini", Kind: "openai", Vendor: "google", Model: "gemini-2.5-pro",
				APIKeyEnv: "GEMINI_API_KEY",
				BaseURL:   "https://generativelanguage.googleapis.com/v1beta/openai",
				Cost:      Cost{Input: 1.25, Output: 10}, MaxTokens: 8192,
			},
		},
		Models: ModelRoles{
			Receptionist: "claude-haiku",
			Default:      "claude-sonnet",
		},
		Council: Council{
			Members: []string{"claude-sonnet", "gpt", "gemini"},
		},
		Embedder: Embedder{
			Model:     "text-embedding-3-small",
			APIKeyEnv: "OPENAI_API_KEY",
		},
		Routes: []Route{
			{
				When:     When{Complexity: "trivial"},
				Strategy: "solo", Thinking: "fast",
				Models:     Models{Primary: "claude-haiku"},
				MaxCostUSD: 0.10,
			},
			{
				When:     When{Domain: "code", Complexity: "standard"},
				Strategy: "solo", Thinking: "auto",
				Models: Models{Primary: "claude-sonnet"},
			},
			{
				When:     When{Domain: "code", Complexity: "hard"},
				Strategy: "twobrain", Thinking: "slow",
				Models: Models{
					Primary:   "claude-opus",
					Divergent: "gpt", Convergent: "claude-sonnet",
				},
			},
			{
				When:     When{Domain: "reasoning", Complexity: "hard"},
				Strategy: "council", Thinking: "slow",
				Models: Models{
					Primary: "claude-opus",
					Council: []string{"claude-sonnet", "gpt", "gemini"},
				},
			},
			{
				// default: solo, auto, mid model
				Strategy: "solo", Thinking: "auto",
				Models: Models{Primary: "claude-sonnet"},
			},
		},
	}
	c.Defaults()
	return c
}
