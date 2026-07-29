package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/craigmidwinter/mail-muncher/internal/pipeline"
	"github.com/craigmidwinter/mail-muncher/internal/sink"
)

// writeMCPConfig writes a config that loads and validates cleanly, with a
// destination directory holding one stored message. The account has no OAuth
// credentials, which does not matter: the MCP server reads files, and only the
// `sync` tool would ever reach a provider.
func writeMCPConfig(t *testing.T) (configPath, destDir string) {
	t.Helper()

	root := t.TempDir()
	destDir = filepath.Join(root, "mail")
	configPath = filepath.Join(root, "config.yml")

	body := fmt.Sprintf(`state_dir: %[1]s/state
accounts:
  - name: personal
    provider: gmail
    gmail:
      credentials_file: %[1]s/credentials.json
      token_file: %[1]s/token.json
rules:
  - name: job-search
    dest: %[2]s
    formats: [markdown]
    match:
      from_domains: [acme.example]
`, root, destDir)
	require.NoError(t, os.WriteFile(configPath, []byte(body), 0o644))

	// A stored message, written in the layout internal/sink produces. The
	// digest fragment is the message id the tools hand back, and its length is
	// sink.HashLen rather than a literal so this fixture cannot drift from the
	// on-disk layout.
	dir := filepath.Join(destDir, "2026", "07")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	doc := "---\n" +
		"subject: Interview scheduled\n" +
		"from: Recruiter <recruiter@acme.example>\n" +
		"to: [Me <me@example.test>]\n" +
		"date: 2026-07-20T09:00:00Z\n" +
		"message_id: <hiring-1@acme.example>\n" +
		"thread_id: thread-hiring\n" +
		"thread_id_source: provider\n" +
		"account: personal\n" +
		"rule: job-search\n" +
		"---\n\nWe would like to book a call on Thursday.\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "1784538000-"+testDigest("a1b2")+"-interview-scheduled.md"),
		[]byte(doc), 0o644))

	return configPath, destDir
}

// TestMCPCommandIsRegistered: the subcommand exists, takes no positional
// arguments, and inherits the persistent flags the docs promise.
func TestMCPCommandIsRegistered(t *testing.T) {
	out, err := execute(t, "--help")
	require.NoError(t, err)
	require.Contains(t, out, "mcp")

	root := newRootCommand()
	cmd := findCommand(t, root, "mcp")
	require.Contains(t, cmd.Short, "MCP")
	require.NotNil(t, root.PersistentFlags().Lookup("config"))
	require.NotNil(t, root.PersistentFlags().Lookup("log-level"))

	// The command takes its config from the persistent flag rather than adding
	// one of its own.
	require.Nil(t, cmd.Flags().Lookup("config"))
}

// testDigest repeats seed until it is exactly sink.HashLen hex characters, so
// a fixture filename always carries a well-formed identity fragment.
func testDigest(seed string) string {
	return strings.Repeat(seed, sink.HashLen/len(seed))[:sink.HashLen]
}

// findCommand returns a registered subcommand by name.
func findCommand(t *testing.T, root *cobra.Command, name string) *cobra.Command {
	t.Helper()
	for _, c := range root.Commands() {
		if c.Name() == name {
			return c
		}
	}
	t.Fatalf("no %q subcommand is registered", name)
	return nil
}

// TestMCPBadConfigExitsOne: a config that will not load is graded like every
// other config failure, before any client can connect.
func TestMCPBadConfigExitsOne(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yml")

	_, _, err := runCLI(t, "mcp", "--config", missing)
	require.Error(t, err)
	require.Equal(t, pipeline.ExitConfig, exitCodeOf(t, err))
}

// TestMCPServesOverTheCommandsStreams drives the command itself, so the wiring
// from cobra's streams through to the protocol is covered and not just the
// server package.
func TestMCPServesOverTheCommandsStreams(t *testing.T) {
	configPath, _ := writeMCPConfig(t)

	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()

	root := newRootCommand()
	root.SetIn(inReader)
	root.SetOut(outWriter)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"mcp", "--config", configPath, "--log-level", "debug"})

	done := make(chan error, 1)
	go func() {
		err := root.Execute()
		_ = outWriter.Close()
		done <- err
	}()

	go func() {
		for _, req := range []string{
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"v1"}}}`,
			`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_messages","arguments":{}}}`,
		} {
			if _, err := io.WriteString(inWriter, req+"\n"); err != nil {
				return
			}
		}
	}()

	frames := readFrames(t, outReader, 2)
	require.NoError(t, inWriter.Close())

	select {
	case err := <-done:
		require.NoError(t, err, "a client disconnecting must not be a command failure")
	case <-time.After(30 * time.Second):
		t.Fatal("the mcp command did not stop after the client closed the stream")
	}

	call := frames[float64(2)]
	require.NotNil(t, call, "no response to the tools/call")
	result, _ := call["result"].(map[string]any)
	require.NotNil(t, result)

	structured, _ := result["structuredContent"].(map[string]any)
	require.NotNil(t, structured)
	require.Equal(t, float64(1), structured["count"])

	messages, _ := structured["messages"].([]any)
	require.Len(t, messages, 1)
	first, _ := messages[0].(map[string]any)
	require.Equal(t, "Interview scheduled", first["subject"])
	require.Equal(t, "thread-hiring", first["thread_id"])
	require.Equal(t, testDigest("a1b2"), first["id"])
}

// readFrames reads n JSON-RPC response frames, failing on anything that is not
// one, and returns them keyed by request id.
func readFrames(t *testing.T, r io.Reader, n int) map[any]map[string]any {
	t.Helper()

	frames := make(map[any]map[string]any, n)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for len(frames) < n && scanner.Scan() {
		line := scanner.Text()
		var frame map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &frame),
			"stdout carried something that is not a JSON-RPC frame: %q", line)
		require.Equal(t, "2.0", frame["jsonrpc"])
		require.NotContains(t, frame, "error", "protocol error: %q", line)
		frames[frame["id"]] = frame
	}
	require.NoError(t, scanner.Err())
	require.Len(t, frames, n)
	return frames
}

// TestMCPStdoutIsPureProtocolInSubprocess is the acceptance criterion, checked
// the way it is actually written: a real binary, launched the way an agent
// runtime launches it, whose stdout holds JSON-RPC frames and nothing else
// while --log-level debug floods stderr.
//
// This is the single most common way to break an stdio MCP server, and the
// only way to catch it is to look at the process's real stdout.
func TestMCPStdoutIsPureProtocolInSubprocess(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}

	bin := filepath.Join(t.TempDir(), "mail-muncher")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	require.NoError(t, build.Run(), "building the CLI")

	configPath, _ := writeMCPConfig(t)

	cmd := exec.Command(bin, "mcp", "--config", configPath, "--log-level", "debug")
	stdin, err := cmd.StdinPipe()
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
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_rules","arguments":{}}}`,
			`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read_message","arguments":{"path":"../../token.json"}}}`,
		} {
			if _, err := io.WriteString(stdin, req+"\n"); err != nil {
				return
			}
		}
	}()

	frames := readFrames(t, stdout, 4)
	require.NoError(t, stdin.Close())

	// Drain whatever is left so the child is not blocked on a full pipe, then
	// confirm a client disconnect is exit 0.
	_, _ = io.Copy(io.Discard, stdout)
	require.NoError(t, cmd.Wait(), "closing stdin must be a clean exit")

	// tools/list named all five tools.
	tools, _ := frames[float64(2)]["result"].(map[string]any)["tools"].([]any)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	require.ElementsMatch(t,
		[]string{"list_messages", "read_message", "search_messages", "list_rules", "sync"}, names)

	// The traversal attempt came back as a tool error, not a protocol error and
	// not a file.
	traversal, _ := frames[float64(4)]["result"].(map[string]any)
	require.Equal(t, true, traversal["isError"])

	// And the logging really did happen — on the other stream.
	logs := stderr.String()
	require.NotEmpty(t, logs)
	require.Contains(t, logs, "mcp server ready")
	require.Contains(t, logs, "level=")
}

// TestMCPLongLine covers the framing edge the scanner in readFrames would
// otherwise hide: a tool result larger than a default bufio buffer must still
// arrive as one line.
func TestMCPLongLine(t *testing.T) {
	configPath, destDir := writeMCPConfig(t)

	// A message with a body far larger than bufio's 64KiB default.
	dir := filepath.Join(destDir, "2026", "07")
	doc := "---\nsubject: Big\nfrom: Recruiter <recruiter@acme.example>\n" +
		"date: 2026-07-21T09:00:00Z\nthread_id: t2\nthread_id_source: provider\n" +
		"account: personal\nrule: job-search\n---\n\n" + strings.Repeat("lorem ipsum ", 20_000)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "1784624400-"+testDigest("b1b2")+"-big.md"), []byte(doc), 0o644))

	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()

	root := newRootCommand()
	root.SetIn(inReader)
	root.SetOut(outWriter)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"mcp", "--config", configPath})

	done := make(chan error, 1)
	go func() {
		err := root.Execute()
		_ = outWriter.Close()
		done <- err
	}()
	go func() {
		for _, req := range []string{
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"v1"}}}`,
			`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_message","arguments":{"id":"` + testDigest("b1b2") + `","max_body_chars":500000}}}`,
		} {
			if _, err := io.WriteString(inWriter, req+"\n"); err != nil {
				return
			}
		}
	}()

	frames := readFrames(t, outReader, 2)
	require.NoError(t, inWriter.Close())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("the mcp command did not stop")
	}

	result, _ := frames[float64(2)]["result"].(map[string]any)
	structured, _ := result["structuredContent"].(map[string]any)
	message, _ := structured["message"].(map[string]any)
	require.Greater(t, len(message["body"].(string)), 200_000)
	require.Equal(t, false, message["body_truncated"])
}
