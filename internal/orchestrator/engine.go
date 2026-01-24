package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/stukennedy/kyotee/internal/config"
	"github.com/stukennedy/kyotee/internal/types"
)

// ErrPaused is returned when execution is paused
var ErrPaused = fmt.Errorf("execution paused")

// LoadSpecWithOverrides loads spec and applies overrides from discovery
func LoadSpecWithOverrides(agentDir string, overrides map[string]any) (*types.Spec, error) {
	specPath := filepath.Join(agentDir, "spec.toml")
	spec, err := config.LoadSpec(specPath)
	if err != nil {
		return nil, err
	}

	// Apply any overrides from discovery spec
	// (For now, we just use the base spec)

	return spec, nil
}

// Engine orchestrates the phase execution
type Engine struct {
	Spec      *types.Spec
	Task      string
	RepoRoot  string
	AgentDir  string
	RunDir    string
	State     *types.RunState
	OnOutput  func(phase string, text string)
	OnPhase   func(phaseIdx int, status types.PhaseStatus)
	OnNarrate func(text string)
}

// NewEngine creates a new orchestrator engine with auto-generated run directory
func NewEngine(spec *types.Spec, task, repoRoot, agentDir string) (*Engine, error) {
	// Create run directory in agentDir/runs
	ts := time.Now().Format("20060102-150405")
	runDir := filepath.Join(agentDir, "runs", ts)
	return NewEngineWithRunDir(spec, task, repoRoot, agentDir, runDir)
}

// NewEngineWithRunDir creates an engine with a specific run directory
// This is used when storing phase outputs in project-local .kyotee/phases/
func NewEngineWithRunDir(spec *types.Spec, task, repoRoot, agentDir, runDir string) (*Engine, error) {
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create run dir: %w", err)
	}

	// Initialize phase states
	phases := make([]types.PhaseState, len(spec.Phases))
	for i, p := range spec.Phases {
		phases[i] = types.PhaseState{
			Phase:  p,
			Status: types.PhasePending,
		}
	}

	state := &types.RunState{
		Task:   task,
		Spec:   spec,
		Phases: phases,
		RunDir: runDir,
	}

	// Save task to run dir
	if err := os.WriteFile(filepath.Join(runDir, "task.txt"), []byte(task), 0644); err != nil {
		return nil, fmt.Errorf("failed to save task: %w", err)
	}

	return &Engine{
		Spec:     spec,
		Task:     task,
		RepoRoot: repoRoot,
		AgentDir: agentDir,
		RunDir:   runDir,
		State:    state,
	}, nil
}

// Run executes the orchestration loop without context (uses background context)
func (e *Engine) Run() error {
	return e.RunWithContext(context.Background())
}

// RunWithContext executes the orchestration loop with cancellation support
func (e *Engine) RunWithContext(ctx context.Context) error {
	for e.State.CurrentPhase < len(e.State.Phases) {
		// Check for cancellation at the start of each phase
		select {
		case <-ctx.Done():
			return ErrPaused
		default:
		}

		if e.State.TotalIterations >= e.Spec.Limits.MaxTotalIterations {
			return fmt.Errorf("reached max total iterations (%d)", e.Spec.Limits.MaxTotalIterations)
		}

		phase := &e.State.Phases[e.State.CurrentPhase]
		phase.Iteration++
		e.State.TotalIterations++

		if phase.Iteration > e.Spec.Limits.MaxPhaseIterations {
			return fmt.Errorf("reached max iterations for phase %s (%d)", phase.Phase.ID, e.Spec.Limits.MaxPhaseIterations)
		}

		// Update status
		phase.Status = types.PhaseRunning
		if e.OnPhase != nil {
			e.OnPhase(e.State.CurrentPhase, types.PhaseRunning)
		}

		// Execute phase
		if err := e.executePhaseWithContext(ctx, phase); err != nil {
			phase.Status = types.PhaseFailed
			phase.Error = err
			if e.OnPhase != nil {
				e.OnPhase(e.State.CurrentPhase, types.PhaseFailed)
			}
			return err
		}

		// Special handling for verify phase
		if phase.Phase.ID == "verify" {
			allPassed, err := e.runGates(phase)
			if err != nil {
				return err
			}
			if !allPassed {
				// Loop back to implement
				phase.Status = types.PhaseFailed
				if e.OnPhase != nil {
					e.OnPhase(e.State.CurrentPhase, types.PhaseFailed)
				}
				e.State.CurrentPhase = e.findPhaseIndex("implement")
				continue
			}
		}

		phase.Status = types.PhasePassed
		if e.OnPhase != nil {
			e.OnPhase(e.State.CurrentPhase, types.PhasePassed)
		}
		e.State.CurrentPhase++
	}

	return nil
}

func (e *Engine) executePhaseWithContext(ctx context.Context, phase *types.PhaseState) error {
	// Build prompt
	prompt, err := e.buildPrompt(phase.Phase.ID, phase.Phase.SchemaPath)
	if err != nil {
		return fmt.Errorf("failed to build prompt: %w", err)
	}

	// Create phase output directory
	phaseDir := filepath.Join(e.RunDir, phase.Phase.ID, fmt.Sprintf("iter_%d", phase.Iteration))
	if err := os.MkdirAll(phaseDir, 0755); err != nil {
		return fmt.Errorf("failed to create phase dir: %w", err)
	}

	// Call worker with context for cancellation support
	output, err := e.callWorkerWithContext(ctx, prompt, phase.Phase.ID)
	if err != nil {
		return fmt.Errorf("worker failed: %w", err)
	}

	// Save raw output
	if err := os.WriteFile(filepath.Join(phaseDir, "worker_output.txt"), []byte(output), 0644); err != nil {
		return fmt.Errorf("failed to save output: %w", err)
	}

	// Extract and validate JSON
	controlJSON, err := extractJSON(output)
	if err != nil {
		return fmt.Errorf("failed to extract JSON: %w", err)
	}

	// Validate against schema
	schema, err := config.LoadSchema(e.AgentDir, phase.Phase.SchemaPath)
	if err != nil {
		return fmt.Errorf("failed to load schema: %w", err)
	}
	if err := config.ValidateJSON(schema, controlJSON); err != nil {
		return err
	}

	phase.ControlJSON = controlJSON
	phase.Output = output

	// Extract narration if present
	if narration, ok := controlJSON["narration"].(string); ok && narration != "" {
		phase.Narration = narration
		if e.OnNarrate != nil {
			e.OnNarrate(narration)
		}
		// Save narration
		if err := os.WriteFile(filepath.Join(phaseDir, "ralph.md"), []byte(narration+"\n"), 0644); err != nil {
			return fmt.Errorf("failed to save narration: %w", err)
		}
	}

	// Save validated control JSON
	controlBytes, _ := json.MarshalIndent(controlJSON, "", "  ")
	if err := os.WriteFile(filepath.Join(phaseDir, "control.json"), controlBytes, 0644); err != nil {
		return fmt.Errorf("failed to save control: %w", err)
	}

	// Apply file changes for implement phase
	if phase.Phase.ID == "implement" {
		if err := e.applyFileChanges(controlJSON); err != nil {
			return fmt.Errorf("failed to apply changes: %w", err)
		}
	}

	return nil
}

func (e *Engine) applyFileChanges(control map[string]any) error {
	files, ok := control["files"].([]any)
	if !ok {
		return nil // No files to apply
	}

	for _, f := range files {
		file, ok := f.(map[string]any)
		if !ok {
			continue
		}

		path, _ := file["path"].(string)
		action, _ := file["action"].(string)
		content, _ := file["content"].(string)

		if path == "" {
			continue
		}

		fullPath := filepath.Join(e.RepoRoot, path)

		switch action {
		case "create", "modify":
			// Ensure directory exists
			dir := filepath.Dir(fullPath)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", dir, err)
			}
			// Write file
			if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
				return fmt.Errorf("failed to write file %s: %w", path, err)
			}
			if e.OnOutput != nil {
				e.OnOutput("implement", fmt.Sprintf("  Created/modified: %s\n", path))
			}

		case "delete":
			if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("failed to delete file %s: %w", path, err)
			}
			if e.OnOutput != nil {
				e.OnOutput("implement", fmt.Sprintf("  Deleted: %s\n", path))
			}
		}
	}

	return nil
}

func (e *Engine) buildPrompt(phaseID, schemaPath string) (string, error) {
	systemPrompt, err := config.LoadPrompt(e.AgentDir, "system")
	if err != nil {
		return "", err
	}

	phasePrompt, err := config.LoadPrompt(e.AgentDir, "phase_"+phaseID)
	if err != nil {
		return "", err
	}

	schemaContent, err := config.GetSchemaContent(e.AgentDir, schemaPath)
	if err != nil {
		return "", err
	}

	diff := e.getGitDiff()
	if diff == "" {
		diff = "<none>"
	}

	return strings.Join([]string{
		systemPrompt,
		phasePrompt,
		"TASK:\n" + e.Task,
		"CURRENT_GIT_DIFF:\n" + diff,
		"REQUIRED JSON SCHEMA:\n" + schemaContent,
		"INSTRUCTIONS:\nReturn ONLY a valid JSON object matching the schema above. No markdown, no explanation, just the JSON.",
	}, "\n\n"), nil
}

func (e *Engine) callWorker(prompt, phaseID string) (string, error) {
	return e.callWorkerWithContext(context.Background(), prompt, phaseID)
}

func (e *Engine) callWorkerWithContext(ctx context.Context, prompt, phaseID string) (string, error) {
	cmd := exec.CommandContext(ctx, "claude", "-p")
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Dir = e.RepoRoot

	output, err := cmd.Output()
	if err != nil {
		// Check if it was cancelled
		if ctx.Err() == context.Canceled {
			return "", ErrPaused
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("claude exited with code %d: %s", exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return "", err
	}

	result := string(output)

	// Stream output to callback
	if e.OnOutput != nil {
		e.OnOutput(phaseID, result)
	}

	return result, nil
}

func (e *Engine) runGates(phase *types.PhaseState) (bool, error) {
	allPassed := true
	var results []types.GateResult

	gateDir := filepath.Join(e.RunDir, phase.Phase.ID, fmt.Sprintf("iter_%d", phase.Iteration), "gate_outputs")
	if err := os.MkdirAll(gateDir, 0755); err != nil {
		return false, fmt.Errorf("failed to create gate dir: %w", err)
	}

	for _, checkName := range e.Spec.Gates.RequiredChecks {
		cmdStr, ok := e.Spec.Commands[checkName]
		if !ok {
			return false, fmt.Errorf("missing command for gate: %s", checkName)
		}

		if e.OnOutput != nil {
			e.OnOutput("verify", fmt.Sprintf("> Running gate: %s\n", checkName))
		}

		cmd := exec.Command("sh", "-c", cmdStr)
		cmd.Dir = e.RepoRoot
		output, err := cmd.CombinedOutput()

		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			}
		}

		outFile := filepath.Join(gateDir, checkName+".log")
		if err := os.WriteFile(outFile, output, 0644); err != nil {
			return false, fmt.Errorf("failed to save gate output: %w", err)
		}

		passed := exitCode == 0
		if !passed {
			allPassed = false
			if e.OnOutput != nil {
				e.OnOutput("verify", fmt.Sprintf("  FAILED: %s (exit %d)\n", checkName, exitCode))
			}
		} else {
			if e.OnOutput != nil {
				e.OnOutput("verify", fmt.Sprintf("  PASSED: %s\n", checkName))
			}
		}

		results = append(results, types.GateResult{
			Name:      checkName,
			Command:   cmdStr,
			ExitCode:  exitCode,
			OutputRef: filepath.Base(outFile),
			Passed:    passed,
		})
	}

	return allPassed, nil
}

func (e *Engine) findPhaseIndex(id string) int {
	for i, p := range e.State.Phases {
		if p.Phase.ID == id {
			return i
		}
	}
	return 0
}

func (e *Engine) getGitDiff() string {
	// Get both staged and unstaged changes, plus untracked files
	var result strings.Builder

	// Regular diff
	cmd := exec.Command("git", "diff")
	cmd.Dir = e.RepoRoot
	if output, err := cmd.Output(); err == nil && len(output) > 0 {
		result.WriteString("=== Modified files ===\n")
		result.Write(output)
	}

	// Staged diff
	cmd = exec.Command("git", "diff", "--cached")
	cmd.Dir = e.RepoRoot
	if output, err := cmd.Output(); err == nil && len(output) > 0 {
		result.WriteString("\n=== Staged files ===\n")
		result.Write(output)
	}

	// List untracked files in src/
	cmd = exec.Command("git", "ls-files", "--others", "--exclude-standard", "src/")
	cmd.Dir = e.RepoRoot
	if output, err := cmd.Output(); err == nil && len(output) > 0 {
		result.WriteString("\n=== New untracked files ===\n")
		result.Write(output)
	}

	return result.String()
}

var (
	markdownJSONRe = regexp.MustCompile(`(?s)` + "```" + `(?:json)?\s*(\{.*?\})\s*` + "```")
	rawJSONRe      = regexp.MustCompile(`(?s)(\{.*\})`)
)

func extractJSON(text string) (map[string]any, error) {
	// Try markdown code blocks first
	if m := markdownJSONRe.FindStringSubmatch(text); len(m) > 1 {
		var result map[string]any
		if err := json.Unmarshal([]byte(m[1]), &result); err == nil {
			return result, nil
		}
	}

	// Fall back to raw JSON
	if m := rawJSONRe.FindStringSubmatch(text); len(m) > 1 {
		var result map[string]any
		if err := json.Unmarshal([]byte(m[1]), &result); err != nil {
			return nil, fmt.Errorf("JSON parse error: %w", err)
		}
		return result, nil
	}

	return nil, fmt.Errorf("no JSON object found in output")
}
