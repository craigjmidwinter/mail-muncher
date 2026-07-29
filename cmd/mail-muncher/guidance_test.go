package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/mcpserver"
	"github.com/craigjmidwinter/mail-muncher/internal/pipeline"
	"github.com/craigjmidwinter/mail-muncher/internal/provider/gmail"
)

// commandsNeedingConfig are every command that cannot work without a config
// file. Each of them must answer "there is no config file" with guidance, not
// with a parse error.
var commandsNeedingConfig = []string{"run", "daemon", "auth", "validate", "mcp"}

// TestNoConfigGuidanceForEveryCommand is the core of the unconfigured-first-run
// contract: whatever you type first, mail-muncher tells you the path it
// checked, the command to run next, and what the two provider paths cost.
//
// The assertions are about *actionable* content. A test that only checked that
// an error happened would pass against the parse failure this replaced.
func TestNoConfigGuidanceForEveryCommand(t *testing.T) {
	for _, name := range commandsNeedingConfig {
		t.Run(name, func(t *testing.T) {
			missing := filepath.Join(t.TempDir(), "config.yml")

			args := []string{name, "--config", missing}
			if name == "daemon" {
				args = append(args, "--interval", "30s")
			}
			_, stderr, err := runCLI(t, args...)
			require.Error(t, err)

			// Exit status is unchanged: this is about the message.
			require.Equal(t, pipeline.ExitConfig, exitCodeOf(t, err))

			var se *setupError
			require.ErrorAs(t, err, &se)
			require.Equal(t, kindNoConfig, se.Kind)

			// `mcp` prints its own guidance at startup, because the client that
			// launched it reads stderr as it goes; everything else lets main
			// print it. Either way the same text has to be in play.
			text := se.Text
			if name == "mcp" {
				require.Contains(t, stderr, "mail-muncher is not configured")
			}

			require.Contains(t, text, missing, "the exact path checked must be named")
			require.Contains(t, text, "mail-muncher init", "the next command must be named")
			require.Contains(t, text, "provider: imap")
			require.Contains(t, text, "provider: gmail")
			require.Contains(t, text, "app password")
			require.Contains(t, text, "BODY.PEEK")
			require.Contains(t, text, "restricted scope")
			require.Contains(t, text, "7 days")
			require.Contains(t, text, "docs/gmail-setup.md")
			require.Contains(t, text, docsURL)
		})
	}
}

// TestNoConfigGuidanceIsShortAndPlain: a wall of text is as useless as no text,
// and an agent parsing this off stderr must not have to strip decoration.
func TestNoConfigGuidanceIsShortAndPlain(t *testing.T) {
	text := noConfigGuidance("/home/someone/.config/mail-muncher/config.yml")

	lines := strings.Split(text, "\n")
	require.LessOrEqual(t, len(lines), 16, "guidance grew past a screenful:\n%s", text)

	for _, line := range lines {
		require.LessOrEqual(t, len(line), 80, "line wider than 80 columns: %q", line)
		for _, r := range line {
			require.Less(t, r, rune(128), "non-ASCII %q in %q; agents and dumb terminals both read this", r, line)
		}
		require.NotContains(t, line, "\x1b", "no escape sequences: colour must never be the only carrier of meaning")
	}
}

// TestSetupStateGuidance walks every partially-configured state a real
// installation passes through, and asserts the actionable content of each: the
// path, and the one command that gets out of it.
func TestSetupStateGuidance(t *testing.T) {
	const configPath = "/home/someone/.config/mail-muncher/config.yml"

	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "no accounts",
			text: noAccountsGuidance(configPath, errors.New("accounts: at least one account is required")),
			want: []string{configPath, "accounts:", "mail-muncher init --force", "docs/configuration.md"},
		},
		{
			name: "never authorized",
			text: notAuthorizedGuidance(configPath, "personal", "/home/someone/.config/mail-muncher/token.json", nil),
			want: []string{
				configPath,
				"/home/someone/.config/mail-muncher/token.json",
				"mail-muncher auth --account personal",
				"7 days",
				"docs/gmail-setup.md",
			},
		},
		{
			name: "token rejected",
			text: tokenRejectedGuidance(configPath, "personal", gmail.ErrTokenRejected),
			want: []string{
				configPath,
				"mail-muncher auth --account personal",
				"7-day refresh-token expiry",
				"Testing mode",
				"docs/gmail-setup.md",
			},
		},
		{
			name: "no rules",
			text: noRulesGuidance(configPath),
			want: []string{
				configPath,
				"rules:",
				"nothing will ever be stored",
				"newer_than: 72h",
				"mail-muncher validate",
				"docs/filters.md",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, want := range tc.want {
				require.Contains(t, tc.text, want)
			}
			require.NotContains(t, tc.text, "\x1b")
		})
	}
}

// TestUnreadableConfigIsNotSetupGuidance: a config file that exists but will
// not parse is a different problem with a different answer, and must keep its
// precise diagnostic rather than being told to run `init`.
func TestUnreadableConfigIsNotSetupGuidance(t *testing.T) {
	cases := map[string]string{
		"malformed yaml": "accounts: [\n",
		"unknown key":    "bogus_key: 1\n",
		"empty file":     "",
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, body)

			_, _, err := runCLI(t, "run", "--config", path)
			require.Error(t, err)
			require.Equal(t, pipeline.ExitConfig, exitCodeOf(t, err))

			var se *setupError
			require.False(t, errors.As(err, &se), "a broken config must not be reported as an unconfigured one")
			require.Contains(t, err.Error(), path, "the diagnostic must still name the file")
		})
	}
}

// TestConfigDirectoryIsReportedAsMissing covers the `--config ~/.config` typo:
// a directory where a file was expected is "not configured", not a parse error.
func TestConfigDirectoryIsReportedAsMissing(t *testing.T) {
	dir := t.TempDir()

	_, _, err := runCLI(t, "run", "--config", dir)
	require.Error(t, err)

	var se *setupError
	require.ErrorAs(t, err, &se)
	require.Equal(t, kindNoConfig, se.Kind)
	require.Contains(t, se.Text, dir)
}

// TestNoAccountsGuidance: a config file that parses but names no mailbox.
func TestNoAccountsGuidance(t *testing.T) {
	path := writeConfig(t, "rules: []\n")

	for _, name := range []string{"run", "auth"} {
		t.Run(name, func(t *testing.T) {
			_, _, err := runCLI(t, name, "--config", path)
			require.Error(t, err)
			require.Equal(t, pipeline.ExitConfig, exitCodeOf(t, err))

			var se *setupError
			require.ErrorAs(t, err, &se)
			require.Equal(t, kindNoAccounts, se.Kind)
			require.Contains(t, se.Text, path)
			require.Contains(t, se.Text, "mail-muncher init --force")
		})
	}
}

// TestNoRulesAdviceOnStderr: a config that would fetch mail and discard all of
// it is legal, so the run proceeds — but the user is told, on stderr, in terms
// that name the next step. The symptom otherwise is an empty directory and no
// error at all.
func TestNoRulesAdviceOnStderr(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yml")
	body := fmt.Sprintf(`state_dir: %[1]s/state
accounts:
  - name: personal
    provider: gmail
    gmail:
      credentials_file: %[1]s/credentials.json
      token_file: %[1]s/token.json
rules: []
`, root)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	_, stderr, err := runCLI(t, "run", "--config", path)
	require.Error(t, err, "the fixture still fails at the provider")

	require.Contains(t, stderr, "no rules, so nothing will ever be stored")
	require.Contains(t, stderr, path)
	require.Contains(t, stderr, "newer_than: 72h")
	require.Contains(t, stderr, "docs/filters.md")
}

// TestNotAuthorizedAdviceOnStderr: an account that has its OAuth client but has
// never been through the consent flow gets the `auth` command by name, before
// the cycle reaches the provider and fails.
func TestNotAuthorizedAdviceOnStderr(t *testing.T) {
	configPath, _, _ := writeRunConfig(t)
	cfg := loadRunConfig(t, configPath)

	// The Cloud Console step is done; only `auth` is left.
	require.NoError(t, os.WriteFile(cfg.Accounts[0].Gmail.CredentialsFile, []byte("{}"), 0o600))

	var stderr bytes.Buffer
	reportSetupAdvice(&stderr, cfg)

	require.Contains(t, stderr.String(), `account "personal" has never been authorized`)
	require.Contains(t, stderr.String(), configPath)
	require.Contains(t, stderr.String(), cfg.Accounts[0].Gmail.TokenFile)
	require.Contains(t, stderr.String(), "mail-muncher auth --account personal")
	require.Contains(t, stderr.String(), "7 days")

	// End to end, through the real command.
	_, cliErr, err := runCLI(t, "run", "--config", configPath)
	require.Error(t, err)
	require.Contains(t, cliErr, "mail-muncher auth --account personal")
}

// TestNotAuthorizedAdviceWaitsForTheOAuthClient: before the Cloud Console step
// is done, `auth` cannot work, so naming it would send the reader down the
// wrong path. The provider's own error covers that case precisely.
func TestNotAuthorizedAdviceWaitsForTheOAuthClient(t *testing.T) {
	configPath, _, _ := writeRunConfig(t)

	var stderr bytes.Buffer
	reportSetupAdvice(&stderr, loadRunConfig(t, configPath))
	require.NotContains(t, stderr.String(), "never been authorized")
}

// loadRunConfig loads a fixture config the way a command does.
func loadRunConfig(t *testing.T, path string) *config.Config {
	t.Helper()
	cfg, _, err := config.LoadAndValidate(path)
	require.NoError(t, err)
	return cfg
}

// TestTokenStateGuidanceIsAttachedToCycleFailures: the two token states a
// running cycle can hit are graded exactly as before — a provider failure is
// still exit 2 — but they arrive carrying the command that fixes them.
func TestTokenStateGuidanceIsAttachedToCycleFailures(t *testing.T) {
	cfg := loadedConfigForTest(t)

	cases := []struct {
		name  string
		cause error
		kind  string
		want  []string
	}{
		{
			name:  "no token",
			cause: &pipeline.ProviderError{Account: "personal", Err: fmt.Errorf("%w: nowhere", gmail.ErrNoToken)},
			kind:  kindNotAuthorized,
			want:  []string{"mail-muncher auth --account personal", "never been authorized"},
		},
		{
			name:  "token rejected",
			cause: &pipeline.ProviderError{Account: "personal", Err: fmt.Errorf("%w: invalid_grant", gmail.ErrTokenRejected)},
			kind:  kindTokenRejected,
			want:  []string{"mail-muncher auth --account personal", "7-day refresh-token expiry", "Testing mode"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := cycleFailure(cfg, tc.cause)
			require.Error(t, err)
			require.Equal(t, pipeline.ExitProvider, exitCodeOf(t, err),
				"grading must not drift: cron branches on this")

			var se *setupError
			require.ErrorAs(t, err, &se)
			require.Equal(t, tc.kind, se.Kind)
			for _, want := range tc.want {
				require.Contains(t, se.Text, want)
			}
			require.Contains(t, se.Text, "cause:", "the underlying diagnostic must survive")
			require.ErrorIs(t, err, tc.cause)
		})
	}
}

// TestCycleFailureLeavesOtherErrorsAlone: only the states with a named next
// command are rewritten. Everything else keeps the text and status it had.
func TestCycleFailureLeavesOtherErrorsAlone(t *testing.T) {
	cause := errors.New("something else went wrong")

	err := cycleFailure(loadedConfigForTest(t), cause)
	require.ErrorIs(t, err, cause)
	require.Equal(t, pipeline.ExitConfig, exitCodeOf(t, err))

	var se *setupError
	require.False(t, errors.As(err, &se))

	require.NoError(t, cycleFailure(nil, nil))
}

// loadedConfigForTest returns a loaded, validated config to hang guidance off.
func loadedConfigForTest(t *testing.T) *config.Config {
	t.Helper()
	configPath, _, _ := writeRunConfig(t)
	cfg, _, err := loadRunner(configPath, true)
	require.NoError(t, err)
	return cfg
}

// TestFatalMessageRendersGuidanceVerbatim: main must not decorate this output.
// The "error:" prefix in front of a fifteen-line setup message would bury the
// only part that matters.
func TestFatalMessageRendersGuidanceVerbatim(t *testing.T) {
	se := &setupError{Kind: kindNoConfig, Path: "/tmp/x", Text: noConfigGuidance("/tmp/x")}

	got := fatalMessage(setupFailure(pipeline.ExitConfig, se))
	require.Equal(t, se.Text+"\n", got)
	require.NotContains(t, got, "error:")

	// Everything else keeps the terse form.
	require.Equal(t, "error: boom\n", fatalMessage(errors.New("boom")))

	// A command that already printed its own message is not printed twice.
	require.Empty(t, fatalMessage(&reportedError{err: se}))

	require.Equal(t, pipeline.ExitProvider, exitStatus(&pipeline.ExitCodeError{Code: pipeline.ExitProvider, Err: se}))
	require.Equal(t, 1, exitStatus(errors.New("boom")))
}

// --- golden files ---------------------------------------------------------
//
// The no-config output for `run` and `mcp` is a user interface: it is what an
// unconfigured machine says first, and what an installing agent parses. It must
// not drift silently, so it is pinned byte for byte.

// goldenPath is the fixed path the golden files are written against, so they do
// not carry a temp directory.
const goldenPath = "/home/someone/.config/mail-muncher/config.yml"

func TestGoldenNoConfigRun(t *testing.T) {
	got := fatalMessage(setupFailure(pipeline.ExitConfig, &setupError{
		Kind: kindNoConfig,
		Path: goldenPath,
		Text: noConfigGuidance(goldenPath),
	}))
	requireGolden(t, "unconfigured-run.txt", got)
}

func TestGoldenNoConfigMCP(t *testing.T) {
	// What an MCP client actually receives: the initialize instructions, which
	// are the only text an agent is guaranteed to see without calling a tool.
	requireGolden(t, "unconfigured-mcp.txt",
		unconfiguredInstructions(noConfigGuidance(goldenPath))+"\n")
}

// requireGolden compares got against testdata/name, rewriting it under
// -update.
func requireGolden(t *testing.T, name, got string) {
	t.Helper()

	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		return
	}

	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden file; regenerate with UPDATE_GOLDEN=1 go test ./cmd/...")
	require.Equal(t, string(want), got,
		"this string is a user interface; if the change is intended, regenerate with UPDATE_GOLDEN=1")
}

// --- the mcp case ---------------------------------------------------------

// TestUnconfiguredMCPToolNamesMatchTheRealServer: an agent working from a
// cached tool list must get the guidance back, not "unknown tool".
func TestUnconfiguredMCPToolNamesMatchTheRealServer(t *testing.T) {
	configPath, _ := writeMCPConfig(t)
	cfg, runner, err := loadRunner(configPath, false)
	require.NoError(t, err)

	srv, err := mcpserver.New(mcpserver.Options{Config: cfg, Runner: runner})
	require.NoError(t, err)

	require.ElementsMatch(t, srv.ToolNames(), unconfiguredToolNames)
}

// TestLaunchedByClient: serving a protocol we cannot fulfil is only right when
// something is waiting on the other end of a pipe. A terminal, /dev/null or an
// in-memory reader all mean nobody is, and the command must exit instead of
// hanging.
func TestLaunchedByClient(t *testing.T) {
	require.False(t, launchedByClient(strings.NewReader("")), "an in-memory reader is not a client")

	devNull, err := os.Open(os.DevNull)
	require.NoError(t, err)
	defer func() { _ = devNull.Close() }()
	require.False(t, launchedByClient(devNull), "/dev/null is not a client")

	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer func() { _ = r.Close(); _ = w.Close() }()
	require.True(t, launchedByClient(r), "a pipe is how a client launches a server")
}

// TestUnconfiguredMCPServesGuidance is the sharpest case in the whole feature:
// an MCP client launches mail-muncher, gets a server that speaks the protocol
// correctly, and can relay a real answer to the user instead of "the server
// died".
func TestUnconfiguredMCPServesGuidance(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}

	bin := filepath.Join(t.TempDir(), "mail-muncher")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	require.NoError(t, build.Run(), "building the CLI")

	missing := filepath.Join(t.TempDir(), "config.yml")

	cmd := exec.Command(bin, "mcp", "--config", missing)
	stdin, err := cmd.StdinPipe() // a real pipe: this is how a client launches us
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	go func() {
		for _, req := range []string{
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"v1"}}}`,
			`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_messages","arguments":{"limit":5}}}`,
		} {
			if _, err := io.WriteString(stdin, req+"\n"); err != nil {
				return
			}
		}
	}()

	frames := readUnconfiguredFrames(t, stdout, 3)
	require.NoError(t, stdin.Close())
	_, _ = io.Copy(io.Discard, stdout)

	// The process still fails: an unconfigured mail-muncher is exit 1.
	var exitErr *exec.ExitError
	require.ErrorAs(t, cmd.Wait(), &exitErr)
	require.Equal(t, pipeline.ExitConfig, exitErr.ExitCode())

	// initialize carried the guidance, so an agent has it without a tool call.
	initResult, _ := frames[float64(1)]["result"].(map[string]any)
	require.NotNil(t, initResult)
	instructions, _ := initResult["instructions"].(string)
	require.Contains(t, instructions, "mail-muncher is not configured")
	require.Contains(t, instructions, missing)
	require.Contains(t, instructions, "mail-muncher init")
	require.Contains(t, instructions, "verbatim")

	// The tools an agent already knows about are all still there.
	tools, _ := frames[float64(2)]["result"].(map[string]any)["tools"].([]any)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	require.ElementsMatch(t, unconfiguredToolNames, names)

	// And calling one is a tool error carrying the same text, not a protocol
	// error and not a silent empty result.
	call, _ := frames[float64(3)]["result"].(map[string]any)
	require.Equal(t, true, call["isError"])
	content, _ := call["content"].([]any)
	require.NotEmpty(t, content)
	text, _ := content[0].(map[string]any)["text"].(string)
	require.Contains(t, text, "mail-muncher is not configured")
	require.Contains(t, text, missing)
	require.Contains(t, text, "mail-muncher init")

	// stderr carried it too, at startup rather than at exit, because that is
	// where a client tees the server log.
	require.Contains(t, stderr.String(), "mail-muncher is not configured")
	require.Contains(t, stderr.String(), "every tool call will return setup instructions")

	// And exactly once: the command printed it, so main must not repeat it.
	require.Equal(t, 1, strings.Count(stderr.String(), "missing config file:"))
}

// readUnconfiguredFrames reads n JSON-RPC responses, tolerating the tool error
// that readFrames deliberately rejects.
func readUnconfiguredFrames(t *testing.T, r io.Reader, n int) map[any]map[string]any {
	t.Helper()

	frames := make(map[any]map[string]any, n)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for len(frames) < n && scanner.Scan() {
			var frame map[string]any
			require.NoError(t, json.Unmarshal(scanner.Bytes(), &frame),
				"stdout carried something that is not a JSON-RPC frame: %q", scanner.Text())
			require.Equal(t, "2.0", frame["jsonrpc"])
			frames[frame["id"]] = frame
		}
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the unconfigured mcp server did not answer")
	}
	require.Len(t, frames, n)
	return frames
}

// TestMCPWithoutAClientExitsInsteadOfHanging: run by hand, `mcp` still exits 1
// with the guidance rather than blocking on a terminal forever.
func TestMCPWithoutAClientExitsInsteadOfHanging(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "config.yml")

	done := make(chan error, 1)
	go func() {
		_, _, err := runCLI(t, "mcp", "--config", missing)
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err)
		require.Equal(t, pipeline.ExitConfig, exitCodeOf(t, err))
	case <-time.After(30 * time.Second):
		t.Fatal("mcp blocked instead of reporting that it is unconfigured")
	}
}
