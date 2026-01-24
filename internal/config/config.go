package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/stukennedy/kyotee/internal/types"
)

// LoadSpec loads and parses the TOML spec file
func LoadSpec(path string) (*types.Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read spec: %w", err)
	}

	var spec types.Spec
	if err := toml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("failed to parse spec: %w", err)
	}

	// Set defaults
	if spec.Limits.MaxTotalIterations == 0 {
		spec.Limits.MaxTotalIterations = 25
	}
	if spec.Limits.MaxPhaseIterations == 0 {
		spec.Limits.MaxPhaseIterations = 6
	}

	return &spec, nil
}

// LoadPrompt loads a prompt file
func LoadPrompt(baseDir, name string) (string, error) {
	path := filepath.Join(baseDir, "prompts", name+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read prompt %s: %w", name, err)
	}
	return string(data), nil
}

// LoadSchema loads and compiles a JSON schema
func LoadSchema(baseDir, schemaPath string) (*jsonschema.Schema, error) {
	fullPath := filepath.Join(baseDir, schemaPath)

	compiler := jsonschema.NewCompiler()
	schema, err := compiler.Compile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to compile schema %s: %w", schemaPath, err)
	}

	return schema, nil
}

// ValidateJSON validates a JSON object against a schema
func ValidateJSON(schema *jsonschema.Schema, data any) error {
	if err := schema.Validate(data); err != nil {
		return fmt.Errorf("schema validation failed: %w", err)
	}
	return nil
}

// GetSchemaContent reads the raw schema JSON for inclusion in prompts
func GetSchemaContent(baseDir, schemaPath string) (string, error) {
	fullPath := filepath.Join(baseDir, schemaPath)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("failed to read schema %s: %w", schemaPath, err)
	}
	return string(data), nil
}
