package main

import (
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/stukennedy/kyotee/internal/config"
	"github.com/stukennedy/kyotee/internal/embedded"
	"github.com/stukennedy/kyotee/internal/orchestrator"
	"github.com/stukennedy/kyotee/internal/paths"
	"github.com/stukennedy/kyotee/internal/tui"
	"github.com/stukennedy/kyotee/internal/types"
)

var (
	appPaths *paths.Paths
	task     string
)

func main() {
	// Resolve paths early
	var err error
	appPaths, err = paths.Resolve()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

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
  kyotee run -t "..." Run a task directly (skip discovery)
  kyotee init         Initialize .kyotee in current project`,
		PersistentPreRunE: ensureInitialized,
		RunE:              runDiscovery,
	}

	// Run command - direct task execution
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run a task directly (skip discovery)",
		RunE:  runTask,
	}
	runCmd.Flags().StringVarP(&task, "task", "t", "", "Task description")
	runCmd.MarkFlagRequired("task")

	// Jobs command - list jobs
	jobsCmd := &cobra.Command{
		Use:   "jobs",
		Short: "List all jobs",
		Long:  "Show all previous jobs with their status. Use 'kyotee resume <id>' to continue a paused job.",
		RunE:  listJobs,
	}

	// Resume command - resume a job
	resumeCmd := &cobra.Command{
		Use:   "resume <job-id>",
		Short: "Resume a paused or failed job",
		Args:  cobra.ExactArgs(1),
		RunE:  resumeJob,
	}

	// Init command - initialize project-local .kyotee
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize .kyotee in current project",
		Long:  "Creates a .kyotee directory in the current project for project-specific configuration.",
		RunE:  initProject,
	}

	rootCmd.AddCommand(runCmd, jobsCmd, resumeCmd, initCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// ensureInitialized runs before any command to set up ~/.kyotee if needed
func ensureInitialized(cmd *cobra.Command, args []string) error {
	// Create directory structure
	if err := appPaths.EnsureUserDir(); err != nil {
		return err
	}

	// Install defaults on first run
	if !appPaths.IsInitialized() {
		fmt.Println("🐺 First run - setting up ~/.kyotee...")
		if err := embedded.Install(appPaths.UserDir); err != nil {
			return fmt.Errorf("failed to install defaults: %w", err)
		}
		fmt.Println("✓ Ready!")
		fmt.Println()
	}

	return nil
}

// runDiscovery starts the interactive discovery mode
func runDiscovery(cmd *cobra.Command, args []string) error {
	// Use NewAppForProject to enable state persistence
	app := tui.NewAppForProject(appPaths.UserDir, appPaths.WorkDir)
	p := tea.NewProgram(&app, tea.WithAltScreen())
	app.SetProgram(p)

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}

// runTask runs a task directly without discovery
func runTask(cmd *cobra.Command, args []string) error {
	specPath := appPaths.EffectiveSpecPath()

	// Load spec
	spec, err := config.LoadSpec(specPath)
	if err != nil {
		return err
	}

	// Create engine
	engine, err := orchestrator.NewEngine(spec, task, appPaths.WorkDir, appPaths.UserDir)
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
		fmt.Printf("💭 Kyotee: %s\n", text)
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
	jobs, err := orchestrator.ListJobs(appPaths.UserDir)
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

	// Load job state
	jobState, err := orchestrator.LoadJobState(appPaths.UserDir, jobID)
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
	app := tui.NewAppWithJob(appPaths.UserDir, jobState.RepoRoot, jobState)
	p := tea.NewProgram(app, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}

// initProject creates a .kyotee directory in the current project
func initProject(cmd *cobra.Command, args []string) error {
	if appPaths.HasProjectConfig() {
		fmt.Println(".kyotee already exists in this directory")
		return nil
	}

	// Create project directory structure
	dirs := []string{
		appPaths.ProjectDir,
		appPaths.ProjectSkills,
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create %s: %w", dir, err)
		}
	}

	// Create a basic spec.toml
	specContent := `# Project-specific kyotee configuration
# Uncomment and customize as needed

# [limits]
# max_phase_iterations = 3
# max_total_iterations = 10

# [gates]
# required_checks = ["syntax_check"]

# [commands]
# syntax_check = "echo 'Add your syntax check command'"
# test = "echo 'Add your test command'"
`

	specPath := filepath.Join(appPaths.ProjectDir, "spec.toml")
	if err := os.WriteFile(specPath, []byte(specContent), 0644); err != nil {
		return fmt.Errorf("failed to write spec.toml: %w", err)
	}

	// Create .gitignore for the .kyotee directory
	gitignore := `# Ignore run artifacts
runs/
`
	gitignorePath := filepath.Join(appPaths.ProjectDir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(gitignore), 0644); err != nil {
		return fmt.Errorf("failed to write .gitignore: %w", err)
	}

	fmt.Println("✓ Created .kyotee/ in current directory")
	fmt.Println()
	fmt.Println("Files created:")
	fmt.Println("  .kyotee/spec.toml   - Project configuration")
	fmt.Println("  .kyotee/skills/     - Project-specific skills")
	fmt.Println("  .kyotee/.gitignore  - Excludes run artifacts")
	fmt.Println()
	fmt.Println("Edit .kyotee/spec.toml to customize gates, commands, etc.")

	return nil
}
