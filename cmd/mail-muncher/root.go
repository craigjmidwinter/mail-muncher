package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// version is the CLI version. It is overridable at build time via
//
//	-ldflags "-X main.version=v1.2.3"
var version = "dev"

// notImplementedError is returned by command stubs that have not been built
// yet. main() maps it to exit status 1.
type notImplementedError struct {
	command string
}

func (e *notImplementedError) Error() string {
	return fmt.Sprintf("%s: not implemented", e.command)
}

func notImplemented(cmd *cobra.Command) error {
	// Cobra would otherwise print usage for any returned error; these stubs
	// are not usage problems.
	cmd.SilenceUsage = true
	fmt.Fprintln(cmd.OutOrStdout(), "not implemented")
	return &notImplementedError{command: cmd.Name()}
}

// defaultConfigPath returns the default config file location,
// ~/.config/mail-muncher/config.yml. If the home directory cannot be
// determined the path is returned relative to the current directory.
func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".config", "mail-muncher", "config.yml")
	}
	return filepath.Join(home, ".config", "mail-muncher", "config.yml")
}

// parseLogLevel maps a human log level name to a slog.Level.
func parseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("invalid log level %q (want one of: debug, info, warn, error)", s)
	}
}

// rootOptions holds the values bound to the root command's persistent flags.
type rootOptions struct {
	configPath string
	logLevel   string
}

// newRootCommand builds the `mail-muncher` root command and attaches every
// subcommand. It is a constructor (rather than a package-level var) so tests
// can build an isolated command tree.
func newRootCommand() *cobra.Command {
	opts := &rootOptions{}

	root := &cobra.Command{
		Use:   "mail-muncher",
		Short: "Pull mail from a provider, filter it, and write matches to disk",
		Long: "mail-muncher fetches messages from a mail provider, evaluates each one\n" +
			"against configurable rules, and writes the matches to the filesystem.",
		Version:       version,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			level, err := parseLogLevel(opts.logLevel)
			if err != nil {
				return err
			}
			handler := slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: level})
			slog.SetDefault(slog.New(handler))
			return nil
		},
	}

	root.PersistentFlags().StringVar(&opts.configPath, "config", defaultConfigPath(), "path to the config file")
	root.PersistentFlags().StringVar(&opts.logLevel, "log-level", "info", "log verbosity: debug, info, warn, error")

	root.AddCommand(
		newInitCommand(),
		newRunCommand(),
		newDaemonCommand(),
		newAuthCommand(),
		newValidateCommand(),
		newMCPCommand(),
	)

	return root
}

// Execute runs the CLI.
func Execute(ctx context.Context) error {
	return newRootCommand().ExecuteContext(ctx)
}
