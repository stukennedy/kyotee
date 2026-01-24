package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	AppName     = "kyotee"
	ConfigDir   = ".kyotee"
	ProjectDir  = ".kyotee"
)

// Paths holds resolved paths for the application
type Paths struct {
	// User-level paths (~/.kyotee/) - global config only
	UserDir     string // ~/.kyotee
	UserConfig  string // ~/.kyotee/config.toml
	UserSkills  string // ~/.kyotee/skills
	UserPrompts string // ~/.kyotee/prompts
	UserSchemas string // ~/.kyotee/schemas

	// Project-level paths (<cwd>/.kyotee/) - all state for this instance
	ProjectDir    string // <cwd>/.kyotee (may not exist)
	ProjectConfig string // <cwd>/.kyotee/spec.toml
	ProjectSkills string // <cwd>/.kyotee/skills

	// Working directory
	WorkDir string
}

// Resolve determines all paths based on current working directory
func Resolve() (*Paths, error) {
	// Get user home directory
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}

	userDir := filepath.Join(home, ConfigDir)
	projectDir := filepath.Join(cwd, ProjectDir)

	return &Paths{
		// User paths - global config only
		UserDir:     userDir,
		UserConfig:  filepath.Join(userDir, "config.toml"),
		UserSkills:  filepath.Join(userDir, "skills"),
		UserPrompts: filepath.Join(userDir, "prompts"),
		UserSchemas: filepath.Join(userDir, "schemas"),

		// Project paths - all state for this instance
		ProjectDir:    projectDir,
		ProjectConfig: filepath.Join(projectDir, "spec.toml"),
		ProjectSkills: filepath.Join(projectDir, "skills"),

		// Working dir
		WorkDir: cwd,
	}, nil
}

// EnsureUserDir creates the user directory structure if it doesn't exist
func (p *Paths) EnsureUserDir() error {
	dirs := []string{
		p.UserDir,
		p.UserSkills,
		p.UserPrompts,
		p.UserSchemas,
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create %s: %w", dir, err)
		}
	}

	return nil
}

// IsInitialized checks if kyotee has been set up
func (p *Paths) IsInitialized() bool {
	// Check if prompts directory has content
	entries, err := os.ReadDir(p.UserPrompts)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

// HasProjectConfig checks if there's a project-level .kyotee directory
func (p *Paths) HasProjectConfig() bool {
	info, err := os.Stat(p.ProjectDir)
	return err == nil && info.IsDir()
}

// EffectiveSkillsDirs returns skill directories to search (project first, then user)
func (p *Paths) EffectiveSkillsDirs() []string {
	dirs := []string{}

	// Project skills take precedence
	if p.HasProjectConfig() {
		if info, err := os.Stat(p.ProjectSkills); err == nil && info.IsDir() {
			dirs = append(dirs, p.ProjectSkills)
		}
	}

	// User skills as fallback
	dirs = append(dirs, p.UserSkills)

	return dirs
}

// EffectiveSpecPath returns the spec.toml path (project or user default)
func (p *Paths) EffectiveSpecPath() string {
	if p.HasProjectConfig() {
		if _, err := os.Stat(p.ProjectConfig); err == nil {
			return p.ProjectConfig
		}
	}
	return filepath.Join(p.UserDir, "spec.toml")
}
