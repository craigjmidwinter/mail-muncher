---
title: Home
layout: default
nav_order: 1
description: >-
  mail-muncher is an email client for AI agents: it reads any IMAP mailbox, or
  Gmail read-only, filters it with ordered composable rules, and delivers
  matches to disk as .eml plus markdown with YAML frontmatter, with an MCP
  server over the archive.
permalink: /
---

# mail-muncher

**An email client for AI agents.** mail-muncher pulls messages from any IMAP
mailbox — Gmail, Fastmail, iCloud, Proton Bridge, a work account, your own
server — or from Gmail's API with a read-only OAuth scope, evaluates each
message against ordered composable filter rules, and writes the matches to a
directory as byte-faithful `.eml` plus optional markdown with YAML frontmatter.

Its distinguishing idea: a rule's filter input can be a plain text file that a
*separate program owns*, re-read at the start of every cycle. An agent declares
what mail it wants by editing that file, and the very next cycle delivers it —
no config edit, no restart, no redeploy.

[Get started](#quickstart){: .btn .btn-primary }
[Configuration reference](configuration.md){: .btn }
[View on GitHub](https://github.com/craigjmidwinter/mail-muncher){: .btn }

---

## Two ways to connect a mailbox

Pick one before installing. Both are supported, and everything downstream —
rules, formats, filenames, the archive layout, the MCP tools — is identical
either way.

| | `provider: imap` | `provider: gmail` |
| --- | --- | --- |
| Setup time | **~2 min** | **~10 min** in the Google Cloud Console |
| What you register | nothing | your own Google Cloud project and Desktop-app OAuth client |
| Credential | an app password from your provider's settings page | an OAuth token, scope `gmail.readonly` |
| How wide that credential is | **a full mail credential.** An app password can send and delete | read-only, and nothing else |
| Who enforces read-only | **mail-muncher's own code** | **Google** |
| Expiry | none | **every 7 days** on a Testing-mode consent screen; `mail-muncher auth` has to be re-run weekly |
| Where the secret lives | wherever your password manager already keeps it: `password_cmd` is run and its stdout is the password. There is deliberately no `password` key | `token.json`, mode 0600, written by `mail-muncher auth` |
| Which mailboxes | the folders you list in `mailboxes:`; `[INBOX]` by default | the whole Gmail account, minus Spam and Trash unless you ask for them |
| Works with | Gmail, Fastmail, iCloud, Proton Bridge, work accounts, self-hosted | Gmail only |
| Extra steps | none. There is no `auth` command on this path | `mail-muncher auth`, after [Gmail setup](gmail-setup.md) |

**Read-only is real on both paths, but it is not the same guarantee.** On Gmail
it is Google's: the `gmail.readonly` token is *incapable* of sending, deleting
or labelling, whatever this program does. On IMAP the credential is a full mail
credential and the guarantee is mail-muncher's own — every folder opened with
`EXAMINE` and never `SELECT`, every body fetched with `BODY.PEEK[]` and never
`BODY[]` so mail is never marked read, and no code path anywhere that issues
`STORE`, `APPEND` or `EXPUNGE`. Both are honest; only one is enforced by
somebody other than this program.

If you have no specific reason to want the Gmail API, start with IMAP. It works
on a Gmail account too.

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
from_address: jane@acme.com
from_addresses: [jane@acme.com]
to: [me@example.com]
to_addresses: [me@example.com]
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

- **Read-only by construction.** Nothing in mail-muncher writes to a mailbox —
  Google's enforcement of `gmail.readonly` on the Gmail path, `EXAMINE` plus
  `BODY.PEEK[]` and no write path at all on IMAP. Whatever consumes the output,
  and whatever bug it has, cannot send, delete, or modify mail.
- **Idempotent delivery.** A message's filename embeds a digest of
  `account + ":" + message id`, so its destination path is a pure function of
  its identity. Re-run, replay after losing state, crash mid-cycle, or overlap
  two cron invocations: the tree converges and nothing is processed twice.
- **Deterministic routing.** Rules are ordered and first-match-wins, so each
  message is written by exactly one rule. Give each consumer its own rule and
  its own `dest` and each gets a private mailbox nothing else writes into.

## Install

```bash
brew install craigjmidwinter/tap/mail-muncher
```

Prebuilt binaries for macOS and Linux (amd64 and arm64), with cosign-verifiable
checksums, are on the
[releases page](https://github.com/craigjmidwinter/mail-muncher/releases/latest).
With a Go 1.25+ toolchain,
`go install github.com/craigjmidwinter/mail-muncher/cmd/mail-muncher@latest`
also works. Full instructions, including signature verification, are in the
[README](https://github.com/craigjmidwinter/mail-muncher#install).

## Quickstart

About five minutes on the IMAP path, with no browser and no clone.

```bash
mail-muncher run                       # not configured yet: prints what to do next
mail-muncher init --provider imap      # writes a validated starter config
# edit imap.host, imap.username and imap.password_cmd in the file it names
mail-muncher validate                  # parse, compile rules, resolve files
mail-muncher run --dry-run             # connect and evaluate, write nothing
mail-muncher run                       # for real
```

That first `mail-muncher run` is a real install check rather than a mistake:
with no config it exits 1 and prints the path it looked at, the next command,
and what each provider costs. Every command that needs a config does the same,
so a broken install never looks like an unconfigured one.

`init` asks three questions — provider, account name, destination — and takes
`--account`, `--dest` and `--yes` to answer them up front. `--yes` still
requires `--provider`, because the two paths cost different things and there is
no honest default. An existing config is never overwritten without `--force`.
The config it writes carries one starter rule matching everything newer than
72h, so the first run is guaranteed to store something.

The secret never goes in the config: `password_cmd` is run and its stdout is
the password, so it stays in whatever password manager you already use.
[`examples/imap.yml`](https://github.com/craigjmidwinter/mail-muncher/blob/main/examples/imap.yml)
is a fuller worked config.

**For the Gmail API instead**, know the two costs before you start: about ten
minutes in the Google Cloud Console registering your own OAuth client
(`gmail.readonly` is a Google restricted scope, so mail-muncher ships none),
and a refresh token that expires **every 7 days** on a Testing-mode consent
screen, meaning `mail-muncher auth` becomes a weekly chore. Then:

```bash
mail-muncher init --provider gmail     # prints the cost warning, writes the config
# follow the Gmail setup page, saving the client JSON as credentials.json
mail-muncher auth --account personal   # OAuth, opens a browser
mail-muncher validate && mail-muncher run --dry-run && mail-muncher run
```

Step by step: [Gmail setup](gmail-setup.md). Neither the ten minutes nor the
seven days has a workaround; both are Google's, not mail-muncher's.

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

If a client launches `mail-muncher mcp` before there is a config, the server
does not die: it completes the handshake, registers the same five tool names,
and returns the setup guidance as a tool error, so the agent has something to
relay. That is deliberate, not a bug.

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

The bundled skill has not yet caught up with `provider: imap` or
`mail-muncher init` and will walk you through Google Cloud rather than the
two-minute route. Follow the [quickstart](#quickstart) above for IMAP; the
binary supports it fully.

## Documentation

| Page | What is in it |
| --- | --- |
| [Configuration](configuration.md) | Every config key for both providers, its default, its validation rule, and its failure mode. The [`accounts[].imap`](configuration.md#accountsimap) section is the whole IMAP reference — there is no separate setup page, because there is no setup beyond an app password. |
| [Gmail setup](gmail-setup.md) | The Gmail path only: the Google Cloud walkthrough — project, API, consent screen, Desktop app OAuth client — the seven-day token expiry, and every OAuth error message with its fix. Read the consent-screen section before you start. Nothing here applies to an IMAP account. |
| [Filters](filters.md) | The complete match-tree language — combinators, every predicate, the externally-owned domain file format — plus a cookbook of real rules. |
| [Output format](output-format.md) | The on-disk contract programs code against: directory layout, filename convention, every frontmatter key, why you need a real YAML parser, and how to enumerate a delivery tree without ingesting sender-controlled attachments. Read this before writing a consumer. |
| [The run manifest](manifest.md) | The `--json` contract, field by field. |
| [The MCP server](mcp.md) | Client wiring, and every tool's arguments and return shape. |
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
| IMAP provider (`password_cmd`, per-mailbox UID cursors, `EXAMINE` + `BODY.PEEK[]`) | Built |
| Gmail provider (OAuth, full scan, incremental history sync, RAW download) | Built |
| `mail-muncher init` for either provider, interactive or scripted | Built |
| Setup guidance on every unconfigured command, including `mcp` | Built |
| Config loading and validation | Built |
| Filter engine (all combinators and predicates) | Built |
| `.eml` and markdown sinks | Built |
| `run`, `daemon`, lockfile | Built |
| MCP server (`mail-muncher mcp`) | Built |

**Deliberately out of scope:** writing to your mailbox (no labelling, deletion,
sending or drafts, and on IMAP not even marking a message read — read-only is a
design constraint, not a phase); being a mail client with a search index,
threading UI or GUI; and a network-facing API. Delivery is files on disk plus
MCP over stdio.

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
