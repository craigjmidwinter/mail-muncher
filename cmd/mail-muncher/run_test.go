package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/craigjmidwinter/mail-muncher/internal/pipeline"
	"github.com/craigjmidwinter/mail-muncher/internal/state"
	"github.com/stretchr/testify/require"
)

// writeRunConfig writes a config that loads and validates cleanly but whose
// account has no OAuth credentials, so a cycle reaches the provider seam and
// fails there. That is exactly the shape needed to exercise the manifest and
// the exit-code contract without a network.
func writeRunConfig(t *testing.T) (configPath, stateDir, destDir string) {
	t.Helper()
	root := t.TempDir()
	stateDir = filepath.Join(root, "state")
	destDir = filepath.Join(root, "mail")
	configPath = filepath.Join(root, "config.yml")

	body := fmt.Sprintf(`state_dir: %s
accounts:
  - name: personal
    provider: gmail
    gmail:
      credentials_file: %s/credentials.json
      token_file: %s/token.json
rules:
  - name: job-search
    dest: %s
    formats: [eml]
    match:
      from_domains: [acme.com]
`, stateDir, root, root, destDir)

	require.NoError(t, os.WriteFile(configPath, []byte(body), 0o644))
	return configPath, stateDir, destDir
}

// runCLI executes the command tree with stdout and stderr kept apart, which is
// what `run --json` depends on.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	var out, errBuf bytes.Buffer
	root := newRootCommand()
	root.SetOut(&out)
	root.SetErr(&errBuf)
	// Stdin is set explicitly, and to something that is not a pipe: `mcp`
	// decides whether an MCP client launched it by looking at stdin, and a test
	// run must never look like one.
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)

	err = root.Execute()
	return out.String(), errBuf.String(), err
}

// exitCodeOf reads the status an error carries, or 0 when there is none.
func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var coded *pipeline.ExitCodeError
	require.ErrorAs(t, err, &coded, "error %v carries no exit status", err)
	return coded.Code
}

// TestRunMissingConfigExitsOne: a config that will not load is exit 1.
func TestRunMissingConfigExitsOne(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yml")

	_, _, err := runCLI(t, "run", "--config", missing)
	require.Error(t, err)
	require.Equal(t, pipeline.ExitConfig, exitCodeOf(t, err))
}

// TestRunInvalidConfigExitsOne: a config that loads but does not validate —
// here a match tree that will not compile — is also exit 1. This doubles as
// proof that internal/pipeline installs the match validator.
func TestRunInvalidConfigExitsOne(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yml")
	body := fmt.Sprintf(`state_dir: %s/state
accounts:
  - name: personal
    provider: gmail
    gmail:
      credentials_file: %s/credentials.json
      token_file: %s/token.json
rules:
  - name: broken
    dest: %s/mail
    match:
      no_such_predicate: nope
`, root, root, root, root)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	_, _, err := runCLI(t, "run", "--config", path)
	require.Error(t, err)
	require.Equal(t, pipeline.ExitConfig, exitCodeOf(t, err))
	require.Contains(t, err.Error(), "no_such_predicate")
}

// TestRunAuthFailureExitsTwo: a provider that will not build is exit 2, and
// the actionable text reaches the user verbatim.
func TestRunAuthFailureExitsTwo(t *testing.T) {
	configPath, _, _ := writeRunConfig(t)

	stdout, _, err := runCLI(t, "run", "--config", configPath)
	require.Error(t, err)
	require.Equal(t, pipeline.ExitProvider, exitCodeOf(t, err))

	// The gmail package's setup guidance must survive to the surface.
	require.Contains(t, err.Error(), "credentials")

	// A human summary line is still printed for the account.
	require.Contains(t, stdout, "personal:")
	require.Contains(t, stdout, "fetched=0")
}

// TestRunLockHeldExitsThree covers the cron-collides-with-daemon case.
func TestRunLockHeldExitsThree(t *testing.T) {
	configPath, stateDir, _ := writeRunConfig(t)

	// Stand in for a concurrently running instance.
	lock, err := state.NewStore(stateDir).TryLock()
	require.NoError(t, err)
	defer func() { _ = lock.Unlock() }()

	stdout, _, runErr := runCLI(t, "run", "--config", configPath)
	require.Error(t, runErr)
	require.Equal(t, pipeline.ExitLocked, exitCodeOf(t, runErr))
	require.ErrorIs(t, runErr, state.ErrLocked)
	require.Empty(t, stdout, "a locked-out run has no manifest to report")

	// Releasing the lock lets the next run past the lock (it then fails at the
	// provider, which is exit 2 — proof it got further).
	require.NoError(t, lock.Unlock())
	_, _, runErr = runCLI(t, "run", "--config", configPath)
	require.Equal(t, pipeline.ExitProvider, exitCodeOf(t, runErr))
}

// TestRunJSONStdoutIsPureJSON is the hard acceptance criterion, checked
// in-process: everything human goes to stderr, so stdout parses as JSON with
// nothing else in it.
func TestRunJSONStdoutIsPureJSON(t *testing.T) {
	configPath, _, _ := writeRunConfig(t)

	stdout, stderr, err := runCLI(t, "run", "--json", "--log-level", "debug", "--config", configPath)
	require.Error(t, err, "this fixture fails at the provider")
	require.Equal(t, pipeline.ExitProvider, exitCodeOf(t, err))

	// Every line of stdout must be a complete JSON object.
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	require.NotEmpty(t, lines)
	for _, line := range lines {
		var m pipeline.Manifest
		require.NoError(t, json.Unmarshal([]byte(line), &m),
			"stdout line is not valid JSON: %q", line)
		require.Equal(t, "personal", m.Account)
		require.NotEmpty(t, m.Error, "the partial manifest records why the cycle stopped")
	}

	// And the logging really did happen — somewhere else.
	require.NotEmpty(t, stderr)
	require.NotContains(t, stdout, "level=", "slog output leaked into stdout")
}

// TestRunJSONStdoutIsPureJSONInSubprocess proves the same thing the way the
// acceptance criterion is actually written: a real binary, with stderr sent to
// /dev/null, whose stdout is nothing but JSON.
func TestRunJSONStdoutIsPureJSONInSubprocess(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}

	bin := filepath.Join(t.TempDir(), "mail-muncher")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	require.NoError(t, build.Run(), "building the CLI")

	configPath, _, _ := writeRunConfig(t)

	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	require.NoError(t, err)
	defer func() { _ = devNull.Close() }()

	var stdout bytes.Buffer
	cmd := exec.Command(bin, "run", "--json", "--log-level", "debug", "--config", configPath)
	cmd.Stdout = &stdout
	cmd.Stderr = devNull // the literal `2>/dev/null` of the acceptance criterion
	runErr := cmd.Run()

	// The fixture has no credentials, so this is the exit-2 path.
	var exitErr *exec.ExitError
	require.ErrorAs(t, runErr, &exitErr)
	require.Equal(t, pipeline.ExitProvider, exitErr.ExitCode(),
		"the process must exit 2, which requires main() to honour the exit code")

	out := stdout.String()
	require.NotEmpty(t, out, "the manifest must still be emitted on a failing cycle")

	dec := json.NewDecoder(strings.NewReader(out))
	var seen int
	for {
		var m pipeline.Manifest
		if err := dec.Decode(&m); err != nil {
			require.ErrorContains(t, err, "EOF", "stdout held something that is not JSON: %q", out)
			break
		}
		seen++
		require.Equal(t, "personal", m.Account)
	}
	require.Equal(t, 1, seen, "one manifest object per account")
}

// TestRunDryRunWritesNothing: the flag must actually be wired through. A
// --dry-run that silently writes files is the worst failure this tool has.
func TestRunDryRunWritesNothing(t *testing.T) {
	configPath, stateDir, destDir := writeRunConfig(t)

	stdout, _, err := runCLI(t, "run", "--dry-run", "--config", configPath)
	require.Error(t, err, "the fixture still fails at the provider")
	require.Equal(t, pipeline.ExitProvider, exitCodeOf(t, err))

	require.Contains(t, stdout, "(dry-run)", "the summary must say it was a dry run")

	require.NoDirExists(t, destDir, "a dry run must not create the destination tree")
	require.NoFileExists(t, filepath.Join(stateDir, "personal.json"), "a dry run must not save state")
}

// TestRunDryRunJSONSetsFlag: --dry-run --json marks the manifest.
func TestRunDryRunJSONSetsFlag(t *testing.T) {
	configPath, _, _ := writeRunConfig(t)

	stdout, _, err := runCLI(t, "run", "--dry-run", "--json", "--config", configPath)
	require.Error(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got))
	require.Equal(t, true, got["dry_run"])
}

// TestRunFlags pins the flags the docs publish.
func TestRunFlags(t *testing.T) {
	root := newRootCommand()
	for _, c := range root.Commands() {
		if c.Name() != "run" {
			continue
		}
		require.NotNil(t, c.Flags().Lookup("dry-run"), "run must expose --dry-run")
		require.NotNil(t, c.Flags().Lookup("json"), "run must expose --json")
		require.Equal(t, "false", c.Flags().Lookup("dry-run").DefValue)
		require.Equal(t, "false", c.Flags().Lookup("json").DefValue)
		return
	}
	t.Fatal("run command not registered")
}
