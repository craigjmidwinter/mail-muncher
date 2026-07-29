# The MCP server

`mail-muncher mcp` serves the **stored** mail archive over the Model Context
Protocol on stdin/stdout. It is meant to be launched by a client, not run by
hand. stdout carries protocol frames and nothing else; all logging goes to
stderr, so `--log-level debug` is safe to leave on.

## Registering it

The `mail-muncher` Claude Code plugin registers this server automatically —
check `/mcp` before adding a second copy.

Otherwise:

```bash
claude mcp add mail-muncher -- mail-muncher mcp
```

or in a project `.mcp.json`:

```json
{
  "mcpServers": {
    "mail-muncher": {
      "command": "mail-muncher",
      "args": ["mcp"]
    }
  }
}
```

Use `"args": ["mcp", "--config", "/abs/path/config.yml"]` for a non-default
config. The binary must be on `PATH`, or give an absolute path as `command`.

The config is read **once at startup**, exactly as `daemon` reads it: a
`config.yml` edit needs a restart. The domain files that config points at are
re-resolved on every `list_rules` call, so a subscription change is visible
immediately.

## With no usable config

Registering the server before there is a config is fine. It is **not** a dead
server and not a bug to report.

Launched by a client — stdin is a pipe — with a missing or unusable config, the
server starts and speaks the protocol correctly:

- `initialize` returns `instructions` that say the tools cannot return mail, that
  this is not transient, that retrying will not help, and to relay the setup
  guidance verbatim; the guidance text follows in the same field.
- `tools/list` returns the **real five tool names** (`list_messages`,
  `read_message`, `search_messages`, `list_rules`, `sync`), each described as
  unavailable — not an empty list and not "unknown tool", which an agent working
  from a cached tool list would read as version skew.
- Any `tools/call` returns `isError: true` with that same guidance as its text
  content, whatever arguments were sent.

The guidance is also written to stderr at startup, where a client tees the server
log. The process still exits **1** once the client hangs up.

Run by hand instead — stdin a terminal, `/dev/null`, or a redirected file —
`mcp` prints the guidance to stderr and exits 1 without serving, because serving
would block forever with nobody on the other end.

Fix it with `mail-muncher init`, then restart the server: config is read once at
startup.

## Security envelope

- Only files under a configured rule `dest` are readable. Paths are jailed at
  construction and a symlink swapped in later cannot widen the jail. A refused
  path is deliberately indistinguishable from "no such file" so the jail cannot
  be used to probe the filesystem.
- No credential, token, state or config path is ever exposed. `list_rules` names
  `from_domains_file` paths only, because those belong to the agent.
- Four of the five tools cannot change anything. `sync` can only ever **add**
  files: it runs the same cycle `run` does, takes the same cross-process lock,
  and is bound by the same read-only guarantee as the configured provider — the
  `gmail.readonly` scope on Gmail, `EXAMINE` plus `BODY.PEEK[]` on IMAP.

## Tools

### `list_messages`

List archived messages, newest first.

| Arg | Type | Meaning |
| --- | --- | --- |
| `rule` | string | Only messages claimed by this rule (see `list_rules`). |
| `account` | string | Only messages from this configured account. |
| `thread_id` | string | Only messages in this conversation. |
| `since` | string | Inclusive lower bound on message date. RFC3339 or `YYYY-MM-DD`. |
| `until` | string | Upper bound. A bare date includes the whole day. |
| `limit` | int | Default 50, maximum 500. |
| `group_by_thread` | bool | Also return the results grouped into conversations, oldest message first within each. |

Returns `messages` (summaries), optional `threads`, and `count` / `matched` /
`truncated` / `limit`. `matched` is the number before the limit was applied, so
`truncated: true` means narrow the filters or raise `limit`.

### `read_message`

Read one message in full. Exactly one of `id` or `path` is required.

| Arg | Type | Meaning |
| --- | --- | --- |
| `id` | string | The `id` from a summary. |
| `path` | string | A path from a summary or a run manifest. Must be inside a configured `dest`. |
| `thread` | bool | Also return every other message in the same conversation, oldest first. |
| `max_body_chars` | int | Truncate each body. Default 20000, maximum 500000. |

Returns `message` (and `thread` when asked) with metadata, `body`,
`body_format` (`markdown` when the body came from a rendered `.md` or an HTML
part, otherwise `text`), `body_truncated`, `files` (one per rendering on disk)
and `attachments` (filename, content type, byte size — never the bytes; open
the file next to the message for those).

Prefer `thread: true` over a second round trip. Grouping the thread is nearly
free, and a hiring process or a booking is a conversation, not a message.

### `search_messages`

Case-insensitive substring search across subject, sender, recipients, labels,
attachment names and body.

| Arg | Type | Meaning |
| --- | --- | --- |
| `query` | string | **Required**, must not be blank. |
| `rule`, `account`, `thread_id`, `since`, `until`, `limit` | | Same as `list_messages`. |

Each result carries a `snippet` of about 120 characters either side of the
first hit.

### `list_rules`

No arguments. Returns each configured rule in config order with `name`,
`account`, `accounts` (the configured accounts it actually applies to), `dest`,
`formats`, `stored_messages` (how many are on disk under that dest), and
`domain_files`.

`domain_files` is the important part and the reason to call this first: every
`from_domains_file` the rule references is **read at call time**, not echoed as
a path. Each entry has `path`, `exists`, `domains` (normalized and
de-duplicated exactly as the pipeline would parse them), `count`,
`modified_at`, and a `note` explaining why the list is empty when it is. A
missing file is an empty list plus a note, never an error.

This is how an agent answers "what am I currently subscribed to" and confirms
that a change it just wrote to its own domain list took effect.

### `sync`

Fetch new mail once and file it.

| Arg | Type | Meaning |
| --- | --- | --- |
| `dry_run` | bool | Evaluate and report what would be written without writing anything. |

Returns `dry_run`, `manifests` (one per configured account, in config order —
the same shape `run --json` writes; see `output.md`) and an optional `error`.

- A provider failure is **not** a tool error: the manifests still describe
  everything that landed before it, with the failure recorded in `error`.
- Only one sync runs at a time. A concurrent call is **refused immediately**
  rather than queued, because a tool call that blocks for the length of a mail
  fetch is indistinguishable from a hung server. A lock held by another process
  (a cron `run`, a daemon tick) comes back the same way: "a sync is already in
  progress; try again shortly". Wait and call again — never retry in a tight
  loop.

## Suggested call order

1. `list_rules` — what is being collected, and which senders are subscribed
   right now.
2. `sync` — only if the archive may be stale and no cron/daemon is already
   running. If one is, skip this; the lock will refuse you anyway.
3. `list_messages` or `search_messages` — find candidates. Both return
   `thread_id`.
4. `read_message` with `thread: true` — read the conversation, not the message.

To change what mail arrives, do not look for a tool: **edit the
`from_domains_file`** that `list_rules` named, then call `list_rules` again to
confirm the new list, and `sync` to pull the mail it now matches. That file is
the subscription API.
