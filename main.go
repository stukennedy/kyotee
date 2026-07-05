// Kyotee is a multi-model AI harness: a receptionist routes each task to a
// solving strategy (solo, two-brain, council) across vendor-diverse models,
// with structural fast/slow thinking gates, budget enforcement, and a Tooey
// TUI observing everything over SSE.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/stukennedy/kyotee/internal/config"
	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/receptionist"
	"github.com/stukennedy/kyotee/internal/server"
	"github.com/stukennedy/kyotee/internal/state"
	"github.com/stukennedy/kyotee/internal/tui"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var configPath string

	root := &cobra.Command{
		Use:   "kyotee",
		Short: "Multi-model AI harness: route, think, debate, budget",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Default: serve the engine in-process and attach the TUI.
			eng, cfg, err := buildEngine(configPath)
			if err != nil {
				return err
			}
			srv := &http.Server{Addr: cfg.Listen, Handler: eng.Handler()}
			go func() {
				if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					fmt.Fprintln(os.Stderr, "engine:", err)
				}
			}()
			defer srv.Shutdown(context.Background())
			time.Sleep(100 * time.Millisecond) // let the listener come up
			return tui.Run(cmd.Context(), "http://"+cfg.Listen)
		},
	}
	root.PersistentFlags().StringVar(&configPath, "config", "", "config file (default ~/.kyotee/config.yaml)")

	serve := &cobra.Command{
		Use:   "serve",
		Short: "Run the engine HTTP/SSE server (headless)",
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, cfg, err := buildEngine(configPath)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			srv := &http.Server{Addr: cfg.Listen, Handler: eng.Handler()}
			go func() {
				<-ctx.Done()
				shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				srv.Shutdown(shutCtx)
			}()
			fmt.Println("kyotee engine listening on", cfg.Listen)
			if err := srv.ListenAndServe(); err != http.ErrServerClosed {
				return err
			}
			return nil
		},
	}

	var attachURL string
	tuiCmd := &cobra.Command{
		Use:   "tui",
		Short: "Attach the TUI to a running engine",
		RunE: func(cmd *cobra.Command, args []string) error {
			return tui.Run(cmd.Context(), attachURL)
		},
	}
	tuiCmd.Flags().StringVar(&attachURL, "url", "http://127.0.0.1:8484", "engine base URL")

	var strategy, thinkingMode string
	var maxCost float64
	ask := &cobra.Command{
		Use:   "ask [prompt]",
		Short: "Run one task in-process and print the answer (no TUI)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, _, err := buildEngine(configPath)
			if err != nil {
				return err
			}
			prompt := strings.Join(args, " ")
			ov := receptionist.Overrides{Strategy: strategy, Thinking: thinkingMode, BudgetUSD: maxCost}
			return askOnce(cmd.Context(), eng, prompt, ov)
		},
	}
	ask.Flags().StringVar(&strategy, "strategy", "", "force strategy: solo|twobrain|council")
	ask.Flags().StringVar(&thinkingMode, "thinking", "", "force thinking mode: fast|slow|auto")
	ask.Flags().Float64Var(&maxCost, "budget", 0, "per-task budget ceiling in USD")

	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Write the default config to ~/.kyotee/config.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := configPath
			if path == "" {
				path = config.DefaultPath()
			}
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists", path)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			data, err := configYAML()
			if err != nil {
				return err
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				return err
			}
			fmt.Println("wrote", path)
			return nil
		},
	}

	// config validate <file>: pre-flight the same validation hot-reload runs
	// (spec 07 §3); prints errors and exits non-zero on invalid config.
	configCmd := &cobra.Command{Use: "config", Short: "Config utilities"}
	configCmd.AddCommand(&cobra.Command{
		Use:   "validate [file]",
		Short: "Validate a config file and exit non-zero on errors",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := configPath
			if len(args) > 0 {
				path = args[0]
			}
			if path == "" {
				path = config.DefaultPath()
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			cfg, err := config.Parse(data)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			for _, w := range cfg.Warnings() {
				fmt.Fprintln(os.Stderr, "warning:", w)
			}
			fmt.Println(path, "is valid")
			return nil
		},
	})

	root.AddCommand(serve, tuiCmd, ask, initCmd, configCmd)
	return root
}

func buildEngine(configPath string) (*server.Engine, *config.Config, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, err
	}
	for _, w := range cfg.Warnings() {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	store, err := state.NewFileStore(cfg.StateDir)
	if err != nil {
		return nil, nil, err
	}
	eng := server.NewEngine(cfg, store)
	eng.ConfigPath = configPath
	return eng, cfg, nil
}

// askOnce submits a task, tails its events to stderr, and prints the final
// answer to stdout.
func askOnce(ctx context.Context, eng *server.Engine, prompt string, ov receptionist.Overrides) error {
	taskID, err := eng.Submit(prompt, ov)
	if err != nil {
		return err
	}
	ch, cancel := eng.Bus.Subscribe(taskID)
	defer cancel()

	w := bufio.NewWriter(os.Stderr)
	defer w.Flush()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-ch:
			switch ev.Kind {
			case events.KindTaskClassified, events.KindTaskRouted, events.KindThinkingMode,
				events.KindThinkingToolChk, events.KindToolCall, events.KindBudgetWarn,
				events.KindCouncilConsensus, events.KindError:
				payload, _ := json.Marshal(ev.Payload)
				fmt.Fprintf(w, "· %-20s %s\n", ev.Kind, payload)
				w.Flush()
			case events.KindTaskFinal:
				text, _ := ev.Payload["text"].(string)
				cost, _ := ev.Payload["total_cost_usd"].(float64)
				fmt.Println(text)
				fmt.Fprintf(w, "— total cost $%.4f\n", cost)
				return nil
			}
		}
	}
}

// configYAML serialises the default config for `kyotee init`.
func configYAML() ([]byte, error) {
	return yaml.Marshal(config.Default())
}
