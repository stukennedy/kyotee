// Kyotee is a multi-model AI harness: a receptionist routes each task to a
// solving strategy (solo, two-brain, council) across vendor-diverse models,
// with structural fast/slow thinking gates, budget enforcement, and a Tooey
// TUI observing everything over SSE.
package main

import (
	"context"
	"fmt"
	"net"
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

	// ask is the Skill shim (spec 09): a stateless HTTP client for a running
	// engine. --local runs an in-process engine instead (no daemon needed).
	var strategy, thinkingMode, consensusMethod, urlFlag string
	var maxCost float64
	var councilRounds int
	var doWait, jsonOut, local bool
	ask := &cobra.Command{
		Use:   "ask [prompt]",
		Short: "Submit a task to a running engine and print the answer",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prompt := strings.Join(args, " ")
			ov := receptionist.Overrides{
				Strategy: strategy, Thinking: thinkingMode, BudgetUSD: maxCost,
				CouncilRounds: councilRounds, ConsensusMethod: consensusMethod,
			}
			if local {
				// Serve the in-process engine on an ephemeral port and run
				// the same client path, so --json/--wait/exit codes behave
				// identically to the remote shim (spec 09 contract).
				eng, _, err := buildEngine(configPath)
				if err != nil {
					return err
				}
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					return err
				}
				srv := &http.Server{Handler: eng.Handler()}
				go srv.Serve(ln)
				defer srv.Close()
				return runRemoteAsk("http://"+ln.Addr().String(), prompt, ov, true, jsonOut, os.Stdout, os.Stderr)
			}
			return runRemoteAsk(engineURL(urlFlag), prompt, ov, doWait, jsonOut, os.Stdout, os.Stderr)
		},
	}
	ask.Flags().StringVar(&strategy, "strategy", "", "force strategy: solo|twobrain|council")
	ask.Flags().StringVar(&thinkingMode, "thinking", "", "force thinking mode: fast|slow|auto")
	ask.Flags().Float64Var(&maxCost, "budget", 0, "per-task budget ceiling in USD")
	ask.Flags().IntVar(&councilRounds, "council-rounds", 0, "override council rounds")
	ask.Flags().StringVar(&consensusMethod, "consensus", "", "override consensus method: vote|similarity|judge")
	ask.Flags().BoolVar(&doWait, "wait", false, "stream progress to stderr and block until the answer; without it, print task_id and return")
	ask.Flags().BoolVar(&jsonOut, "json", false, "print the stable JSON result contract")
	ask.Flags().BoolVar(&local, "local", false, "run an in-process engine instead of connecting to one")
	ask.Flags().StringVar(&urlFlag, "url", "", "engine base URL (default $KYOTEE_URL or "+defaultEngineURL+")")

	var resumeWait, resumeJSON bool
	var resumeURL string
	resumeCmd := &cobra.Command{
		Use:   "resume <task_id>",
		Short: "Resume a persisted task on a running engine",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRemoteResume(engineURL(resumeURL), args[0], resumeWait, resumeJSON, os.Stdout, os.Stderr)
		},
	}
	resumeCmd.Flags().BoolVar(&resumeWait, "wait", false, "stream progress and block until the task finishes")
	resumeCmd.Flags().BoolVar(&resumeJSON, "json", false, "print the stable JSON result contract")
	resumeCmd.Flags().StringVar(&resumeURL, "url", "", "engine base URL")

	var statusURL string
	statusCmd := &cobra.Command{
		Use:   "status <task_id>",
		Short: "Print a task's persisted State snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRemoteStatus(engineURL(statusURL), args[0], os.Stdout)
		},
	}
	statusCmd.Flags().StringVar(&statusURL, "url", "", "engine base URL")

	var providersURL string
	providersCmd := &cobra.Command{
		Use:   "providers",
		Short: "List the engine's registered models, capabilities, and costs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRemoteProviders(engineURL(providersURL), os.Stdout)
		},
	}
	providersCmd.Flags().StringVar(&providersURL, "url", "", "engine base URL")

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

	root.AddCommand(serve, tuiCmd, ask, resumeCmd, statusCmd, providersCmd, initCmd, configCmd)
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

// configYAML serialises the default config for `kyotee init`.
func configYAML() ([]byte, error) {
	return yaml.Marshal(config.Default())
}
