package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/stukennedy/kyotee/internal/config"
	"github.com/stukennedy/kyotee/internal/embedded"
	"github.com/stukennedy/kyotee/internal/orchestrator"
	"github.com/stukennedy/kyotee/internal/paths"
	"github.com/stukennedy/kyotee/internal/tui"
	"github.com/stukennedy/kyotee/internal/types"

	"golang.org/x/term"
)

var (
	appPaths  *paths.Paths
	task      string
	ralphMode bool
)

func main() {
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

	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run a task directly (skip discovery)",
		RunE:  runTask,
	}
	runCmd.Flags().StringVarP(&task, "task", "t", "", "Task description")
	runCmd.Flags().BoolVar(&ralphMode, "ralph", false, "Use Ralph Wiggum pattern (fresh context each iteration)")
	runCmd.MarkFlagRequired("task")

	ralphCmd := &cobra.Command{
		Use:   "ralph",
		Short: "Run task using Ralph Wiggum pattern (fresh context each iteration)",
		Long: `Ralph Wiggum pattern: Each iteration runs in a fresh context window.
State persists on disk, not in conversation history.
The model stays sharp because context never accumulates.

This is the recommended mode for long-running or complex tasks.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ralphMode = true
			return runRalph(cmd, args)
		},
	}
	ralphCmd.Flags().StringVarP(&task, "task", "t", "", "Task description")
	ralphCmd.MarkFlagRequired("task")

	jobsCmd := &cobra.Command{
		Use:   "jobs",
		Short: "List all jobs",
		RunE:  listJobs,
	}

	resumeCmd := &cobra.Command{
		Use:   "resume <job-id>",
		Short: "Resume a paused or failed job",
		Args:  cobra.ExactArgs(1),
		RunE:  resumeJob,
	}

	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize .kyotee in current project",
		RunE:  initProject,
	}

	rootCmd.AddCommand(runCmd, ralphCmd, jobsCmd, resumeCmd, initCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func ensureInitialized(cmd *cobra.Command, args []string) error {
	if err := appPaths.EnsureUserDir(); err != nil {
		return err
	}
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

func runDiscovery(cmd *cobra.Command, args []string) error {
	// Set terminal to raw mode for Tooey
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %v", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	app := tui.NewAppForProject(appPaths.UserDir, appPaths.WorkDir)
	if err := app.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}
	return nil
}

func runTask(cmd *cobra.Command, args []string) error {
	// If Ralph mode is enabled, use the Ralph runner
	if ralphMode {
		return runRalph(cmd, args)
	}

	specPath := appPaths.EffectiveSpecPath()
	spec, err := config.LoadSpec(specPath)
	if err != nil {
		return err
	}

	engine, err := orchestrator.NewEngine(spec, task, appPaths.WorkDir, appPaths.UserDir)
	if err != nil {
		return err
	}

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

func runRalph(cmd *cobra.Command, args []string) error {
	fmt.Println("🐺 KYOTEE - Ralph Wiggum Mode")
	fmt.Println("Fresh context each iteration. State persists on disk.")
	fmt.Println()
	fmt.Printf("Task: %s\n", task)
	fmt.Printf("Working dir: %s\n\n", appPaths.WorkDir)

	// Create autonomous engine for Ralph mode
	engine := orchestrator.NewAutonomousEngine(nil, task, appPaths.WorkDir, appPaths.UserDir)

	engine.OnOutput = func(text string) {
		fmt.Print(text)
	}
	engine.OnPhase = func(phase, status string) {
		fmt.Printf("\n[%s] %s\n", phase, status)
	}
	engine.OnTool = func(name string, input any) {
		// Tool call notification (already handled in OnOutput)
	}

	// Run with Ralph pattern
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	if err := engine.RunRalph(ctx); err != nil {
		return fmt.Errorf("ralph execution failed: %w", err)
	}

	fmt.Println("\n✓ Done!")
	return nil
}

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

		timeStr := job.StartTime.Format("Jan 02 15:04")
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

func resumeJob(cmd *cobra.Command, args []string) error {
	jobID := args[0]

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

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %v", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	app := tui.NewAppWithJob(appPaths.UserDir, jobState.RepoRoot, jobState)
	if err := app.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}
	return nil
}

func initProject(cmd *cobra.Command, args []string) error {
	if appPaths.HasProjectConfig() {
		fmt.Println(".kyotee already exists in this directory")
		return nil
	}

	dirs := []string{
		appPaths.ProjectDir,
		appPaths.ProjectSkills,
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create %s: %w", dir, err)
		}
	}

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
