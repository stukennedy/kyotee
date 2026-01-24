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
	noTUI    bool
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "kyotee",
		Short: "Kyotee - AI agent orchestrator with a terminal UI",
		Long: `Kyotee is a deterministic CLI orchestrator for AI agent workflows.
It runs phases as a state machine, calls Claude Code as a worker,
enforces strict JSON outputs, and provides a cyberpunk terminal experience.`,
	}

	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run an orchestrated task",
		RunE:  runTask,
	}

	runCmd.Flags().StringVarP(&specPath, "spec", "s", "agent/spec.toml", "Path to spec.toml")
	runCmd.Flags().StringVarP(&repoPath, "repo", "r", ".", "Path to repository root")
	runCmd.Flags().StringVarP(&task, "task", "t", "", "Task description")
	runCmd.Flags().BoolVar(&noTUI, "no-tui", false, "Run without TUI (plain output)")
	runCmd.MarkFlagRequired("task")

	rootCmd.AddCommand(runCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runTask(cmd *cobra.Command, args []string) error {
	// Resolve paths
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

	if noTUI {
		return runWithoutTUI(engine)
	}

	return runWithTUI(engine)
}

func runWithTUI(engine *orchestrator.Engine) error {
	model := tui.New(engine)
	p := tea.NewProgram(model, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}

func runWithoutTUI(engine *orchestrator.Engine) error {
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
