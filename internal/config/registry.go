package config

import (
	"os"

	"github.com/stukennedy/kyotee/internal/provider"
	"github.com/stukennedy/kyotee/internal/thinking"
)

// googleOpenAIBase is Google's OpenAI-compatible endpoint for the Gemini
// family, used when a google provider doesn't set base_url explicitly.
const googleOpenAIBase = "https://generativelanguage.googleapis.com/v1beta/openai"

// BuildRegistry instantiates a provider.Registry from the declared
// providers. Vendor selects the adapter: anthropic → Messages API;
// openai/google/local → OpenAI-compatible chat completions; mock → Fake.
// Providers with unset API keys are still registered — the error surfaces at
// call time (config load already warns loudly).
func BuildRegistry(c *Config) *provider.MapRegistry {
	reg := provider.NewRegistry()
	for _, p := range c.Providers {
		apiKey := ""
		if p.APIKeyEnv != "" {
			apiKey = os.Getenv(p.APIKeyEnv)
		}
		switch p.Vendor {
		case "anthropic":
			reg.Register(&provider.Anthropic{
				ModelName: p.Name, ModelID: p.Model,
				APIKey: apiKey, BaseURL: p.BaseURL,
				InUSD: p.Cost.Input, OutUSD: p.Cost.Output, MaxCtx: p.MaxContext,
			})
		case "openai", "google", "local":
			baseURL := p.BaseURL
			if baseURL == "" && p.Vendor == "google" {
				baseURL = googleOpenAIBase
			}
			reg.Register(&provider.OpenAICompat{
				ModelName: p.Name, ModelID: p.Model, VendorTag: p.Vendor,
				APIKey: apiKey, BaseURL: baseURL, Reasoning: p.Reasoning,
				InUSD: p.Cost.Input, OutUSD: p.Cost.Output, MaxCtx: p.MaxContext,
			})
		case "mock":
			fake := provider.NewFake(p.Name, "mock")
			fake.InUSD, fake.OutUSD = p.Cost.Input, p.Cost.Output
			reg.Register(fake)
		}
	}
	return reg
}

// BuildEmbedder returns the configured embedding client, or nil when the
// similarity consensus method is unavailable.
func BuildEmbedder(c *Config) *provider.OpenAIEmbedder {
	if c.Embedder.Model == "" {
		return nil
	}
	apiKey := ""
	if c.Embedder.APIKeyEnv != "" {
		apiKey = os.Getenv(c.Embedder.APIKeyEnv)
	}
	return &provider.OpenAIEmbedder{
		ModelID: c.Embedder.Model,
		APIKey:  apiKey,
		BaseURL: c.Embedder.BaseURL,
	}
}

// BuildTools instantiates the tool registry from the tools block.
func BuildTools(c *Config) *thinking.ToolRegistry {
	reg := thinking.NewToolRegistry()
	for _, t := range c.Tools {
		switch t.Kind {
		case "web_search":
			reg.Register(&thinking.WebSearch{})
		case "file_read":
			reg.Register(thinking.NewFileRead(t.Name, t.Root))
		}
	}
	return reg
}
