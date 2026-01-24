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

Use 'kyotee run --task "..."' to skip discovery and run a task directly.`,
		RunE: runDiscovery,
	}

	rootCmd.Flags().StringVarP(&specPath, "spec", "s", "agent/spec.toml", "Path to spec.toml")
	rootCmd.Flags().StringVarP(&repoPath, "repo", "r", ".", "Path to repository root")

	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run a task directly (skip discovery)",
		RunE:  runTask,
	}

	runCmd.Flags().StringVarP(&specPath, "spec", "s", "agent/spec.toml", "Path to spec.toml")
	runCmd.Flags().StringVarP(&repoPath, "repo", "r", ".", "Path to repository root")
	runCmd.Flags().StringVarP(&task, "task", "t", "", "Task description")
	runCmd.MarkFlagRequired("task")

	rootCmd.AddCommand(runCmd)

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
