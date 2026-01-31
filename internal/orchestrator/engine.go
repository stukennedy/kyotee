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

// ErrCheckpoint is returned when a checkpoint is hit
var ErrCheckpoint = fmt.Errorf("checkpoint requested")

const checkpointPromptInstructions = `INSTRUCTIONS:
Implement ONLY the steps listed above. Return ONLY a valid JSON object matching the schema. No markdown, no explanation, just the JSON.

CHECKPOINTS: If you need human input before continuing, return a JSON object with a "checkpoint" field instead of the normal output:
- Human verification needed: {"checkpoint": {"type": "human-verify", "message": "I've set up X — does it look right?"}}
- Decision needed: {"checkpoint": {"type": "decision", "message": "Should I use A or B?", "options": ["Option A: ...", "Option B: ..."]}}
- Human action needed: {"checkpoint": {"type": "human-action", "message": "Please run 'vercel login' and authenticate"}}
Use checkpoints SPARINGLY — only for architectural decisions, visual verification, or actions only a human can do. Keep implementing for routine work.`

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
			// Check if this is a checkpoint (not a failure)
			if cpErr, ok := err.(*types.ErrCheckpoint); ok {
				_ = cpErr
				// Leave phase as running — it will resume after checkpoint resolution
				return err
			}
			phase.Status = types.PhaseFailed
			phase.Error = err
			if e.OnPhase != nil {
				e.OnPhase(e.State.CurrentPhase, types.PhaseFailed)
			}
			return err
		}

		// Special handling for verify phase
		if phase.Phase.ID == "verify" {
			gatesPassed, err := e.runGates(phase)
			if err != nil {
				return err
			}

			// Run goal-backward verification after gates
			gbResult, gbErr := e.runGoalBackwardVerification()
			if gbErr != nil {
				return fmt.Errorf("goal-backward verification error: %w", gbErr)
			}

			// Log goal-backward results
			if e.OnOutput != nil {
				e.OnOutput("verify", fmt.Sprintf("\n🔍 %s\n", gbResult.Summary))
				for _, c := range gbResult.Checks {
					if !c.Passed {
						e.OnOutput("verify", fmt.Sprintf("  ✗ [%s] %s: %s\n", c.Category, c.File, c.Detail))
					}
				}
			}

			// Save goal-backward report
			gbDir := filepath.Join(e.RunDir, phase.Phase.ID, fmt.Sprintf("iter_%d", phase.Iteration))
			if reportBytes, err := json.MarshalIndent(gbResult, "", "  "); err == nil {
				_ = os.WriteFile(filepath.Join(gbDir, "goal_backward.json"), reportBytes, 0644)
			}

			allPassed := gatesPassed && gbResult.AllPassed
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
	// For implement phase, use chunked execution if plan output is available
	if phase.Phase.ID == "implement" {
		planPhase := e.findPhaseByID("plan")
		if planPhase != nil && planPhase.ControlJSON != nil {
			if steps, err := e.extractPlanSteps(planPhase.ControlJSON); err == nil && len(steps) > 0 {
				return e.executeChunkedImplement(ctx, phase)
			}
		}
		// Fall through to single-call if no plan steps available
	}

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

	// Apply file changes for implement phase (non-chunked fallback)
	if phase.Phase.ID == "implement" {
		if err := e.applyFileChanges(controlJSON); err != nil {
			return fmt.Errorf("failed to apply changes: %w", err)
		}
	}

	return nil
}

// executeChunkedImplement runs the implement phase as multiple sub-plan chunks
func (e *Engine) executeChunkedImplement(ctx context.Context, phase *types.PhaseState) error {
	// Get the plan phase output
	planPhase := e.findPhaseByID("plan")
	if planPhase == nil || planPhase.ControlJSON == nil {
		return fmt.Errorf("plan phase output not found, cannot chunk implement")
	}

	// Extract steps from plan
	steps, err := e.extractPlanSteps(planPhase.ControlJSON)
	if err != nil {
		return fmt.Errorf("failed to extract plan steps: %w", err)
	}

	if len(steps) == 0 {
		return fmt.Errorf("plan has no steps to implement")
	}

	// Group steps into chunks of 2-3
	chunks := chunkSteps(steps, 3)

	phase.SubPlan = &types.SubPlanState{
		TotalChunks: len(chunks),
		StepGroups:  chunks,
	}

	if e.OnOutput != nil {
		e.OnOutput("implement", fmt.Sprintf("📦 Split plan into %d chunks (%d steps total)\n", len(chunks), len(steps)))
	}

	// Execute each chunk
	var allFiles []any
	var allEvidence []any
	var allNotes []string

	for i, chunk := range chunks {
		select {
		case <-ctx.Done():
			return ErrPaused
		default:
		}

		phase.SubPlan.ChunkIndex = i

		if e.OnOutput != nil {
			var stepIDs []string
			for _, s := range chunk {
				stepIDs = append(stepIDs, s.ID)
			}
			e.OnOutput("implement", fmt.Sprintf("\n🔨 Chunk %d/%d: steps [%s]\n", i+1, len(chunks), strings.Join(stepIDs, ", ")))
		}

		// Build chunk-specific prompt
		prompt, err := e.buildChunkPrompt(chunk, phase.SubPlan)
		if err != nil {
			return fmt.Errorf("failed to build chunk %d prompt: %w", i+1, err)
		}

		// Create chunk output directory
		chunkDir := filepath.Join(e.RunDir, "implement", fmt.Sprintf("iter_%d", phase.Iteration), fmt.Sprintf("chunk_%d", i))
		if err := os.MkdirAll(chunkDir, 0755); err != nil {
			return fmt.Errorf("failed to create chunk dir: %w", err)
		}

		// Call worker with fresh context
		output, err := e.callWorkerWithContext(ctx, prompt, "implement")
		if err != nil {
			return fmt.Errorf("chunk %d worker failed: %w", i+1, err)
		}

		// Save raw output
		if err := os.WriteFile(filepath.Join(chunkDir, "worker_output.txt"), []byte(output), 0644); err != nil {
			return fmt.Errorf("failed to save chunk output: %w", err)
		}

		// Extract JSON
		controlJSON, err := extractJSON(output)
		if err != nil {
			return fmt.Errorf("chunk %d: failed to extract JSON: %w", i+1, err)
		}

		// Check for checkpoint before validation (checkpoint may not match implement schema)
		if cp := detectCheckpoint(output, controlJSON); cp != nil {
			phase.SubPlan.Checkpoint = cp
			if e.OnOutput != nil {
				icon := "🔍"
				switch cp.Type {
				case types.CheckpointDecision:
					icon = "🤔"
				case types.CheckpointAction:
					icon = "👤"
				}
				e.OnOutput("implement", fmt.Sprintf("\n%s CHECKPOINT [%s]: %s\n", icon, cp.Type, cp.Message))
				if len(cp.Options) > 0 {
					for j, opt := range cp.Options {
						e.OnOutput("implement", fmt.Sprintf("  %d) %s\n", j+1, opt))
					}
				}
			}
			// Save checkpoint state
			cpBytes, _ := json.MarshalIndent(cp, "", "  ")
			_ = os.WriteFile(filepath.Join(chunkDir, "checkpoint.json"), cpBytes, 0644)
			return &types.ErrCheckpoint{Checkpoint: *cp}
		}

		// Validate against implement schema
		schema, err := config.LoadSchema(e.AgentDir, phase.Phase.SchemaPath)
		if err != nil {
			return fmt.Errorf("failed to load schema: %w", err)
		}
		if err := config.ValidateJSON(schema, controlJSON); err != nil {
			return fmt.Errorf("chunk %d schema validation failed: %w", i+1, err)
		}

		// Save control JSON
		controlBytes, _ := json.MarshalIndent(controlJSON, "", "  ")
		if err := os.WriteFile(filepath.Join(chunkDir, "control.json"), controlBytes, 0644); err != nil {
			return fmt.Errorf("failed to save chunk control: %w", err)
		}

		// Apply file changes immediately
		if err := e.applyFileChanges(controlJSON); err != nil {
			return fmt.Errorf("chunk %d: failed to apply changes: %w", i+1, err)
		}

		// Accumulate results
		if files, ok := controlJSON["files"].([]any); ok {
			allFiles = append(allFiles, files...)
			for _, f := range files {
				if fm, ok := f.(map[string]any); ok {
					if p, ok := fm["path"].(string); ok {
						phase.SubPlan.CompletedFiles = append(phase.SubPlan.CompletedFiles, p)
					}
				}
			}
		}
		if evidence, ok := controlJSON["evidence"].([]any); ok {
			allEvidence = append(allEvidence, evidence...)
		}
		if notes, ok := controlJSON["notes"].(string); ok && notes != "" {
			allNotes = append(allNotes, fmt.Sprintf("Chunk %d: %s", i+1, notes))
		}

		// Extract narration
		if narration, ok := controlJSON["narration"].(string); ok && narration != "" {
			if e.OnNarrate != nil {
				e.OnNarrate(narration)
			}
		}

		// Refresh cumulative diff for next chunk
		phase.SubPlan.CumulativeDiff = e.getGitDiff()
	}

	// Combine all chunk results into final phase output
	combined := map[string]any{
		"phase":    "implement",
		"files":    allFiles,
		"notes":    strings.Join(allNotes, "\n"),
		"evidence": allEvidence,
	}
	phase.ControlJSON = combined

	// Save combined output
	phaseDir := filepath.Join(e.RunDir, "implement", fmt.Sprintf("iter_%d", phase.Iteration))
	combinedBytes, _ := json.MarshalIndent(combined, "", "  ")
	if err := os.WriteFile(filepath.Join(phaseDir, "control.json"), combinedBytes, 0644); err != nil {
		return fmt.Errorf("failed to save combined control: %w", err)
	}

	return nil
}

// buildChunkPrompt builds a prompt for a specific chunk of the implement phase
func (e *Engine) buildChunkPrompt(chunk []types.PlanStep, subPlan *types.SubPlanState) (string, error) {
	systemPrompt, err := config.LoadPrompt(e.AgentDir, "system")
	if err != nil {
		return "", err
	}

	phasePrompt, err := config.LoadPrompt(e.AgentDir, "phase_implement")
	if err != nil {
		return "", err
	}

	schemaContent, err := config.GetSchemaContent(e.AgentDir, "schemas/implement_output.schema.json")
	if err != nil {
		return "", err
	}

	// Build step descriptions
	var stepDescs []string
	for _, s := range chunk {
		stepDescs = append(stepDescs, fmt.Sprintf("### Step %s: %s\nActions: %s\nExpected files: %s",
			s.ID, s.Goal,
			strings.Join(s.Actions, "; "),
			strings.Join(s.ExpectedFiles, ", ")))
	}

	// Build cumulative context
	var cumulativeCtx string
	if len(subPlan.CompletedFiles) > 0 {
		cumulativeCtx = fmt.Sprintf("FILES ALREADY CREATED/MODIFIED IN PREVIOUS CHUNKS:\n%s\n\nGIT DIFF SO FAR:\n%s",
			strings.Join(subPlan.CompletedFiles, "\n"),
			subPlan.CumulativeDiff)
	} else {
		cumulativeCtx = "This is the first chunk. No prior changes."
	}

	diff := e.getGitDiff()
	if diff == "" {
		diff = "<none>"
	}

	// Include checkpoint resolution context if any
	var checkpointCtx string
	if len(subPlan.CheckpointResolutions) > 0 {
		checkpointCtx = "PREVIOUS CHECKPOINT RESOLUTIONS (human decisions/approvals):\n" +
			strings.Join(subPlan.CheckpointResolutions, "\n")
	}

	parts := []string{}

	// Prepend AGENTS.md if available (primary context)
	if agentsContent, err := LoadAgentsFile(e.RepoRoot); err == nil && agentsContent != "" {
		parts = append(parts, agentsContent)
	}

	parts = append(parts,
		systemPrompt,
		phasePrompt,
		"TASK:\n"+e.Task,
		fmt.Sprintf("CHUNK %d of %d — IMPLEMENT ONLY THESE STEPS:\n%s",
			subPlan.ChunkIndex+1, subPlan.TotalChunks,
			strings.Join(stepDescs, "\n\n")),
		"CUMULATIVE CONTEXT:\n"+cumulativeCtx,
	)
	if checkpointCtx != "" {
		parts = append(parts, checkpointCtx)
	}
	parts = append(parts,
		"CURRENT_GIT_DIFF:\n"+diff,
		"REQUIRED JSON SCHEMA:\n"+schemaContent,
		checkpointPromptInstructions,
	)

	return strings.Join(parts, "\n\n"), nil
}

// extractPlanSteps extracts structured steps from plan phase output
func (e *Engine) extractPlanSteps(planJSON map[string]any) ([]types.PlanStep, error) {
	plan, ok := planJSON["plan"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("plan field not found in plan output")
	}

	stepsRaw, ok := plan["steps"].([]any)
	if !ok {
		return nil, fmt.Errorf("steps field not found in plan output")
	}

	var steps []types.PlanStep
	for _, s := range stepsRaw {
		sm, ok := s.(map[string]any)
		if !ok {
			continue
		}

		step := types.PlanStep{
			ID:   getString(sm, "id"),
			Goal: getString(sm, "goal"),
		}

		if actions, ok := sm["actions"].([]any); ok {
			for _, a := range actions {
				if str, ok := a.(string); ok {
					step.Actions = append(step.Actions, str)
				}
			}
		}
		if files, ok := sm["expected_files"].([]any); ok {
			for _, f := range files {
				if str, ok := f.(string); ok {
					step.ExpectedFiles = append(step.ExpectedFiles, str)
				}
			}
		}
		if checks, ok := sm["checks"].([]any); ok {
			for _, c := range checks {
				if str, ok := c.(string); ok {
					step.Checks = append(step.Checks, str)
				}
			}
		}

		steps = append(steps, step)
	}

	return steps, nil
}

func (e *Engine) findPhaseByID(id string) *types.PhaseState {
	for i := range e.State.Phases {
		if e.State.Phases[i].Phase.ID == id {
			return &e.State.Phases[i]
		}
	}
	return nil
}

// chunkSteps groups plan steps into chunks of at most chunkSize
func chunkSteps(steps []types.PlanStep, chunkSize int) [][]types.PlanStep {
	var chunks [][]types.PlanStep
	for i := 0; i < len(steps); i += chunkSize {
		end := i + chunkSize
		if end > len(steps) {
			end = len(steps)
		}
		chunks = append(chunks, steps[i:end])
	}
	return chunks
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

	parts := []string{}

	// Prepend AGENTS.md if available (primary context)
	if agentsContent, err := LoadAgentsFile(e.RepoRoot); err == nil && agentsContent != "" {
		parts = append(parts, agentsContent)
	}

	parts = append(parts,
		systemPrompt,
		phasePrompt,
		"TASK:\n"+e.Task,
		"CURRENT_GIT_DIFF:\n"+diff,
		"REQUIRED JSON SCHEMA:\n"+schemaContent,
		"INSTRUCTIONS:\nReturn ONLY a valid JSON object matching the schema above. No markdown, no explanation, just the JSON.",
	)

	return strings.Join(parts, "\n\n"), nil
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

// detectCheckpoint looks for a checkpoint signal in LLM output.
// The LLM can emit: {"checkpoint": {"type": "decision", "message": "...", "options": [...]}}
// This can appear in the output text or inside the control JSON's "checkpoint" field.
func detectCheckpoint(output string, controlJSON map[string]any) *types.Checkpoint {
	// Check control JSON first (preferred — structured)
	if cp, ok := controlJSON["checkpoint"].(map[string]any); ok {
		checkpoint := &types.Checkpoint{}
		if t, ok := cp["type"].(string); ok {
			checkpoint.Type = types.CheckpointType(t)
		}
		if m, ok := cp["message"].(string); ok {
			checkpoint.Message = m
		}
		if opts, ok := cp["options"].([]any); ok {
			for _, o := range opts {
				if s, ok := o.(string); ok {
					checkpoint.Options = append(checkpoint.Options, s)
				}
			}
		}
		if checkpoint.Message != "" {
			return checkpoint
		}
	}

	// Also scan raw output for checkpoint JSON block
	checkpointRe := regexp.MustCompile(`(?s)\{"checkpoint"\s*:\s*(\{[^}]+\})\}`)
	if m := checkpointRe.FindStringSubmatch(output); len(m) > 1 {
		var cp map[string]any
		if err := json.Unmarshal([]byte(m[1]), &cp); err == nil {
			checkpoint := &types.Checkpoint{}
			if t, ok := cp["type"].(string); ok {
				checkpoint.Type = types.CheckpointType(t)
			}
			if msg, ok := cp["message"].(string); ok {
				checkpoint.Message = msg
			}
			if opts, ok := cp["options"].([]any); ok {
				for _, o := range opts {
					if s, ok := o.(string); ok {
						checkpoint.Options = append(checkpoint.Options, s)
					}
				}
			}
			if checkpoint.Message != "" {
				return checkpoint
			}
		}
	}

	return nil
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
