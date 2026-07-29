package main

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// execute runs the CLI with args, capturing stdout+stderr into one buffer.
func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()

	buf := &bytes.Buffer{}
	root := newRootCommand()
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)

	err := root.Execute()
	return buf.String(), err
}

func TestHelpListsSubcommands(t *testing.T) {
	out, err := execute(t, "--help")
	require.NoError(t, err)

	for _, name := range []string{"run", "daemon", "auth", "validate", "mcp"} {
		require.Contains(t, out, name)
	}
}

func TestSubcommandStubsAreNotImplemented(t *testing.T) {
	// Every subcommand is now implemented — `run`, `daemon`, `validate` and
	// `auth`; see run_test.go, daemon_test.go, validate_test.go and
	// internal/provider/gmail. The list is deliberately kept (empty) so a new
	// stub only has to be named here to be covered.
	for _, name := range []string{} {
		t.Run(name, func(t *testing.T) {
			out, err := execute(t, name)
			require.Error(t, err)
			require.Contains(t, out, "not implemented")

			var ne *notImplementedError
			require.ErrorAs(t, err, &ne)
			require.Equal(t, name, ne.command)
		})
	}
}

func TestVersionFlag(t *testing.T) {
	out, err := execute(t, "--version")
	require.NoError(t, err)
	require.Contains(t, out, version)
}

func TestConfigFlagDefault(t *testing.T) {
	root := newRootCommand()
	flag := root.PersistentFlags().Lookup("config")
	require.NotNil(t, flag)
	require.True(t, strings.HasSuffix(flag.DefValue, filepath.Join(".config", "mail-muncher", "config.yml")),
		"default config path %q should end in .config/mail-muncher/config.yml", flag.DefValue)
}

func TestLogLevelFlag(t *testing.T) {
	root := newRootCommand()
	flag := root.PersistentFlags().Lookup("log-level")
	require.NotNil(t, flag)
	require.Equal(t, "info", flag.DefValue)

	_, err := execute(t, "--log-level", "nonsense", "run")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid log level")
}

func TestParseLogLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"WARN":  slog.LevelWarn,
		"error": slog.LevelError,
	}
	for in, want := range cases {
		got, err := parseLogLevel(in)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}

	_, err := parseLogLevel("loud")
	require.Error(t, err)
}
