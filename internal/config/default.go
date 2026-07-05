package config

// Default returns the built-in configuration used when no config file
// exists. Model names and prices are operator-supplied placeholders (spec 02
// §1: "model names are config, not constants") — verify current identifiers
// and rates against vendor docs and edit ~/.kyotee/config.yaml.
func Default() *Config {
	c := &Config{
		Version: 1,
		Defaults: Defaults{
			BudgetUSD:   0.50,
			ToolCallCap: 4,
		},
		Providers: []Provider{
			{
				Name: "claude-haiku-4-5", Vendor: "anthropic",
				APIKeyEnv: "ANTHROPIC_API_KEY", Reasoning: false, MaxContext: 200000,
				Cost: Cost{Input: 0.80, Output: 4.00}, MaxTokens: 4096,
			},
			{
				Name: "claude-sonnet-5", Vendor: "anthropic",
				APIKeyEnv: "ANTHROPIC_API_KEY", Reasoning: true, MaxContext: 200000,
				Cost: Cost{Input: 3.00, Output: 15.00}, MaxTokens: 8192,
			},
			{
				Name: "claude-opus-4-8", Vendor: "anthropic",
				APIKeyEnv: "ANTHROPIC_API_KEY", Reasoning: true, MaxContext: 200000,
				Cost: Cost{Input: 5.00, Output: 25.00}, MaxTokens: 8192,
			},
			{
				Name: "gpt-5", Vendor: "openai",
				APIKeyEnv: "OPENAI_API_KEY", Reasoning: true, MaxContext: 400000,
				Cost: Cost{Input: 2.50, Output: 10.00}, MaxTokens: 8192,
			},
			{
				Name: "gemini-3-pro", Vendor: "google",
				APIKeyEnv: "GOOGLE_API_KEY", Reasoning: true, MaxContext: 1000000,
				Cost: Cost{Input: 2.00, Output: 8.00}, MaxTokens: 8192,
			},
		},
		Receptionist: Receptionist{
			Model: "claude-haiku-4-5",
			Routes: []Route{
				{
					When:     When{Complexity: "trivial"},
					Strategy: "solo", Thinking: "fast",
					Models:    Models{Primary: "claude-haiku-4-5"},
					BudgetUSD: 0.05,
				},
				{
					When:     When{Domain: "code", Complexity: "standard"},
					Strategy: "solo", Thinking: "auto",
					Models:    Models{Primary: "claude-sonnet-5"},
					BudgetUSD: 0.30,
				},
				{
					When:     When{Domain: "code", Complexity: "hard"},
					Strategy: "twobrain", Thinking: "slow",
					Models: Models{
						Primary:   "claude-opus-4-8",
						Divergent: "claude-sonnet-5", Convergent: "claude-sonnet-5",
					},
					BudgetUSD: 1.50,
				},
				{
					When:     When{Domain: "reasoning", Complexity: "hard"},
					Strategy: "council", Thinking: "slow",
					Models: Models{
						Primary: "claude-opus-4-8",
						Council: []string{"claude-opus-4-8", "gpt-5", "gemini-3-pro"},
					},
					BudgetUSD: 3.00,
				},
				{
					// tool_need==required forces slow regardless of route mode.
					When:     When{ToolNeed: "required"},
					Strategy: "solo", Thinking: "slow",
					Models:    Models{Primary: "claude-sonnet-5"},
					BudgetUSD: 0.40,
				},
				{
					// default catch-all
					Strategy: "solo", Thinking: "auto",
					Models:    Models{Primary: "claude-sonnet-5"},
					BudgetUSD: 0.30,
				},
			},
		},
		Council: Council{
			RequireVendorDiversity: true,
			Members:                []string{"claude-sonnet-5", "gpt-5", "gemini-3-pro"},
		},
		Embedder: Embedder{
			Provider:  "openai",
			Model:     "text-embedding-3-large",
			APIKeyEnv: "OPENAI_API_KEY",
		},
	}
	c.ApplyDefaults()
	return c
}
