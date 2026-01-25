package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	KyoteeDir        = ".kyotee"
	ConversationFile = "conversation.json"
	SpecFile         = "spec.json"
	JobFile          = "job.json"
	PhasesDir        = "phases"
)

// Message represents a conversation message
type Message struct {
	Role      string    `json:"role"` // "user" or "assistant"
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// Conversation holds the full discovery conversation
type Conversation struct {
	Messages  []Message `json:"messages"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Spec holds the generated project specification
type Spec struct {
	ProjectName  string         `json:"project_name"`
	Goal         string         `json:"goal"`
	Language     string         `json:"language,omitempty"`
	Framework    string         `json:"framework,omitempty"`
	Features     []string       `json:"features,omitempty"`
	Constraints  []string       `json:"constraints,omitempty"`
	FilesToCreate []string      `json:"files_to_create,omitempty"`
	FilesToModify []string      `json:"files_to_modify,omitempty"`
	Skill        string         `json:"skill,omitempty"`
	Raw          map[string]any `json:"raw,omitempty"` // Original spec data
	CreatedAt    time.Time      `json:"created_at"`
}

// JobStatus represents the current job state
type JobStatus string

const (
	JobStatusDiscovery  JobStatus = "discovery"
	JobStatusRunning    JobStatus = "running"
	JobStatusPaused     JobStatus = "paused"
	JobStatusCompleted  JobStatus = "completed"
	JobStatusFailed     JobStatus = "failed"
)

// PhaseState represents the state of a single phase
type PhaseState struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"` // pending, running, passed, failed
	Iteration int       `json:"iteration"`
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// Job holds the current job state
type Job struct {
	Status       JobStatus    `json:"status"`
	CurrentPhase int          `json:"current_phase"`
	Phases       []PhaseState `json:"phases"`
	StartedAt    time.Time    `json:"started_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	Error        string       `json:"error,omitempty"`
}

// Project represents a kyotee project with all its state
type Project struct {
	Dir          string        // Project root directory
	KyoteeDir    string        // .kyotee directory path
	Conversation *Conversation
	Spec         *Spec
	Job          *Job
}

// Open opens or creates a project at the given directory
func Open(projectDir string) (*Project, error) {
	kyoteeDir := filepath.Join(projectDir, KyoteeDir)

	p := &Project{
		Dir:       projectDir,
		KyoteeDir: kyoteeDir,
	}

	// Create .kyotee directory structure if needed
	if err := p.ensureDirs(); err != nil {
		return nil, err
	}

	// Load existing state if present
	p.loadConversation()
	p.loadSpec()
	p.loadJob()

	return p, nil
}

// ensureDirs creates the .kyotee directory structure
func (p *Project) ensureDirs() error {
	dirs := []string{
		p.KyoteeDir,
		filepath.Join(p.KyoteeDir, PhasesDir),
		filepath.Join(p.KyoteeDir, PhasesDir, "context"),
		filepath.Join(p.KyoteeDir, PhasesDir, "plan"),
		filepath.Join(p.KyoteeDir, PhasesDir, "implement"),
		filepath.Join(p.KyoteeDir, PhasesDir, "verify"),
		filepath.Join(p.KyoteeDir, PhasesDir, "deliver"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create %s: %w", dir, err)
		}
	}

	// Create .gitignore if it doesn't exist
	gitignorePath := filepath.Join(p.KyoteeDir, ".gitignore")
	if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
		content := "# Kyotee working files\nphases/\n"
		os.WriteFile(gitignorePath, []byte(content), 0644)
	}

	return nil
}

// HasConversation returns true if there's an existing conversation
func (p *Project) HasConversation() bool {
	return p.Conversation != nil && len(p.Conversation.Messages) > 0
}

// HasSpec returns true if a spec has been generated
func (p *Project) HasSpec() bool {
	return p.Spec != nil && p.Spec.Goal != ""
}

// HasJob returns true if a job has been started
func (p *Project) HasJob() bool {
	return p.Job != nil
}

// CanResume returns true if there's a resumable job
func (p *Project) CanResume() bool {
	if p.Job == nil {
		return false
	}
	return p.Job.Status == JobStatusPaused ||
	       p.Job.Status == JobStatusRunning ||
	       p.Job.Status == JobStatusDiscovery
}

// StartConversation initializes a new conversation
func (p *Project) StartConversation() {
	p.Conversation = &Conversation{
		Messages:  []Message{},
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

// AddMessage adds a message to the conversation
func (p *Project) AddMessage(role, content string) {
	if p.Conversation == nil {
		p.StartConversation()
	}

	p.Conversation.Messages = append(p.Conversation.Messages, Message{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	})
	p.Conversation.UpdatedAt = time.Now()
	p.saveConversation()
}

// SetSpec sets the project specification
func (p *Project) SetSpec(specData map[string]any) {
	p.Spec = &Spec{
		Raw:       specData,
		CreatedAt: time.Now(),
	}

	// Extract known fields
	if v, ok := specData["project_name"].(string); ok {
		p.Spec.ProjectName = v
	}
	if v, ok := specData["goal"].(string); ok {
		p.Spec.Goal = v
	}
	if v, ok := specData["language"].(string); ok {
		p.Spec.Language = v
	}
	if v, ok := specData["framework"].(string); ok {
		p.Spec.Framework = v
	}
	if v, ok := specData["skill"].(string); ok {
		p.Spec.Skill = v
	}
	if features, ok := specData["features"].([]any); ok {
		for _, f := range features {
			if s, ok := f.(string); ok {
				p.Spec.Features = append(p.Spec.Features, s)
			}
		}
	}

	p.saveSpec()
}

// StartJob initializes a new job
func (p *Project) StartJob() {
	p.Job = &Job{
		Status:       JobStatusRunning,
		CurrentPhase: 0,
		Phases: []PhaseState{
			{ID: "context", Status: "pending"},
			{ID: "plan", Status: "pending"},
			{ID: "implement", Status: "pending"},
			{ID: "verify", Status: "pending"},
			{ID: "deliver", Status: "pending"},
		},
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	p.saveJob()
}

// UpdatePhase updates a phase's state
func (p *Project) UpdatePhase(phaseID, status string) {
	if p.Job == nil {
		return
	}

	for i := range p.Job.Phases {
		if p.Job.Phases[i].ID == phaseID {
			p.Job.Phases[i].Status = status
			if status == "running" {
				p.Job.Phases[i].StartedAt = time.Now()
				p.Job.CurrentPhase = i
			} else if status == "passed" || status == "failed" {
				p.Job.Phases[i].EndedAt = time.Now()
			}
			break
		}
	}
	p.Job.UpdatedAt = time.Now()
	p.saveJob()
}

// SetJobStatus updates the overall job status
func (p *Project) SetJobStatus(status JobStatus, err string) {
	if p.Job == nil {
		return
	}
	p.Job.Status = status
	p.Job.Error = err
	p.Job.UpdatedAt = time.Now()
	p.saveJob()
}

// PhaseDir returns the directory for a phase's iteration
func (p *Project) PhaseDir(phaseID string, iteration int) string {
	return filepath.Join(p.KyoteeDir, PhasesDir, phaseID, fmt.Sprintf("iter_%d", iteration))
}

// File operations

func (p *Project) loadConversation() {
	path := filepath.Join(p.KyoteeDir, ConversationFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var conv Conversation
	if json.Unmarshal(data, &conv) == nil {
		p.Conversation = &conv
	}
}

func (p *Project) saveConversation() {
	if p.Conversation == nil {
		return
	}
	path := filepath.Join(p.KyoteeDir, ConversationFile)
	data, _ := json.MarshalIndent(p.Conversation, "", "  ")
	os.WriteFile(path, data, 0644)
}

func (p *Project) loadSpec() {
	path := filepath.Join(p.KyoteeDir, SpecFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var spec Spec
	if json.Unmarshal(data, &spec) == nil {
		p.Spec = &spec
	}
}

func (p *Project) saveSpec() {
	if p.Spec == nil {
		return
	}
	path := filepath.Join(p.KyoteeDir, SpecFile)
	data, _ := json.MarshalIndent(p.Spec, "", "  ")
	os.WriteFile(path, data, 0644)
}

func (p *Project) loadJob() {
	path := filepath.Join(p.KyoteeDir, JobFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var job Job
	if json.Unmarshal(data, &job) == nil {
		p.Job = &job
	}
}

func (p *Project) saveJob() {
	if p.Job == nil {
		return
	}
	path := filepath.Join(p.KyoteeDir, JobFile)
	data, _ := json.MarshalIndent(p.Job, "", "  ")
	os.WriteFile(path, data, 0644)
}
