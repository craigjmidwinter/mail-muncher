package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

// unconfiguredToolNames are the tools the real server exposes. The
// unconfigured server registers the same names so an agent working from a
// cached tool list gets the setup guidance back, rather than "unknown tool"
// — which teaches it nothing and is indistinguishable from a version skew.
//
// They must stay in step with mcpserver.Server.ToolNames; a test asserts it.
var unconfiguredToolNames = []string{
	"list_messages", "read_message", "search_messages", "list_rules", "sync",
}

// mcpStartupFailure is what `mcp` does instead of dying quietly.
//
// This is the sharpest case in the whole product. Every other command is run by
// a person who sees stderr; `mcp` is launched by an MCP client, which sees a
// process that exited and reports "server failed to start". The calling agent
// then has nothing to relay and no next command. So when the config is missing
// or broken and we were plainly launched by a client, we start anyway: a server
// that speaks the protocol correctly, carries the guidance in its initialize
// instructions, and answers every tool call with that same text as a tool
// error. An agent can relay it verbatim.
//
// The guidance also goes to stderr immediately, because that is where a client
// tees the server log, and because a person who ran the command by hand needs
// it now rather than at exit.
//
// The exit status is unchanged either way: a config failure is still 1.
func mcpStartupFailure(ctx context.Context, cmd *cobra.Command, err error) error {
	text := startupText(err)
	fmt.Fprintln(cmd.ErrOrStderr(), text)

	if !launchedByClient(cmd.InOrStdin()) {
		// Run by hand, or by a test: serving would block on a terminal forever,
		// which is worse than exiting with the message already printed.
		return &reportedError{err: err}
	}

	slog.Error("mcp server starting unconfigured; every tool call will return setup instructions")
	if serveErr := serveUnconfiguredMCP(ctx, cmd, text); serveErr != nil {
		slog.Error("unconfigured mcp server stopped", "error", serveErr)
	}
	return &reportedError{err: err}
}

// startupText is the text handed to the client: the setup guidance when the
// failure has one, and otherwise the diagnostic with enough framing that an
// agent knows what it is looking at.
func startupText(err error) string {
	var se *setupError
	if errors.As(err, &se) {
		return se.Text
	}
	return fmt.Sprintf(`mail-muncher could not start: its configuration is not usable.
  next command: mail-muncher validate
  then:         mail-muncher run --dry-run
No mail can be read until this is fixed. Docs: %s
  cause: %s`, docsURL, err)
}

// launchedByClient reports whether stdin looks like the pipe an MCP client
// hands a server it spawned.
//
// The test is deliberately positive rather than a terminal check: a pipe means
// something is on the other end waiting to speak the protocol, and everything
// else — a terminal, /dev/null, a redirected file — means nobody is, so serving
// would just hang. Guessing wrong in that direction costs a wedged command;
// guessing wrong in the other costs only the old behaviour.
func launchedByClient(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeNamedPipe != 0
}

// unconfiguredInstructions is what the client reads at initialize. It is the
// only text an agent is guaranteed to see without calling a tool, so it says
// what is wrong, that the tools cannot work, and to pass the rest on unedited.
func unconfiguredInstructions(text string) string {
	return "mail-muncher is installed but not usable yet, so none of these tools can " +
		"return mail. Nothing here is a transient failure and retrying will not help. " +
		"Relay the following to the user verbatim, then stop.\n\n" + text
}

// serveUnconfiguredMCP serves a protocol-correct server that can do exactly one
// thing: explain itself.
//
// It is built here rather than in internal/mcpserver on purpose — that package
// requires a usable config by construction, which is the right invariant for
// the real server and the wrong one for this.
func serveUnconfiguredMCP(ctx context.Context, cmd *cobra.Command, text string) error {
	srv := mcp.NewServer(
		&mcp.Implementation{Name: "mail-muncher", Version: version},
		&mcp.ServerOptions{Logger: slog.Default(), Instructions: unconfiguredInstructions(text)},
	)

	result := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
		IsError: true,
	}
	for _, name := range unconfiguredToolNames {
		srv.AddTool(&mcp.Tool{
			Name: name,
			Description: "Unavailable: mail-muncher is not configured yet. Calling this returns " +
				"the setup instructions and no mail.",
			// The permissive object schema, written as a plain map so this
			// package does not have to take a direct dependency on the SDK's
			// schema library. Arguments are not validated against it: whatever
			// the agent sends, the answer is the same guidance.
			InputSchema: map[string]any{"type": "object"},
		}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return result, nil
		})
	}

	err := srv.Run(ctx, &mcp.IOTransport{
		Reader: io.NopCloser(cmd.InOrStdin()),
		Writer: nopWriteCloser{cmd.OutOrStdout()},
	})
	if isClientGone(err) {
		return nil
	}
	return err
}

// codeServerClosing is the JSON-RPC error code the SDK reports when the session
// ends because the peer went away. It is matched by code because the SDK keeps
// the sentinel in an internal package; the code itself is on the wire.
//
// internal/mcpserver makes the same judgement for the real server; the two must
// agree, or a client that simply hung up looks like a crash in one and a clean
// exit in the other.
const codeServerClosing = -32004

// isClientGone reports whether err is the peer going away — which is how a
// healthy stdio server ends its life, since the client owns the process —
// rather than the server failing.
func isClientGone(err error) bool {
	switch {
	case err == nil,
		errors.Is(err, context.Canceled),
		errors.Is(err, io.EOF),
		errors.Is(err, io.ErrClosedPipe),
		errors.Is(err, mcp.ErrConnectionClosed):
		return true
	}
	var wire *jsonrpc.Error
	return errors.As(err, &wire) && wire.Code == codeServerClosing
}

// nopWriteCloser adapts a writer this command does not own: ending the session
// must not close the process's stdout.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
