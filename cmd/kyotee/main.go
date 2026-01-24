package main

import (
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/stukennedy/kyotee/internal/config"
	"github.com/stukennedy/kyotee/internal/orchestrator"
	"github.com/stukennedy/kyotee/internal/tui"
	"github.com/stukennedy/kyotee/internal/types"
)

var (
	specPath string
	repoPath string
	task     string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "kyotee",
		Short: "Kyotee - Interactive AI agent orchestrator",
		Long: `Kyotee is an interactive AI assistant that helps you define and build software projects.

Launch without arguments to start an interactive discovery session where Kyotee
will ask questions to understand your project before building it.

Commands:
  kyotee              Start interactive discovery mode
  kyotee jobs         List all jobs (recent builds)
  kyotee resume <id>  Resume a paused or failed job
  kyotee run -t "..." Run a task directly (skip discovery)`,
		RunE: runDiscovery,
	}

	rootCmd.Flags().StringVarP(&specPath, "spec", "s", "agent/spec.toml", "Path to spec.toml")
	rootCmd.Flags().StringVarP(&repoPath, "repo", "r", ".", "Path to repository root")

	// Run command - direct task execution
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run a task directly (skip discovery)",
		RunE:  runTask,
	}
	runCmd.Flags().StringVarP(&specPath, "spec", "s", "agent/spec.toml", "Path to spec.toml")
	runCmd.Flags().StringVarP(&repoPath, "repo", "r", ".", "Path to repository root")
	runCmd.Flags().StringVarP(&task, "task", "t", "", "Task description")
	runCmd.MarkFlagRequired("task")

	// Jobs command - list jobs
	jobsCmd := &cobra.Command{
		Use:   "jobs",
		Short: "List all jobs",
		Long:  "Show all previous jobs with their status. Use 'kyotee resume <id>' to continue a paused job.",
		RunE:  listJobs,
	}
	jobsCmd.Flags().StringVarP(&specPath, "spec", "s", "agent/spec.toml", "Path to spec.toml")

	// Resume command - resume a job
	resumeCmd := &cobra.Command{
		Use:   "resume <job-id>",
		Short: "Resume a paused or failed job",
		Args:  cobra.ExactArgs(1),
		RunE:  resumeJob,
	}
	resumeCmd.Flags().StringVarP(&specPath, "spec", "s", "agent/spec.toml", "Path to spec.toml")

	rootCmd.AddCommand(runCmd, jobsCmd, resumeCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runDiscovery starts the interactive discovery mode
func runDiscovery(cmd *cobra.Command, args []string) error {
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return fmt.Errorf("invalid repo path: %w", err)
	}

	absSpec, err := filepath.Abs(specPath)
	if err != nil {
		return fmt.Errorf("invalid spec path: %w", err)
	}

	agentDir := filepath.Dir(absSpec)

	// Start TUI in discovery mode
	app := tui.NewApp(agentDir, absRepo)
	p := tea.NewProgram(app, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}

// runTask runs a task directly without discovery
func runTask(cmd *cobra.Command, args []string) error {
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return fmt.Errorf("invalid repo path: %w", err)
	}

	absSpec, err := filepath.Abs(specPath)
	if err != nil {
		return fmt.Errorf("invalid spec path: %w", err)
	}

	agentDir := filepath.Dir(absSpec)

	// Load spec
	spec, err := config.LoadSpec(absSpec)
	if err != nil {
		return err
	}

	// Create engine
	engine, err := orchestrator.NewEngine(spec, task, absRepo, agentDir)
	if err != nil {
		return err
	}

	// Simple console output
	engine.OnOutput = func(phase, text string) {
		fmt.Printf("[%s] %s", phase, text)
	}
	engine.OnPhase = func(idx int, status types.PhaseStatus) {
		fmt.Printf("[phase] %s: %s\n", engine.State.Phases[idx].Phase.ID, status)
	}
	engine.OnNarrate = func(text string) {
		fmt.Printf("💭 Ralph: %s\n", text)
	}

	fmt.Printf("🐺 KYOTEE - Starting run: %s\n", engine.RunDir)
	fmt.Printf("Task: %s\n\n", engine.Task)

	if err := engine.Run(); err != nil {
		return err
	}

	fmt.Printf("\n✓ Done! Artifacts in: %s\n", engine.RunDir)
	return nil
}

// listJobs shows all previous jobs
func listJobs(cmd *cobra.Command, args []string) error {
	absSpec, err := filepath.Abs(specPath)
	if err != nil {
		return fmt.Errorf("invalid spec path: %w", err)
	}
	agentDir := filepath.Dir(absSpec)

	jobs, err := orchestrator.ListJobs(agentDir)
	if err != nil {
		return err
	}

	if len(jobs) == 0 {
		fmt.Println("No jobs found. Start a new project with 'kyotee'")
		return nil
	}

	fmt.Println("🐺 KYOTEE - Jobs")
	fmt.Println()

	for _, job := range jobs {
		// Status icon
		icon := "○"
		switch job.Status {
		case "completed":
			icon = "✓"
		case "failed":
			icon = "✗"
		case "paused":
			icon = "⏸"
		case "running":
			icon = "●"
		}

		// Format time
		timeStr := job.StartTime.Format("Jan 02 15:04")

		// Project name
		project := job.ProjectName
		if project == "" || project == "." {
			project = "(current dir)"
		}

		fmt.Printf("  %s %s  %s  %s\n", icon, job.ID, project, job.Status)
		fmt.Printf("    %s\n", job.Task)
		fmt.Printf("    Started: %s\n", timeStr)
		fmt.Println()
	}

	fmt.Println("Resume a job with: kyotee resume <job-id>")
	return nil
}

// resumeJob resumes a paused or failed job
func resumeJob(cmd *cobra.Command, args []string) error {
	jobID := args[0]

	absSpec, err := filepath.Abs(specPath)
	if err != nil {
		return fmt.Errorf("invalid spec path: %w", err)
	}
	agentDir := filepath.Dir(absSpec)

	// Load job state
	jobState, err := orchestrator.LoadJobState(agentDir, jobID)
	if err != nil {
		return fmt.Errorf("failed to load job %s: %w", jobID, err)
	}

	if jobState.Status == "completed" {
		fmt.Printf("Job %s is already completed.\n", jobID)
		return nil
	}

	fmt.Printf("🐺 KYOTEE - Resuming job: %s\n", jobID)
	fmt.Printf("Project: %s\n", jobState.ProjectName)
	fmt.Printf("Task: %s\n\n", jobState.Task)

	// Start TUI with the loaded job state
	app := tui.NewAppWithJob(agentDir, jobState.RepoRoot, jobState)
	p := tea.NewProgram(app, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}
