package main

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/pipeline"
	"github.com/spf13/cobra"
)

// newRunCommand builds the `run` subcommand: a single fetch/filter/store
// cycle, intended as the cron entrypoint.
func newRunCommand() *cobra.Command {
	var (
		dryRun  bool
		jsonOut bool
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run one fetch/filter/store cycle",
		Long: "Run a single fetch/filter/store cycle. This is the cron entrypoint.\n\n" +
			"Exit status: 0 success (message-level errors are reported, not fatal),\n" +
			"1 config or validation error, 2 provider or auth failure, 3 another\n" +
			"instance holds the cycle lock.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Nothing below is a usage problem, so never answer with usage.
			cmd.SilenceUsage = true

			configPath, err := cmd.Flags().GetString("config")
			if err != nil {
				return err
			}
			return runCycle(cmd, configPath, dryRun, jsonOut)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "evaluate rules and report what would be written without writing anything")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "write a machine-readable manifest of the cycle to stdout, one JSON object per account")

	return cmd
}

// loadRunner is the prologue every command that runs cycles shares: load and
// validate the config, report its warnings, and build a Runner over it.
//
// `run`, `daemon` and `mcp` all start this way and must not drift apart — a
// warning one of them swallows, or a config error one of them grades
// differently, is a bug the other two would hide. Both returns are useful: the
// Runner does the work, and the Config is what `daemon` logs and what the MCP
// server reads rules and destinations from.
//
// Every failure is graded ExitConfig: nothing here has reached a provider yet.
//
// "No config file at the resolved path" is checked first and separately, so
// first contact with an unconfigured machine produces setup guidance rather
// than an open(2) failure from the YAML decoder.
func loadRunner(configPath string, dryRun bool) (*config.Config, *pipeline.Runner, error) {
	if err := requireConfigFile(configPath); err != nil {
		return nil, nil, err
	}

	cfg, problems, err := config.LoadAndValidate(configPath)
	if err != nil {
		return nil, nil, configFailure(configPath, cfg, err)
	}
	// Warnings do not stop a run, but the user should hear about them. They go
	// to slog (stderr), so `--json` keeps stdout parseable — and so the MCP
	// server's stdout carries protocol frames and nothing else.
	for _, p := range problems.Warnings() {
		slog.Warn("config", "problem", p.String())
	}

	runner, err := pipeline.NewRunner(pipeline.Options{
		Config: cfg,
		DryRun: dryRun,
	})
	if err != nil {
		return nil, nil, &pipeline.ExitCodeError{Code: pipeline.ExitConfig, Err: err}
	}
	return cfg, runner, nil
}

// loadRunnerFor is loadRunner plus the guidance a command can only print once
// it has a writer: the partially-configured states — no rules, an account that
// has never been authorized — that are legal but almost certainly not what the
// user meant.
//
// The advice goes to stderr, never stdout, so `run --json` stays parseable and
// the MCP server's stdout carries protocol frames and nothing else.
func loadRunnerFor(cmd *cobra.Command, configPath string, dryRun bool) (*config.Config, *pipeline.Runner, error) {
	cfg, runner, err := loadRunner(configPath, dryRun)
	if err != nil {
		return nil, nil, err
	}
	reportSetupAdvice(cmd.ErrOrStderr(), cfg)
	return cfg, runner, nil
}

// runCycle loads the config, runs exactly one cycle, and renders the result.
//
// It is kept separate from the cobra plumbing so the command layer owns only
// the flags: every decision that `daemon` and the MCP server must make the same
// way (grading errors, rendering manifests) lives in internal/pipeline.
func runCycle(cmd *cobra.Command, configPath string, dryRun, jsonOut bool) error {
	cfg, runner, err := loadRunnerFor(cmd, configPath, dryRun)
	if err != nil {
		return err
	}

	manifests, cycleErr := runner.Cycle(cmd.Context())

	// Report before deciding the exit status: a cycle that died partway still
	// created files, and the caller must not lose track of them.
	writeManifests(cmd.OutOrStdout(), manifests, jsonOut)

	return cycleFailure(cfg, cycleErr)
}

// writeManifests renders one cycle's manifests to stdout: newline-delimited
// JSON under --json, one human summary line per account otherwise.
func writeManifests(w io.Writer, manifests []pipeline.Manifest, jsonOut bool) {
	for _, m := range manifests {
		if jsonOut {
			if err := pipeline.WriteJSON(w, m); err != nil {
				slog.Error("writing manifest", "account", m.Account, "error", err)
			}
			continue
		}
		fmt.Fprintln(w, m.Line())
	}
}
