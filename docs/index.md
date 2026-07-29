---
title: Home
layout: default
nav_order: 1
description: >-
  mail-muncher is an email client for AI agents: it pulls Gmail read-only,
  filters it with ordered composable rules, and delivers matches to disk as
  .eml plus markdown with YAML frontmatter, with an MCP server over the archive.
permalink: /
---

# mail-muncher

**An email client for AI agents.** mail-muncher pulls messages from Gmail with a
read-only OAuth scope, evaluates each one against ordered composable filter
rules, and writes the matches to a directory as byte-faithful `.eml` plus
optional markdown with YAML frontmatter.

Its distinguishing idea: a rule's filter input can be a plain text file that a
*separate program owns*, re-read at the start of every cycle. An agent declares
what mail it wants by editing that file, and the very next cycle delivers it —
no config edit, no restart, no redeploy.

[Get started](gmail-setup.md){: .btn .btn-primary }
[Configuration reference](configuration.md){: .btn }
[View on GitHub](https://github.com/craigjmidwinter/mail-muncher){: .btn }

---

## The agent workflow

The loop is fully decoupled. The agent never calls mail-muncher and
mail-muncher never calls the agent; they share two paths on disk.

**1. The agent declares what it wants.** It appends to a file it owns:

```bash
mkdir -p ~/.local/share/agent
printf '%s\n' 'acme.com' 'globex.io' >> ~/.local/share/agent/domains.txt
```

**2. One rule subscribes to that declaration.**

```yaml
rules:
  - name: agent-inbox
    match:
      from_domains_file: ~/.local/share/agent/domains.txt
    dest: ~/mail/agent-inbox
    formats: [eml, markdown]
```

**3. Every cycle re-reads the file.** Run it from cron, or leave the daemon
running:

```bash
mail-muncher run                    # one cycle — the cron entrypoint
mail-muncher daemon --interval 5m   # poll forever
```

**4. Matched mail lands in `dest` as files the agent reads.**

```
~/mail/agent-inbox/
└── 2026/
    └── 07/
        ├── 1785230100-a00d5c5e-re-your-application.eml
        ├── 1785230100-a00d5c5e-re-your-application.md
        └── 1785230100-a00d5c5e-re-your-application.attachments/
            └── offer.pdf
```

The `.md` is the consumable rendering — parse the frontmatter, feed the body to
a model, open attachments from the sibling directory:

```markdown
---
subject: 'Re: Your application for Senior Engineer'
from: Jane Doe <jane@acme.com>
to: [me@example.com]
date: 2026-07-28T09:15:00Z
message_id: <abc123@acme.com>
thread_id: 18f2a9c4d5e6
thread_id_source: provider
account: personal
rule: job-search
attachments: [offer.pdf]
---

Hi there, thanks for applying.
```

Group by `thread_id` — a hiring process, a booking or a support case is a
conversation, not a message.

## Why it is safe in an autonomous loop

- **Read-only by construction.** The only OAuth scope requested is
  `gmail.readonly`. Whatever consumes the output — and whatever bug it has —
  cannot send, delete, or modify mail.
- **Idempotent delivery.** A message's filename embeds a digest of
  `account + ":" + message id`, so its destination path is a pure function of
  its identity. Re-run, replay after losing state, crash mid-cycle, or overlap
  two cron invocations: the tree converges and nothing is processed twice.
- **Deterministic routing.** Rules are ordered and first-match-wins, so each
  message is written by exactly one rule. Give each consumer its own rule and
  its own `dest` and each gets a private mailbox nothing else writes into.

## Install

Requires Go 1.25 or newer.

```bash
go install github.com/craigjmidwinter/mail-muncher/cmd/mail-muncher@latest
```

Or from a clone, which stamps the version from `git describe`:

```bash
git clone https://github.com/craigjmidwinter/mail-muncher
cd mail-muncher
make build          # -> ./mail-muncher
```

Then create a Google OAuth client in your own Google Cloud project (mail-muncher
ships none of its own — see [Gmail setup](gmail-setup.md)), and:

```bash
mail-muncher auth --account personal   # OAuth, opens a browser
mail-muncher validate                  # parse, compile rules, resolve files
mail-muncher run --dry-run             # evaluate, write nothing
mail-muncher run                       # for real
```

## Use it from an agent

Two integration points beyond the files themselves:

**A JSON manifest.** `mail-muncher run --json` writes newline-delimited JSON to
stdout, one object per account, listing every rendering stored and skipped this
cycle with its path, rule, subject, sender, date and `thread_id`. No directory
diffing required.

**An MCP server.** `mail-muncher mcp` speaks the Model Context Protocol over
stdio and exposes five tools over the stored archive:

| Tool | Purpose |
| --- | --- |
| `list_messages` | List archived messages, newest first; filter by rule, account, thread or date range. |
| `read_message` | Read one message in full, optionally with its whole conversation. |
| `search_messages` | Case-insensitive search across subject, sender, recipients, labels, attachment names and body. |
| `list_rules` | What each rule collects, and — read fresh on every call — the domains currently subscribed. |
| `sync` | Fetch new mail once and return a manifest. Only ever adds files. |

Register it with any MCP client:

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

Claude Code users can install the bundled plugin, which registers the MCP server
and ships a skill that teaches an agent to use the tool:

```
/plugin marketplace add craigjmidwinter/mail-muncher
/plugin install mail-muncher@mail-muncher
```

## Documentation

| Page | What is in it |
| --- | --- |
| [Gmail setup](gmail-setup.md) | The Google Cloud walkthrough: project, API, consent screen, Desktop app OAuth client — plus every OAuth error message and its fix. Read the consent-screen section before you start. |
| [Configuration](configuration.md) | Every config key, its default, its validation rule, and its failure mode. |
| [Filters](filters.md) | The complete match-tree language — combinators, every predicate, the externally-owned domain file format — plus a cookbook of real rules. |
| [Architecture](architecture.md) | The pipeline, its seams, and where a change belongs. |

Also in the repository:
[README](https://github.com/craigjmidwinter/mail-muncher#readme) ·
[CONTRIBUTING](https://github.com/craigjmidwinter/mail-muncher/blob/main/CONTRIBUTING.md) ·
[example configs](https://github.com/craigjmidwinter/mail-muncher/tree/main/examples)

## Status and scope

Pre-1.0. The current release is
[v0.1.0](https://github.com/craigjmidwinter/mail-muncher/releases/latest). The
config schema is stable enough to write against, but treat it as subject to
change until 1.0.

| Area | Status |
| --- | --- |
| Gmail provider (OAuth, full scan, incremental history sync, RAW download) | Built |
| Config loading and validation | Built |
| Filter engine (all combinators and predicates) | Built |
| `.eml` and markdown sinks | Built |
| `run`, `daemon`, lockfile | Built |
| MCP server (`mail-muncher mcp`) | Built |
| IMAP provider | **Planned, not built.** The config rejects any provider other than `gmail`. |

**Deliberately out of scope:** writing to your mailbox (no labelling, deletion,
sending or drafts — the read-only scope is a design constraint, not a phase);
being a mail client with a search index, threading UI or GUI; and a
network-facing API. Delivery is files on disk plus MCP over stdio.

## Alternatives

Several tools do the fetch-filter-deliver shape well, and some are a better fit.
[getmail6](https://github.com/getmail6/getmail6) is the mature, widely packaged
fetcher when a human (or mutt, or notmuch) is the consumer.
[fdm](https://github.com/nicm/fdm) gives per-rule Maildir destinations with a
compact config. [lieer](https://github.com/gauteh/lieer) syncs a whole Gmail
mailbox bidirectionally into a local Maildir for notmuch. `mbsync` and
`offlineimap` replicate the full mailbox so you can filter locally afterwards.

What none of them do, and what mail-muncher exists for: take filter input from a
file another program owns and re-read it every cycle, and emit a rendering built
for a program to consume rather than for a mail client to display. If you do not
need both, one of the tools above will serve you better and has years more
mileage.

---

MIT licensed. Source at
[github.com/craigjmidwinter/mail-muncher](https://github.com/craigjmidwinter/mail-muncher).
