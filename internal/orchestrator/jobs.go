package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/stukennedy/kyotee/internal/types"
)

// JobState represents a persisted job state
type JobState struct {
	ID           string            `json:"id"`
	ProjectName  string            `json:"project_name"`
	Task         string            `json:"task"`
	Spec         map[string]any    `json:"spec"`
	Status       string            `json:"status"` // running, paused, completed, failed
	CurrentPhase int               `json:"current_phase"`
	Phases       []JobPhaseState   `json:"phases"`
	StartTime    time.Time         `json:"start_time"`
	EndTime      time.Time         `json:"end_time,omitempty"`
	Error        string            `json:"error,omitempty"`
	RepoRoot     string            `json:"repo_root"`
	AgentDir     string            `json:"agent_dir"`
}

// JobPhaseState represents the state of a single phase
type JobPhaseState struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"` // pending, running, passed, failed, checkpoint
	Iteration int       `json:"iteration"`
	StartTime time.Time `json:"start_time,omitempty"`
	EndTime   time.Time `json:"end_time,omitempty"`
	Output    []string  `json:"output,omitempty"`
	// Checkpoint state for mid-build pauses
	ChunkIndex            int               `json:"chunk_index,omitempty"`
	TotalChunks           int               `json:"total_chunks,omitempty"`
	Checkpoint            *types.Checkpoint  `json:"checkpoint,omitempty"`
	CheckpointResolutions []string           `json:"checkpoint_resolutions,omitempty"`
}

// JobSummary is a lightweight job listing
type JobSummary struct {
	ID          string    `json:"id"`
	ProjectName string    `json:"project_name"`
	Task        string    `json:"task"`
	Status      string    `json:"status"`
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time,omitempty"`
}

// SaveJobState saves job state to disk
func SaveJobState(agentDir string, state *JobState) error {
	runsDir := filepath.Join(agentDir, "runs")
	if err := os.MkdirAll(runsDir, 0755); err != nil {
		return fmt.Errorf("failed to create runs dir: %w", err)
	}

	// Use the job ID as the directory name
	jobDir := filepath.Join(runsDir, state.ID)
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		return fmt.Errorf("failed to create job dir: %w", err)
	}

	statePath := filepath.Join(jobDir, "state.json")
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	if err := os.WriteFile(statePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write state: %w", err)
	}

	return nil
}

// LoadJobState loads job state from disk
func LoadJobState(agentDir, jobID string) (*JobState, error) {
	statePath := filepath.Join(agentDir, "runs", jobID, "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read state: %w", err)
	}

	var state JobState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse state: %w", err)
	}

	return &state, nil
}

// ListJobs returns all jobs, sorted by start time (newest first)
func ListJobs(agentDir string) ([]JobSummary, error) {
	runsDir := filepath.Join(agentDir, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read runs dir: %w", err)
	}

	var jobs []JobSummary
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		statePath := filepath.Join(runsDir, entry.Name(), "state.json")
		data, err := os.ReadFile(statePath)
		if err != nil {
			continue // Skip jobs without state
		}

		var state JobState
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}

		// Truncate task for summary
		task := state.Task
		if len(task) > 60 {
			task = task[:57] + "..."
		}

		jobs = append(jobs, JobSummary{
			ID:          state.ID,
			ProjectName: state.ProjectName,
			Task:        task,
			Status:      state.Status,
			StartTime:   state.StartTime,
			EndTime:     state.EndTime,
		})
	}

	// Sort by start time, newest first
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].StartTime.After(jobs[j].StartTime)
	})

	return jobs, nil
}

// ResolveCheckpoint resolves a checkpoint on a job, allowing it to continue
func ResolveCheckpoint(agentDir, jobID, resolution string) error {
	state, err := LoadJobState(agentDir, jobID)
	if err != nil {
		return err
	}
	if state.Status != "checkpoint" {
		return fmt.Errorf("job %s is not at a checkpoint (status: %s)", jobID, state.Status)
	}

	// Find the phase with the checkpoint
	for i := range state.Phases {
		if state.Phases[i].Checkpoint != nil && state.Phases[i].Checkpoint.Resolution == "" {
			state.Phases[i].Checkpoint.Resolution = resolution
			state.Phases[i].Checkpoint.ResolvedAt = time.Now().Format(time.RFC3339)
			state.Phases[i].CheckpointResolutions = append(
				state.Phases[i].CheckpointResolutions,
				fmt.Sprintf("[%s] %s → %s", state.Phases[i].Checkpoint.Type, state.Phases[i].Checkpoint.Message, resolution),
			)
			state.Phases[i].Status = "running"
			state.Phases[i].Checkpoint = nil
			break
		}
	}

	state.Status = "running"
	return SaveJobState(agentDir, state)
}

// DeleteJob removes a job and its artifacts
func DeleteJob(agentDir, jobID string) error {
	jobDir := filepath.Join(agentDir, "runs", jobID)
	return os.RemoveAll(jobDir)
}

// NewJobState creates a new job state
func NewJobState(projectName, task string, spec map[string]any, repoRoot, agentDir string) *JobState {
	id := time.Now().Format("20060102-150405")

	return &JobState{
		ID:          id,
		ProjectName: projectName,
		Task:        task,
		Spec:        spec,
		Status:      "running",
		StartTime:   time.Now(),
		RepoRoot:    repoRoot,
		AgentDir:    agentDir,
		Phases: []JobPhaseState{
			{ID: "context", Status: "pending"},
			{ID: "plan", Status: "pending"},
			{ID: "implement", Status: "pending"},
			{ID: "verify", Status: "pending"},
			{ID: "deliver", Status: "pending"},
		},
	}
}
