// Package mcpserver exposes the mail mail-muncher has already stored to agents
// over the Model Context Protocol.
//
// The archive on disk is the source of truth. Nothing here talks to a mail
// provider, and nothing here writes, moves, or deletes a stored message: the
// tools read the `.md` and `.eml` files under each rule's `dest` directory and
// answer questions about them. The single exception is the `sync` tool, which
// delegates to pipeline.Runner.Cycle — the same call `run` and `daemon` make,
// including the cross-process lock — and which can therefore only ever *add*
// files.
//
// # The five tools
//
//	list_messages   message summaries, newest first, filtered and optionally
//	                grouped by thread
//	read_message    one message in full (metadata, body, attachments), with the
//	                rest of its thread on request
//	search_messages substring search over subject/sender/recipients/body, with
//	                a surrounding snippet per hit
//	list_rules      the configured rules, each with the *live* contents of any
//	                `from_domains_file` it references
//	sync            one pipeline cycle
//
// # The path jail
//
// Every path a tool resolves must land inside one of the configured rule `dest`
// directories. See Archive.Resolve: a caller-supplied path is checked
// lexically against each dest, then resolved with filepath.EvalSymlinks and
// checked again, so neither `..` nor a symlink planted inside the tree can
// reach a file the archive does not own. Only `.eml` and `.md` files are
// readable, and the directory walk never follows symlinks. Nothing outside the
// dest tree — the config file, credential files, token files, the state
// directory — is reachable through any tool, and no tool result names them.
//
// # Transport
//
// The server speaks stdio, where stdout carries the JSON-RPC frames and
// nothing else. Every log line this package emits goes to the slog.Logger in
// Options, which the CLI points at stderr. A stray write to stdout corrupts
// the protocol stream, so this package never writes to it directly.
package mcpserver
