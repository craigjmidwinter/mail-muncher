package mcpserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/craigmidwinter/mail-muncher/internal/config"
	"github.com/craigmidwinter/mail-muncher/internal/pipeline"
)

// ServerName is the implementation name reported during initialize.
const ServerName = "mail-muncher"

// instructions are handed to the client at initialize. They are the one place
// an agent learns the shape of this archive without a round trip, so they say
// what the tools are for rather than restating their schemas.
const instructions = `mail-muncher is an email client for agents: it archives mail matching your rules to disk, and these tools read that archive.

Start with list_rules to see what is being collected and, for any rule driven by a from_domains_file, which senders are currently subscribed. Use list_messages to see what has arrived and search_messages to find something specific; both return summaries with a thread_id. Group by that id — a hiring process, a booking or a support case is a conversation, not a single message — and pass thread:true to read_message to get the whole exchange in order.

Everything here is read-only over stored mail. sync fetches new mail into the archive; it never sends, deletes or modifies anything in the mailbox.`

// Options configures a Server. Config is required; everything else has a
// working default.
type Options struct {
	// Config is the loaded, validated configuration. Its rules' `dest`
	// directories are the entire filesystem the server can see.
	Config *config.Config

	// Runner runs a live cycle for the sync tool. Nil means one is built from
	// Config on first use.
	Runner Syncer

	// DryRunner runs a `dry_run: true` cycle. Nil means one is built from
	// Config on first use.
	DryRunner Syncer

	// NewRunner overrides how a runner is built, for tests. Nil means
	// pipeline.NewRunner over Config.
	NewRunner func(dryRun bool) (Syncer, error)

	// Logger receives every human-facing message. Nil means slog.Default().
	//
	// It must never write to stdout: the stdio transport owns stdout, and a
	// single line of log output there corrupts the protocol stream. The CLI
	// points slog at stderr, which is what keeps this safe.
	Logger *slog.Logger

	// Version is reported to clients during initialize. Empty means "dev".
	Version string
}

// Server is the MCP server over one configuration's stored mail.
type Server struct {
	cfg     *config.Config
	log     *slog.Logger
	archive *Archive
	mcp     *mcp.Server

	newRunner func(dryRun bool) (Syncer, error)
	runnerMu  sync.Mutex
	runners   map[bool]Syncer

	// syncSlot is a one-deep try-lock: a sync holds it for the length of its
	// cycle, and a concurrent call that cannot take it is refused rather than
	// queued behind a mail fetch.
	syncSlot chan struct{}
}

// New builds the server and registers the five tools.
//
// It fails fast on a configuration the server could not serve — no config, no
// rule with a `dest` — so the failure surfaces at startup rather than on the
// agent's first tool call.
func New(opts Options) (*Server, error) {
	if opts.Config == nil {
		return nil, errors.New("mcpserver: no config")
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}

	archive, err := NewArchive(opts.Config, log)
	if err != nil {
		return nil, err
	}

	s := &Server{
		cfg:      opts.Config,
		log:      log,
		archive:  archive,
		runners:  make(map[bool]Syncer, 2),
		syncSlot: make(chan struct{}, 1),
	}

	s.newRunner = opts.NewRunner
	if s.newRunner == nil {
		s.newRunner = func(dryRun bool) (Syncer, error) {
			return pipeline.NewRunner(pipeline.Options{
				Config: opts.Config,
				DryRun: dryRun,
				Logger: log,
			})
		}
	}
	if opts.Runner != nil {
		s.runners[false] = opts.Runner
	}
	if opts.DryRunner != nil {
		s.runners[true] = opts.DryRunner
	}

	s.mcp = mcp.NewServer(
		&mcp.Implementation{Name: ServerName, Version: version},
		&mcp.ServerOptions{Logger: log, Instructions: instructions},
	)
	s.registerTools()

	return s, nil
}

// readOnly annotates the four tools that cannot change anything.
func readOnly(title string) *mcp.ToolAnnotations {
	closedWorld := false
	return &mcp.ToolAnnotations{
		Title:          title,
		ReadOnlyHint:   true,
		IdempotentHint: true,
		OpenWorldHint:  &closedWorld,
	}
}

// registerTools adds the five tools. Their input and output schemas are
// inferred from the Go argument and result types, so the schema an agent sees
// and the struct a handler receives cannot drift apart.
func (s *Server) registerTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "list_messages",
		Description: "List archived messages, newest first. Filter by rule, account, thread or date range. " +
			"Every summary carries a thread_id, and group_by_thread returns the same messages grouped into conversations.",
		Annotations: readOnly("List archived messages"),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in ListMessagesInput) (*mcp.CallToolResult, ListMessagesOutput, error) {
		out, err := s.listMessages(in)
		return nil, out, err
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "read_message",
		Description: "Read one archived message in full: metadata, body text, and the name and size of every attachment. " +
			"Identify it by the id from a summary, or by a path inside a configured destination directory. " +
			"Set thread:true to get the whole conversation, oldest message first.",
		Annotations: readOnly("Read an archived message"),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in ReadMessageInput) (*mcp.CallToolResult, ReadMessageOutput, error) {
		out, err := s.readMessage(in)
		return nil, out, err
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "search_messages",
		Description: "Search archived messages for a string, case-insensitively, across subject, sender, recipients, labels, " +
			"attachment names and body. Returns summaries newest first, each with a snippet around the hit.",
		Annotations: readOnly("Search archived messages"),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in SearchMessagesInput) (*mcp.CallToolResult, SearchMessagesOutput, error) {
		out, err := s.searchMessages(in)
		return nil, out, err
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "list_rules",
		Description: "List the configured rules: what each one collects, where it writes, and — read fresh on every call — " +
			"the domains currently listed in any from_domains_file it uses. This is how to see what mail is being subscribed to right now.",
		Annotations: readOnly("List collection rules"),
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ListRulesOutput, error) {
		out, err := s.listRules()
		return nil, out, err
	})

	destructive := false
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "sync",
		Description: "Fetch new mail once and file it, returning one manifest per account describing exactly what was stored. " +
			"Only ever adds files; it cannot send, delete or modify mail. Fails immediately if another sync is already running.",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Fetch new mail",
			DestructiveHint: &destructive,
			IdempotentHint:  false,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SyncInput) (*mcp.CallToolResult, SyncOutput, error) {
		out, err := s.sync(ctx, in)
		return nil, out, err
	})
}

// ToolNames lists the registered tools, in registration order. It exists for
// the command layer's startup log and for tests.
func (s *Server) ToolNames() []string {
	return []string{"list_messages", "read_message", "search_messages", "list_rules", "sync"}
}

// MCP exposes the underlying SDK server, so a test can connect it to an
// in-memory transport.
func (s *Server) MCP() *mcp.Server { return s.mcp }

// codeServerClosing is the JSON-RPC error code the SDK reports when the
// session ends because the peer went away: the client closed the pipe, or the
// process it launched us from exited.
//
// It is matched by code rather than by sentinel because the SDK keeps the
// sentinel in an internal package; the code itself is on the wire and stable.
const codeServerClosing = -32004

// Run serves MCP over the given transport until the client disconnects or ctx
// is cancelled.
//
// Both of those are how a healthy stdio server ends its life — the client is
// what owns the process — so neither is reported as a failure. Anything else
// is.
func (s *Server) Run(ctx context.Context, transport mcp.Transport) error {
	if err := s.mcp.Run(ctx, transport); err != nil && !isDisconnect(err) {
		return err
	}
	return nil
}

// isDisconnect reports whether err is the peer going away rather than the
// server failing.
func isDisconnect(err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, context.Canceled),
		errors.Is(err, io.EOF),
		errors.Is(err, io.ErrClosedPipe),
		errors.Is(err, mcp.ErrConnectionClosed):
		return true
	}
	var wire *jsonrpc.Error
	return errors.As(err, &wire) && wire.Code == codeServerClosing
}

// ServeStdio serves MCP over a byte stream, newline-delimited JSON in each
// direction. The command layer passes os.Stdin and os.Stdout.
//
// out carries protocol frames and nothing else. Everything human — every log
// line this package and the SDK emit — goes to the Logger in Options, which
// the CLI points at stderr.
func (s *Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	return s.Run(ctx, &mcp.IOTransport{
		Reader: io.NopCloser(in),
		Writer: nopWriteCloser{out},
	})
}

// nopWriteCloser adapts a writer the server does not own: closing the session
// must not close the process's stdout.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
