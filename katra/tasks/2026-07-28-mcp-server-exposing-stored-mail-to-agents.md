---
title: MCP server exposing stored mail to agents
date: "2026-07-28"
time: "16:14:00"
tags:
    - agents
    - mcp
summary: 'mail-muncher mcp: stdio MCP server with list/read/search/sync tools over archived mail'
type: task
status: todo
effort: L
epic: agent-interface
---

## Context
The payoff of the agent framing. Instead of every agent reimplementing "walk the dest directory, parse frontmatter, dedupe", mail-muncher speaks MCP and mail becomes a tool call.

## Spec
- New subcommand `mail-muncher mcp`, **stdio transport** (the transport local agent runtimes use). Honors the existing persistent `--config` and `--log-level`.
- SDK: `github.com/modelcontextprotocol/go-sdk` v1.7.0 (official). This is the one new dependency in the project; add it deliberately.
- Implementation in `internal/mcpserver` (not `internal/mcp` — avoids confusion with the SDK package). The command layer stays a thin wire-up.
- **All logging must go to stderr.** stdio transport owns stdout; a stray `fmt.Println` corrupts the protocol stream. This is the single most common way to break an stdio MCP server.

### Tools

| tool | args | returns |
|---|---|---|
| `list_messages` | `rule?`, `account?`, `since?`, `until?`, `limit?` (default 50, max 500) | message summaries, newest first |
| `read_message` | `id` (or `path`) | full metadata + body text + attachment filenames/sizes |
| `search_messages` | `query` (required), `rule?`, `since?`, `limit?` | matching summaries with a short surrounding snippet |
| `list_rules` | — | each rule's name, account, dest, formats, and the **currently resolved** contents of any `from_domains_file` |
| `sync` | `dry_run?` | runs one pipeline cycle, returns the `pipeline.Manifest` from the manifest task |

- `list_rules` resolving the live domain file is the point: it lets an agent see what it is currently subscribed to, in the same call shape it would use to reason about coverage.
- `sync` takes the state lockfile like `run` does. If another instance holds it, return a clean tool error ("a sync is already in progress"), never block indefinitely.

### Reading stored mail
- Source of truth is the files on disk under each rule's `dest`. Prefer the `.md` rendering when present (frontmatter is already structured); fall back to parsing the `.eml` with `internal/model` when a rule is `formats: [eml]` only.
- `id` is the basename hash segment produced by `internal/sink.Basename` — stable, already the idempotency key. Accept a full path as an alternative.
- Index lazily with an mtime-keyed cache; a cold directory walk per call is acceptable at personal-mailbox scale, but do not re-parse unchanged files.

### Security — non-negotiable
- Every resolved path must be **inside one of the configured rule `dest` directories**. Reject anything else, including via `..`, symlinks that escape, or absolute paths supplied by the caller. Test this explicitly with hostile inputs.
- The server is **read-only over mail**: no tool deletes, moves, or edits stored messages, and none touches the live mailbox. `sync` is the only state-changing operation and it only ever *adds* files.
- Do not expose credentials, token files, or raw config paths through any tool result.

## Acceptance
- Unit tests per tool against a temp dest tree with fixture `.md`/`.eml` files: filtering, limits, ordering, snippet extraction, `.eml`-only fallback.
- Path-escape tests: `../`, absolute path outside dest, symlink out of dest — all rejected.
- A protocol-level smoke test using the SDK's in-memory transport: initialize, `tools/list` returns the five tools with valid schemas, one round-trip call succeeds.
- Manual: wire into a real MCP client, confirm `tools/list` and a `list_messages` call work over stdio.
