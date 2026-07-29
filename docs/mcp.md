---
title: The MCP server
layout: default
nav_order: 7
description: >-
  mail-muncher mcp serves the archive to an agent over the Model Context
  Protocol on stdio — list_rules, list_messages, read_message, search, sync.
---

# The MCP server

`mail-muncher mcp` serves the mail already on disk to an agent over the Model
Context Protocol, on stdin and stdout.

- [What it is](#what-it-is)
- [Wiring it into a client](#wiring-it-into-a-client)
- [What it can and cannot do](#what-it-can-and-cannot-do)
- [`list_rules`](#list_rules)
- [`list_messages`](#list_messages)
- [`read_message`](#read_message)
- [`search_messages`](#search_messages)
- [`sync`](#sync)
- [Limits](#limits)
- [Errors](#errors)
- [Troubleshooting](#troubleshooting)

## What it is

The archive on disk is the source of truth. The server reads the `.eml` and
`.md` files under each rule's `dest` and answers questions about them. It is
launched by the client, not run by hand:

```bash
mail-muncher mcp --config ~/.config/mail-muncher/config.yml
```

Five tools:

| Tool | What it answers |
| --- | --- |
| `list_rules` | What am I collecting, and which senders am I subscribed to *right now*? |
| `list_messages` | What has arrived? Filter by rule, account, thread or date; optionally grouped into conversations. |
| `search_messages` | Where is the message that mentions X? |
| `read_message` | Give me one message in full — and optionally its whole thread. |
| `sync` | Fetch new mail once, and tell me exactly what landed. |

Everything but `sync` is read-only. `sync` runs the same cycle `run` does, takes
the same cross-process lock, and can only ever *add* files.

## Wiring it into a client

The server speaks stdio. Any MCP client that launches a subprocess can use it.
Give it an absolute path to both the binary and the config — the client's
environment is not your shell's.

**Claude Code**, in `.mcp.json` at the project root (or `~/.claude.json` for a
user-level server):

```json
{
  "mcpServers": {
    "mail-muncher": {
      "command": "/usr/local/bin/mail-muncher",
      "args": ["mcp", "--config", "/Users/you/.config/mail-muncher/config.yml"]
    }
  }
}
```

**Claude Desktop**, in `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "mail-muncher": {
      "command": "/usr/local/bin/mail-muncher",
      "args": ["mcp", "--config", "/Users/you/.config/mail-muncher/config.yml"]
    }
  }
}
```

The shape is the same for any other stdio MCP client: a command, and
`["mcp", "--config", "<path>"]` as its arguments.

Check it starts before wiring it up — a config error is reported at startup,
exit 1, rather than as a tool error buried in a transcript:

```bash
mail-muncher validate --config /Users/you/.config/mail-muncher/config.yml
```

`--log-level debug` is safe to leave on. stdout carries protocol frames and
nothing else; every log line goes to stderr, where the client collects it.

## What it can and cannot do

- **Read-only over mail.** No tool sends, deletes, moves, or modifies anything —
  not in the mailbox, and not on disk. `sync` is the single tool that changes
  anything at all, and it can only add files.
- **Paths are jailed to the configured `dest` roots.** A caller-supplied path is
  checked lexically against each rule's `dest`, then resolved with symlinks
  followed and checked again, so neither `..` nor a symlink planted inside the
  tree reaches a file the archive does not own. Only `.eml` and `.md` files are
  readable, and the directory walk never follows symlinks.
- **Nothing outside the archive is reachable or named.** Not the config file,
  not `credentials_file` or `token_file`, not the state directory, not the
  quarantine directory. The one exception is deliberate: `list_rules` names each
  rule's `from_domains_file` and `from_regex_file`, because those files belong to
  the agent.
- **A refused path is indistinguishable from a missing one**, so the jail cannot
  be used to probe the filesystem.
- **The config is read once, at startup.** Editing `config.yml` needs a restart.
  The `from_domains_file` and `from_regex_file` lists it points at are *not*
  cached — they are re-read on every `list_rules` call.

## `list_rules`

No arguments. Start here: it is the only tool that says what is being collected.

The point of it is the resolution. Every externally-owned file a rule reads —
every `from_domains_file` **and** every `from_regex_file` in its match tree — is
read **at call time**, with the same parsing the filter engine uses, so an agent
that rewrote its own subscription file a second ago sees the new list. A rule
whose subscription is split across both kinds reports both; there is no view of
this tool's output in which half a subscription looks like a whole one.

Returns `{"rules": [...]}`, in config order:

| Field | Type | Meaning |
| --- | --- | --- |
| `name` | string | Rule name. |
| `account` | string, omitted when unset | The account the rule is restricted to. Absent means every account. |
| `accounts` | array of string | The configured accounts this rule actually applies to. |
| `dest` | string | Destination directory, as configured. |
| `formats` | array of string | Renderings this rule writes. |
| `domain_files` | array, omitted when the rule has none | One entry per `from_domains_file`, resolved as of this call. |
| `pattern_files` | array, omitted when the rule has none | One entry per `from_regex_file`, resolved as of this call. |
| `stored_messages` | integer | Messages currently on disk under this rule's `dest`. |

Each `domain_files` entry:

| Field | Type | Meaning |
| --- | --- | --- |
| `path` | string | The domain list, as configured. |
| `exists` | bool | Whether it was readable at the time of the call. |
| `domains` | array of string | The domains currently listed, normalized and de-duplicated. `[]` when there are none. |
| `count` | integer | `domains` length. |
| `modified_at` | RFC3339 UTC, omitted when unreadable | When the file was last written. |
| `note` | string, omitted when the list is fine | Why the list is empty: not written yet, unreadable, a directory, or listing no usable domains. |

Each `pattern_files` entry:

| Field | Type | Meaning |
| --- | --- | --- |
| `path` | string | The pattern list, as configured. |
| `exists` | bool | Whether it was readable at the time of the call. |
| `patterns` | array of string | The RE2 patterns currently in force, as written in the file, de-duplicated. `[]` when there are none. |
| `count` | integer | `patterns` length. |
| `rejected` | integer | How many lines the breadth guards refused: they do not compile, or they match the empty string and so would match every message. |
| `modified_at` | RFC3339 UTC, omitted when unreadable | When the file was last written. |
| `note` | string, omitted when nothing is wrong | Why the list is empty or shorter than the file. |

```json
{
  "rules": [
    {
      "name": "job-search",
      "account": "personal",
      "accounts": ["personal"],
      "dest": "/Users/you/Mail/job-search",
      "formats": ["eml", "markdown"],
      "domain_files": [
        {
          "path": "/Users/you/.local/share/jobsearch/domains.txt",
          "exists": true,
          "domains": ["acme.com", "globex.io"],
          "count": 2,
          "modified_at": "2026-07-28T09:02:11Z"
        }
      ],
      "pattern_files": [
        {
          "path": "/Users/you/.local/share/jobsearch/companies.txt",
          "exists": true,
          "patterns": ["wagepoint", "(?i)^careers@acme\\.io$"],
          "count": 2,
          "rejected": 1,
          "modified_at": "2026-07-28T09:02:11Z",
          "note": "1 line rejected: a line is refused when it does not compile, or when it would match every message. Only the 2 patterns listed here are in force"
        }
      ],
      "stored_messages": 37
    }
  ]
}
```

A missing file is an empty list plus a `note`, never a tool error — the program
that owns it may simply not have written it yet.

`rejected` is the field to watch. `count: 1, rejected: 11` means the program
generating that file emitted junk, and the rule is now subscribed to almost
nothing; that is worth telling a human about. The rejected lines' own text is
deliberately **not** returned — `mail-muncher validate` prints them, and a
tool result is the wrong place for an unbounded quantity of somebody else's
file. `pattern_files` was added after `domain_files`; a rule with only a domain
list is reported exactly as it was before.

## `list_messages`

Every argument is optional. With none of them, the newest 50 messages across the
whole archive.

| Argument | Type | Meaning |
| --- | --- | --- |
| `rule` | string | Only messages claimed by this rule. |
| `account` | string | Only messages from this configured account. |
| `thread_id` | string | Only messages in this conversation. |
| `since` | string | Inclusive lower bound on the message date. RFC3339 or `YYYY-MM-DD`. |
| `until` | string | Exclusive upper bound. A bare `YYYY-MM-DD` includes the whole day. |
| `limit` | integer | Maximum to return. Default 50, maximum 500. |
| `group_by_thread` | bool | Also return the same messages grouped into conversations. |

Returns:

| Field | Type | Meaning |
| --- | --- | --- |
| `messages` | array of summary | Newest first. |
| `threads` | array of thread, only when `group_by_thread` | The returned messages grouped, most recently active first. |
| `count` | integer | Messages returned. |
| `matched` | integer | Messages matching the filters *before* the limit. |
| `truncated` | bool | Whether `matched` exceeded the limit. |
| `limit` | integer | The limit actually applied. |

A message summary:

| Field | Type | Meaning |
| --- | --- | --- |
| `id` | string | Stable identity digest for this message. Pass it to `read_message`. |
| `path` | string | Absolute path of the preferred rendering on disk. |
| `rule` | string | Rule that claimed it. |
| `account` | string | Account it was fetched from. |
| `from` | string | `From` header, display name included when there is one. |
| `to` | array of string | `To` recipients. |
| `subject` | string | Subject header. |
| `date` | RFC3339 UTC | Message date. |
| `message_id` | string | RFC822 `Message-ID`. |
| `thread_id` | string | Conversation id. Never empty. |
| `thread_id_source` | string | `provider`, `references`, `in_reply_to` or `self`. |
| `labels` | array of string | Provider labels or folders. |
| `has_attachment` | bool | Whether it carries a non-inline attachment. |
| `formats` | array of string | Renderings of this message that exist on disk. |
| `snippet` | string | Text around the search hit. `search_messages` only. |

A thread adds `thread_id`, `subject` (of its oldest message), `count`,
`first_date`, `last_date`, `participants` (distinct `From` addresses, oldest
first) and `messages` (oldest first — reading order).

Two details worth knowing:

- A thread's `thread_id_source` is the **weakest** among its messages, so a
  partly-reconstructed thread is not reported as provider-grouped.
- `group_by_thread` groups *the page you got back*. A thread whose other
  messages fell outside the filters or the limit is shown partially; filter by
  `thread_id` to get all of it.

## `read_message`

Exactly one of `id` or `path` is required.

| Argument | Type | Meaning |
| --- | --- | --- |
| `id` | string | The `id` from a message summary. |
| `path` | string | A path from a summary or from a run manifest. Must be inside a configured `dest`. |
| `thread` | bool | Also return every other message in the same conversation, oldest first. |
| `max_body_chars` | integer | Truncate each body to this many characters. Default 20000, maximum 500000. |

Returns `{"message": {...}}`, plus `"thread": [...]` when `thread` was set. The
detail shape is a summary plus:

| Field | Type | Meaning |
| --- | --- | --- |
| `cc` | array of string | `Cc` recipients. |
| `in_reply_to` | string, omitted when empty | The parent message's id. |
| `attachments` | array, omitted when none | `{filename, content_type, bytes}` per attachment. |
| `files` | array | `{format, path, bytes}` for every rendering on disk. |
| `body` | string | The message body. |
| `body_format` | `"markdown"` \| `"text"` | `markdown` when the body came from a rendered `.md` or an HTML part. |
| `body_truncated` | bool | Whether `max_body_chars` cut it. |

Attachment **bytes are never returned**. An agent that wants one opens the file
next to the message, from the path in `attachments` / the `.attachments/`
directory beside the `.md`.

Returning the thread is nearly free — the index already groups by thread id — so
`thread: true` is a flag rather than a second round trip. Use it: a hiring
process, a booking or a support case is a conversation, not a message.

## `search_messages`

| Argument | Type | Meaning |
| --- | --- | --- |
| `query` | string, **required** | Text to look for. |
| `rule`, `account`, `thread_id`, `since`, `until`, `limit` | | Exactly as `list_messages`. |

Case-insensitive substring search across subject, sender, recipients, labels,
attachment names **and body**. Returns the same shape as `list_messages` plus
the echoed `query`, and each summary carries a `snippet` — the text around the
first hit, whitespace collapsed, with `…` where it was cut.

This is substring matching, not a query language: there is no index, no
stemming, and no boolean syntax. A blank query is an error rather than a match
on everything.

## `sync`

| Argument | Type | Meaning |
| --- | --- | --- |
| `dry_run` | bool | Evaluate rules and report what would be written, without writing anything. |

Runs one pipeline cycle — the same one `run` and `daemon` run, cross-process
lock included — and returns:

```json
{
  "dry_run": false,
  "manifests": [ { "account": "personal", "...": "..." } ],
  "error": "why the cycle ended early, if it did"
}
```

One manifest per configured account, in config order. The full field-by-field
contract is [docs/manifest.md](manifest.md) — it is the same struct
`run --json` writes.

Two guards keep it well-behaved as a server:

- **One sync at a time in this process.** A second concurrent call is refused
  immediately rather than queued, because a tool call that blocks for the length
  of a mail fetch is indistinguishable, to an agent, from a hung server.
- **A lock held by another process** — a cron `run`, a daemon tick — comes back
  as `a sync is already in progress; try again shortly`. It is never waited on
  and never retried.

A provider failure is deliberately **not** a tool error: the manifests still
describe everything that landed before it, so they are returned with the failure
recorded alongside in `error`.

## Limits

Part of the contract: a call that asks for more comes back capped, with
`truncated` set, rather than refused.

| Limit | Value |
| --- | --- |
| Default `limit` | 50 |
| Maximum `limit` | 500 |
| Default `max_body_chars` | 20000 |
| Maximum `max_body_chars` | 500000 |
| Search snippet context | 120 characters each side |

## Errors

Tool errors are ordinary results with `isError` set, so the agent sees them and
can act:

| Error | Cause |
| --- | --- |
| `path is not inside a configured destination directory` | The path jail refused it — or nothing is there. The two are deliberately indistinguishable. |
| `no such stored message` | The `id` or `path` names nothing in the archive. |
| `one of id or path is required` / `give either id or path, not both` | `read_message` was called with neither or with both. |
| `query is required and must not be blank` | `search_messages` with an empty query. |
| `a sync is already in progress; try again shortly` | Another cycle holds the lock, here or in another process. |
| `cannot read ... as a date` | A `since` / `until` that is neither RFC3339 nor `YYYY-MM-DD`. |

Startup failures are different: a config that will not load or validate exits 1
before any client connects.

## Troubleshooting

**The server exits immediately with `no rule has a dest directory to read`.**
Every rule needs a `dest`; with none, there is no archive to serve.

**`list_messages` returns nothing but mail is on disk.** The server only reads
files under a configured `dest`. Check `list_rules` — its `dest` and
`stored_messages` say exactly which directories it is looking at and how many
messages it found in each.

**A message is missing its `.eml` or `.md`.** `formats` on the summary lists the
renderings that actually exist. A rule with `formats: [eml]` produces no
markdown, and `read_message` then has no body to return from a rendered `.md`.

**The client reports a protocol error.** Something wrote to stdout. Nothing in
mail-muncher does — all logging goes to stderr — so check for a wrapper script
echoing before `exec`.
