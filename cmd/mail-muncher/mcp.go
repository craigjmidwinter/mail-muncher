package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/craigjmidwinter/mail-muncher/internal/mcpserver"
	"github.com/craigjmidwinter/mail-muncher/internal/pipeline"
)

// newMCPCommand builds the `mcp` subcommand: an MCP server over the stored
// mail archive, speaking stdio.
func newMCPCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the stored mail archive to agents over MCP (stdio)",
		Long: "Serve the stored mail archive over the Model Context Protocol, on stdin\n" +
			"and stdout. This is the transport local agent runtimes speak, so the\n" +
			"command is meant to be launched by a client rather than run by hand.\n\n" +
			"Five tools are exposed: list_messages, read_message, search_messages,\n" +
			"list_rules and sync. Everything but sync is read-only, and sync runs the\n" +
			"same cycle `run` does — it can only ever add files, and it takes the same\n" +
			"cross-process lock.\n\n" +
			"Only files under a configured rule `dest` are readable. stdout carries\n" +
			"protocol frames and nothing else; all logging goes to stderr, so\n" +
			"--log-level debug is safe to leave on.\n\n" +
			"Exit status: 0 the client disconnected or a signal stopped the server,\n" +
			"1 config or validation error.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Nothing below is a usage problem, so never answer with usage.
			cmd.SilenceUsage = true

			configPath, err := cmd.Flags().GetString("config")
			if err != nil {
				return err
			}

			// A client that stops the server sends a signal rather than closing
			// the pipe, so honour both: the context cancels the session, and
			// stop() restores the default disposition so a second signal still
			// kills a wedged process.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			return runMCP(ctx, cmd, configPath)
		},
	}

	return cmd
}

// runMCP loads the config, builds the server, and serves until the client goes
// away.
//
// The config is read once, at startup, exactly as `daemon` reads it: editing
// config.yml needs a restart, while the domain files it points at are resolved
// on every list_rules call. Failing here — before a client has connected — is
// what turns a broken config into a startup error the user can see rather than
// a tool error buried in an agent transcript.
func runMCP(ctx context.Context, cmd *cobra.Command, configPath string) error {
	cfg, runner, err := loadRunnerFor(cmd, configPath, false)
	if err != nil {
		return mcpStartupFailure(ctx, cmd, err)
	}

	srv, err := mcpserver.New(mcpserver.Options{
		Config:  cfg,
		Runner:  runner,
		Logger:  slog.Default(),
		Version: version,
	})
	if err != nil {
		return &pipeline.ExitCodeError{Code: pipeline.ExitConfig, Err: err}
	}

	// slog is on stderr (see newRootCommand), which is the only place anything
	// human may go while the stdio transport owns stdout.
	slog.Info("mcp server ready",
		"transport", "stdio",
		"accounts", len(cfg.Accounts),
		"rules", len(cfg.Rules),
		"tools", len(srv.ToolNames()),
	)

	// InOrStdin/OutOrStdout are the real os.Stdin and os.Stdout in production;
	// routing through cobra keeps the streams injectable for tests.
	if err := srv.ServeStdio(ctx, cmd.InOrStdin(), cmd.OutOrStdout()); err != nil {
		return err
	}
	slog.Info("mcp server stopped")
	return nil
}
