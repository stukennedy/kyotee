package config

import (
	"os"

	"github.com/stukennedy/kyotee/internal/provider"
)

// BuildRegistry instantiates a provider.Registry from the declared providers.
// Providers with missing API keys are still registered — the error surfaces
// at call time, so a partially-credentialed config remains usable for the
// models that do have keys.
func BuildRegistry(c *Config) *provider.MapRegistry {
	reg := provider.NewRegistry()
	for _, p := range c.Providers {
		apiKey := ""
		if p.APIKeyEnv != "" {
			apiKey = os.Getenv(p.APIKeyEnv)
		}
		switch p.Kind {
		case "anthropic":
			reg.Register(&provider.Anthropic{
				ModelName: p.Name, ModelID: p.Model,
				APIKey: apiKey, BaseURL: p.BaseURL,
				InUSD: p.Cost.Input, OutUSD: p.Cost.Output, MaxCtx: p.MaxCtx,
			})
		case "openai":
			reg.Register(&provider.OpenAICompat{
				ModelName: p.Name, ModelID: p.Model, VendorTag: p.Vendor,
				APIKey: apiKey, BaseURL: p.BaseURL, Reasoning: p.Reasoning,
				InUSD: p.Cost.Input, OutUSD: p.Cost.Output, MaxCtx: p.MaxCtx,
			})
		case "mock":
			vendor := p.Vendor
			if vendor == "" {
				vendor = "mock"
			}
			fake := provider.NewFake(p.Name, vendor)
			fake.InUSD, fake.OutUSD = p.Cost.Input, p.Cost.Output
			reg.Register(fake)
		}
	}
	return reg
}

// BuildEmbedder returns the configured embedding client, or nil if the
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
