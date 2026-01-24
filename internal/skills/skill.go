package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Skill represents knowledge about a tech stack
type Skill struct {
	Name        string   `toml:"name"`
	Description string   `toml:"description"`
	Tags        []string `toml:"tags"` // For matching: ["go", "gin", "rest", "api"]

	Conventions  Conventions       `toml:"conventions"`
	Patterns     map[string]string `toml:"patterns"`
	Docs         Docs              `toml:"docs"`
	Preferences  map[string]string `toml:"preferences"`
	Dependencies []string          `toml:"dependencies"`
}

type Conventions struct {
	ProjectStructure string `toml:"project_structure"`
	Naming           string `toml:"naming"`
	FileExtensions   string `toml:"file_extensions"`
}

type Docs struct {
	URLs     []string `toml:"urls"`
	Fetched  string   `toml:"fetched"`  // Cached doc content
	FetchedAt string  `toml:"fetched_at"`
}

// Registry holds all available skills
type Registry struct {
	skillsDir string
	skills    map[string]*Skill
}

// NewRegistry creates a skill registry from a directory
func NewRegistry(skillsDir string) (*Registry, error) {
	r := &Registry{
		skillsDir: skillsDir,
		skills:    make(map[string]*Skill),
	}

	if err := r.loadSkills(); err != nil {
		return nil, err
	}

	return r, nil
}

func (r *Registry) loadSkills() error {
	// Create skills dir if it doesn't exist
	if err := os.MkdirAll(r.skillsDir, 0755); err != nil {
		return err
	}

	// Also create custom subdir
	customDir := filepath.Join(r.skillsDir, "custom")
	if err := os.MkdirAll(customDir, 0755); err != nil {
		return err
	}

	// Load all .toml files
	return filepath.Walk(r.skillsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".toml") {
			return nil
		}

		skill, err := LoadSkill(path)
		if err != nil {
			return fmt.Errorf("failed to load skill %s: %w", path, err)
		}

		r.skills[skill.Name] = skill
		return nil
	})
}

// LoadSkill loads a skill from a TOML file
func LoadSkill(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var skill Skill
	if err := toml.Unmarshal(data, &skill); err != nil {
		return nil, err
	}

	return &skill, nil
}

// SaveSkill saves a skill to a TOML file
func SaveSkill(skillsDir string, skill *Skill, custom bool) error {
	dir := skillsDir
	if custom {
		dir = filepath.Join(skillsDir, "custom")
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Generate filename from skill name
	filename := strings.ToLower(strings.ReplaceAll(skill.Name, " ", "-")) + ".toml"
	path := filepath.Join(dir, filename)

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	encoder := toml.NewEncoder(f)
	return encoder.Encode(skill)
}

// Find searches for a skill matching the given terms
func (r *Registry) Find(terms ...string) *Skill {
	// Normalize search terms
	searchTerms := make([]string, len(terms))
	for i, t := range terms {
		searchTerms[i] = strings.ToLower(strings.TrimSpace(t))
	}

	var bestMatch *Skill
	bestScore := 0

	for _, skill := range r.skills {
		score := 0

		// Check name match
		nameLower := strings.ToLower(skill.Name)
		for _, term := range searchTerms {
			if strings.Contains(nameLower, term) {
				score += 3
			}
		}

		// Check tag matches
		for _, tag := range skill.Tags {
			tagLower := strings.ToLower(tag)
			for _, term := range searchTerms {
				if tagLower == term {
					score += 2
				} else if strings.Contains(tagLower, term) || strings.Contains(term, tagLower) {
					score += 1
				}
			}
		}

		// Check description
		descLower := strings.ToLower(skill.Description)
		for _, term := range searchTerms {
			if strings.Contains(descLower, term) {
				score += 1
			}
		}

		if score > bestScore {
			bestScore = score
			bestMatch = skill
		}
	}

	// Only return if we have a reasonable match
	if bestScore >= 2 {
		return bestMatch
	}
	return nil
}

// List returns all available skills
func (r *Registry) List() []*Skill {
	skills := make([]*Skill, 0, len(r.skills))
	for _, s := range r.skills {
		skills = append(skills, s)
	}
	return skills
}

// Add adds a skill to the registry
func (r *Registry) Add(skill *Skill) {
	r.skills[skill.Name] = skill
}

// ToPromptContext formats a skill for inclusion in prompts
func (s *Skill) ToPromptContext() string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("## Tech Stack: %s\n", s.Name))
	b.WriteString(fmt.Sprintf("%s\n\n", s.Description))

	if s.Conventions.ProjectStructure != "" {
		b.WriteString("### Project Structure\n```\n")
		b.WriteString(s.Conventions.ProjectStructure)
		b.WriteString("\n```\n\n")
	}

	if s.Conventions.Naming != "" {
		b.WriteString(fmt.Sprintf("### Naming: %s\n\n", s.Conventions.Naming))
	}

	if len(s.Patterns) > 0 {
		b.WriteString("### Patterns\n")
		for name, pattern := range s.Patterns {
			b.WriteString(fmt.Sprintf("**%s:**\n```\n%s\n```\n\n", name, pattern))
		}
	}

	if len(s.Preferences) > 0 {
		b.WriteString("### Preferences\n")
		for key, val := range s.Preferences {
			b.WriteString(fmt.Sprintf("- %s: %s\n", key, val))
		}
		b.WriteString("\n")
	}

	if len(s.Dependencies) > 0 {
		b.WriteString(fmt.Sprintf("### Dependencies: %s\n\n", strings.Join(s.Dependencies, ", ")))
	}

	if s.Docs.Fetched != "" {
		b.WriteString("### Reference Documentation\n")
		b.WriteString(s.Docs.Fetched)
		b.WriteString("\n")
	}

	return b.String()
}
