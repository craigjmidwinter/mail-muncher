package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/craigjmidwinter/mail-muncher/internal/pipeline"
	"github.com/craigjmidwinter/mail-muncher/internal/state"
)

// connect runs the server on the SDK's in-memory transport and returns an
// initialized client session. This exercises the real protocol path —
// initialize, schema validation, content packing — rather than calling the
// handlers directly.
func connect(t *testing.T, s *Server) *mcp.ClientSession {
	t.Helper()

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	ss, err := s.MCP().Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ss.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "v1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	return cs
}

// callJSON makes a tool call and decodes its structured output into v. It
// fails the test if the call came back as a tool error.
func callJSON(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, v any) {
	t.Helper()

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	require.False(t, res.IsError, "%s returned a tool error: %s", name, toolErrorText(res))
	require.NotNil(t, res.StructuredContent, "%s returned no structured content", name)

	b, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, v))
}

// toolErrorText renders the text content of a failed call.
func toolErrorText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// TestProtocolInitializeAndListTools is the acceptance criterion: a real
// client initializes, and tools/list returns the five tools with usable
// schemas.
func TestProtocolListsFiveTools(t *testing.T) {
	s := seedArchive(t).server(&fakeSyncer{})
	cs := connect(t, s)

	require.Equal(t, ServerName, cs.InitializeResult().ServerInfo.Name)
	require.Contains(t, cs.InitializeResult().Instructions, "thread_id",
		"the instructions must teach an agent to group by thread")

	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	got := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}
	require.Len(t, got, 5)
	for _, name := range s.ToolNames() {
		tool, ok := got[name]
		require.True(t, ok, "tools/list is missing %q", name)
		require.NotEmpty(t, tool.Description, "%s has no description", name)
		require.NotNil(t, tool.InputSchema, "%s has no input schema", name)
		require.NotNil(t, tool.OutputSchema, "%s has no output schema", name)

		in := schemaOf(t, tool.InputSchema)
		require.Equal(t, "object", in["type"], "%s input schema is not an object", name)
		if name != "list_rules" {
			require.Contains(t, in, "properties", "%s declares no arguments", name)
		}

		out := schemaOf(t, tool.OutputSchema)
		require.Equal(t, "object", out["type"], "%s output schema is not an object", name)
	}

	// Only sync may be advertised as changing anything.
	for name, tool := range got {
		if name == "sync" {
			require.False(t, tool.Annotations.ReadOnlyHint)
			require.NotNil(t, tool.Annotations.DestructiveHint)
			require.False(t, *tool.Annotations.DestructiveHint, "sync only ever adds files")
			continue
		}
		require.True(t, tool.Annotations.ReadOnlyHint, "%s must be advertised read-only", name)
	}

	// query is the one required argument in the whole surface.
	search := schemaOf(t, got["search_messages"].InputSchema)
	require.Equal(t, []any{"query"}, search["required"])
	require.Empty(t, schemaOf(t, got["list_messages"].InputSchema)["required"])
}

// schemaOf re-marshals a schema value into a generic map, which is what a
// client actually receives over the wire.
func schemaOf(t *testing.T, schema any) map[string]any {
	t.Helper()
	b, err := json.Marshal(schema)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

// TestProtocolRoundTrips drives every tool over the wire, so the inferred
// schemas and the handlers are proven to agree.
func TestProtocolRoundTrips(t *testing.T) {
	runner := &fakeSyncer{manifests: []pipeline.Manifest{{
		Account: "personal",
		Stored:  []pipeline.Entry{},
		Skipped: []pipeline.Entry{},
		Summary: pipeline.Summary{Fetched: 2},
	}}}
	s := seedArchive(t).server(runner)
	cs := connect(t, s)

	var list ListMessagesOutput
	callJSON(t, cs, "list_messages", map[string]any{"limit": 2, "group_by_thread": true}, &list)
	require.Len(t, list.Messages, 2)
	require.NotEmpty(t, list.Threads)
	require.True(t, list.Truncated)

	var read ReadMessageOutput
	callJSON(t, cs, "read_message", map[string]any{"id": list.Messages[0].ID, "thread": true}, &read)
	require.NotEmpty(t, read.Message.Subject)
	require.NotEmpty(t, read.Thread)

	var byPath ReadMessageOutput
	callJSON(t, cs, "read_message", map[string]any{"path": list.Messages[0].Path}, &byPath)
	require.Equal(t, read.Message.ID, byPath.Message.ID)

	var search SearchMessagesOutput
	callJSON(t, cs, "search_messages", map[string]any{"query": "system design"}, &search)
	require.Equal(t, 2, search.Count)
	require.NotEmpty(t, search.Messages[0].Snippet)

	var rules ListRulesOutput
	callJSON(t, cs, "list_rules", map[string]any{}, &rules)
	require.Len(t, rules.Rules, 3)
	require.Len(t, rules.Rules[0].DomainFiles, 1)
	require.Len(t, rules.Rules[0].PatternFiles, 1,
		"pattern_files survives the inferred output schema, same as domain_files")

	var synced SyncOutput
	callJSON(t, cs, "sync", map[string]any{"dry_run": false}, &synced)
	require.Len(t, synced.Manifests, 1)
	require.Equal(t, 2, synced.Manifests[0].Summary.Fetched)
	require.Equal(t, 1, runner.callCount())
}

// TestServeStdioListRulesReportsBothFileKinds drives list_rules the way a real
// client does — raw JSON-RPC over a pipe, no in-memory shortcut — for a rule
// whose subscription is split across a domain list and a pattern list.
//
// It asserts on the decoded frame rather than on the handler's return value,
// so a field that the inferred output schema drops on the way out is a failure
// here. The frame is logged, because the shape of that JSON is the contract an
// agent codes against and it should be readable from a test run.
func TestServeStdioListRulesReportsBothFileKinds(t *testing.T) {
	f := seedArchive(t)
	require.NoError(t, os.WriteFile(f.domainFile, []byte(
		"# senders I am waiting to hear from\n@Acme.example.\nglobex.io\n"), 0o644))
	require.NoError(t, os.WriteFile(f.patternFile, []byte(
		"# tracked companies\nwagepoint\n(?i)^careers@acme\\.io$\nx?\n"), 0o644))
	s := f.server(&fakeSyncer{})

	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	served := make(chan error, 1)
	go func() {
		err := s.ServeStdio(ctx, inReader, outWriter)
		_ = outWriter.Close()
		served <- err
	}()

	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"v1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_rules","arguments":{}}}`,
	}
	go func() {
		for _, r := range requests {
			if _, err := io.WriteString(inWriter, r+"\n"); err != nil {
				return
			}
		}
	}()

	scanner := bufio.NewScanner(outReader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)

	var structured json.RawMessage
	for scanner.Scan() {
		var frame struct {
			ID     float64 `json:"id"`
			Result struct {
				IsError           bool            `json:"isError"`
				StructuredContent json.RawMessage `json:"structuredContent"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &frame),
			"the output stream carried something that is not a JSON-RPC frame: %q", scanner.Text())
		if frame.ID != 2 {
			continue
		}
		require.False(t, frame.Result.IsError)
		structured = frame.Result.StructuredContent
		break
	}
	require.NoError(t, scanner.Err())
	require.NotEmpty(t, structured, "list_rules produced no structured content")

	var pretty bytes.Buffer
	require.NoError(t, json.Indent(&pretty, structured, "", "  "))
	t.Logf("list_rules over stdio:\n%s", pretty.String())

	var out ListRulesOutput
	require.NoError(t, json.Unmarshal(structured, &out))

	jobs := out.Rules[0]
	require.Equal(t, "job-search", jobs.Name)
	require.Len(t, jobs.DomainFiles, 1)
	require.Equal(t, []string{"acme.example", "globex.io"}, jobs.DomainFiles[0].Domains)
	require.Len(t, jobs.PatternFiles, 1)
	require.Equal(t, []string{"wagepoint", `(?i)^careers@acme\.io$`}, jobs.PatternFiles[0].Patterns)
	require.Equal(t, 2, jobs.PatternFiles[0].Count)
	require.Equal(t, 1, jobs.PatternFiles[0].Rejected)
	require.Contains(t, jobs.PatternFiles[0].Note, "1 line rejected")

	require.NoError(t, inWriter.Close())
	select {
	case err := <-served:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("the server did not stop after the client closed the stream")
	}
}

// TestProtocolToolErrorsAreToolErrors: a hostile path, an unknown id and a
// held lock must all come back as IsError results the model can read and
// correct, never as protocol failures that kill the session.
func TestProtocolToolErrorsAreToolErrors(t *testing.T) {
	f := seedArchive(t)
	s := f.server(&fakeSyncer{err: fmt.Errorf("account %q: %w", "personal", state.ErrLocked)})
	cs := connect(t, s)
	ctx := context.Background()

	cases := map[string]*mcp.CallToolParams{
		"traversal":   {Name: "read_message", Arguments: map[string]any{"path": "../../token.json"}},
		"absolute":    {Name: "read_message", Arguments: map[string]any{"path": "/etc/passwd"}},
		"unknown id":  {Name: "read_message", Arguments: map[string]any{"id": "00000000"}},
		"bad date":    {Name: "list_messages", Arguments: map[string]any{"since": "last tuesday"}},
		"blank query": {Name: "search_messages", Arguments: map[string]any{"query": "  "}},
		"locked":      {Name: "sync", Arguments: map[string]any{}},
	}

	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := cs.CallTool(ctx, params)
			require.NoError(t, err, "a tool failure must not be a protocol failure")
			require.True(t, res.IsError)

			text := toolErrorText(res)
			require.NotEmpty(t, text)
			require.NotContains(t, text, "hunter2")
			require.NotContains(t, text, "token_file")
		})
	}

	// A missing required argument is rejected by the schema before the handler
	// ever runs.
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "search_messages", Arguments: map[string]any{}})
	require.NoError(t, err)
	require.True(t, res.IsError)

	// The session is still usable after all of that.
	var list ListMessagesOutput
	callJSON(t, cs, "list_messages", map[string]any{}, &list)
	require.Equal(t, 4, list.Count)
}

// TestServeStdioWritesOnlyProtocolFrames drives ServeStdio the way a client
// does — raw JSON-RPC in on one stream, raw JSON-RPC out on another — and
// asserts the output stream carries nothing but frames.
//
// The subprocess test in cmd/mail-muncher proves the same property of the real
// process's stdout; this one proves it of the server itself, so a regression is
// attributed to the right layer.
func TestServeStdioWritesOnlyProtocolFrames(t *testing.T) {
	s := seedArchive(t).server(&fakeSyncer{})

	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	served := make(chan error, 1)
	go func() {
		err := s.ServeStdio(ctx, inReader, outWriter)
		_ = outWriter.Close()
		served <- err
	}()

	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"v1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_messages","arguments":{"limit":1}}}`,
	}
	go func() {
		for _, r := range requests {
			if _, err := io.WriteString(inWriter, r+"\n"); err != nil {
				return
			}
		}
	}()

	// One response per request that carried an id; the notification gets none.
	scanner := bufio.NewScanner(outReader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	var ids []any
	for len(ids) < 3 && scanner.Scan() {
		line := scanner.Text()

		var frame map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &frame),
			"the output stream carried something that is not a JSON-RPC frame: %q", line)
		require.Equal(t, "2.0", frame["jsonrpc"])
		require.NotContains(t, frame, "error", "unexpected protocol error: %q", line)
		require.Contains(t, frame, "result")
		ids = append(ids, frame["id"])
	}
	require.NoError(t, scanner.Err())
	// JSON-RPC responses may come back in any order — requests are handled
	// concurrently — so the set is what matters, not the sequence.
	require.ElementsMatch(t, []any{float64(1), float64(2), float64(3)}, ids)

	// Closing the input is how a client says goodbye; that is a clean stop.
	require.NoError(t, inWriter.Close())
	select {
	case err := <-served:
		require.NoError(t, err, "a client disconnecting is not a server failure")
	case <-ctx.Done():
		t.Fatal("the server did not stop after the client closed the stream")
	}
}
