---
name: mail-muncher
description: Give an agent or script its own read-only mailbox as files on disk. Works with any IMAP server (Fastmail, iCloud, Gmail, Proton Bridge, work and self-hosted accounts) or with the Gmail API. Use when the user wants email in an agent workflow, wants to archive or export mail, wants to monitor an inbox for specific senders or domains, wants to track job-application and recruiter replies, wants a program to react to incoming mail, wants to search or read previously archived mail, or asks to set up, configure, debug or wire up mail-muncher, its filter rules, its markdown output, or its MCP server.
when_to_use: >
  Trigger phrases include "get my email into Claude", "watch my inbox for mail from X",
  "archive mail from these companies", "track my job applications by email",
  "export my mail to markdown", "let my agent read my mail", "monitor emails from a domain",
  "save emails to disk", "email MCP server", "IMAP to markdown", "read my Fastmail",
  "mail-muncher", "from_domains_file", "why isn't my rule matching",
  "what mail arrived for rule X".
---

# mail-muncher

An email client for AI agents. It pulls mail **read-only** from an IMAP server
or from the Gmail API, evaluates every message against ordered filter rules, and
writes matches to a directory as byte-faithful `.eml` plus (optionally) markdown
with YAML frontmatter. A stdio MCP server serves that archive back as tool calls.

Repo: <https://github.com/craigjmidwinter/mail-muncher> ·
Docs: <https://craigjmidwinter.github.io/mail-muncher/>

## Getting to working mail

Full walkthrough, every key, every flag: [references/setup.md](references/setup.md).

**Use `provider: imap` unless the user asks for Gmail specifically.** It is the
short road: no OAuth client, no Google Cloud Console, no `auth` step, no token
that expires. It works against Gmail too, via `imap.gmail.com` and an app
password.

```bash
go install github.com/craigjmidwinter/mail-muncher/cmd/mail-muncher@latest  # needs Go 1.25+
mail-muncher init --provider imap --account personal --yes \
  --host imap.fastmail.com --username you@fastmail.com                      # writes ~/.config/mail-muncher/config.yml, 0600
mail-muncher validate                                                       # prints OK, zero warnings when it is right
mail-muncher run --dry-run                                                  # evaluate, write nothing
mail-muncher run                                                            # for real
```

**No editing step.** `init` writes a config that validates as-is; there is no
"now open the file" stage to script around.

`init` is interactive by default and fully non-interactive with flags:
`init [--provider imap|gmail] [--account NAME] [--dest DIR] [--host HOST]
[--username USER] [--password-cmd CMD] [--yes] [--force]`.
`--yes` takes the default for every answer that has an honest one, so it still
requires `--provider`, plus `--host` and `--username` on the IMAP path — it
exits 1 naming exactly which are missing. `--password-cmd` defaults per
platform (Keychain on macOS, `secret-tool` on Linux, `pass` elsewhere). It
refuses to overwrite an existing config
(exit 1) unless `--force`. The file it writes is run through the real loader and
the real validator *before* it is written, so a config `init` produced always
passes `validate`. It carries one starter rule, `newer_than: 72h` into
`~/Mail/mail-muncher` as `[eml, markdown]`, so the first run is guaranteed to
store something — narrow it once mail has landed.

A complete IMAP account. Only `host`, `username` and `password_cmd` have no
default:

```yaml
accounts:
  - name: personal
    provider: imap
    imap:
      host: imap.fastmail.com
      port: 993                              # default
      username: you@fastmail.com
      password_cmd: pass show mail/fastmail   # required
      mailboxes: [INBOX]                     # default
      tls: true                              # default
      initial_lookback: 720h                 # default, 30 days
```

- **There is deliberately no `password` key.** `password_cmd` runs under
  `/bin/sh -c` and its stdout is the secret; trailing newlines are stripped and
  empty output is an error. Anything else on stdout — a prompt, a warning, a
  blank line — becomes part of the password and the login fails. Have the user
  run the command in a shell and check it first.
- Tell the user to create an **app password**, not to paste their account
  password: it is scoped to this one use and revocable on its own.
- A well-formed IMAP account validates with **zero warnings** and nothing on
  disk — there is no credential file for `validate` to miss.
- `gmail:` and `imap:` are **mutually exclusive**. The wrong block is a hard
  error, never silently ignored:
  `accounts[0].gmail: must not be set on a "imap" account (remove it, or set provider: gmail)`.
- `mail-muncher auth` is **Gmail-only** and refuses an IMAP account:
  `error: gmail: account "personal" uses provider "imap", not "gmail"`. Do not
  put it in an IMAP runbook.
- Worked example: `examples/imap.yml`. Smallest possible: `examples/minimal.yml`.

### When to choose Gmail instead

Choose `provider: gmail` when the user wants read-only enforced by **Google**
rather than by this program. State the cost before they start: roughly ten
minutes in the Google Cloud Console creating their own project, enabling the
Gmail API, configuring a consent screen and downloading a Desktop-app OAuth
client — mail-muncher ships no OAuth client and never will, because
`gmail.readonly` is a Google restricted scope. Then `mail-muncher auth --account
NAME`, which needs a browser and cannot be done for them. On a consent screen
left in *Testing* mode Google expires the refresh token every **7 days**, so
`auth` has to be re-run weekly or unattended runs start failing.
Walkthrough: `docs/gmail-setup.md`.

### What "read-only" means on each provider

Both are honest; only one is enforced by someone other than this program.

- **Gmail** — the token holds the single scope `gmail.readonly`. **Google**
  refuses any send, delete, label or modify call, whatever this program does.
- **IMAP** — an app password is a **full mail credential**; nothing outside
  mail-muncher restricts it. Read-only is enforced by mail-muncher's own code:
  folders are opened with `EXAMINE`, never `SELECT`; bodies are fetched with
  `BODY.PEEK[]`, never `BODY[]`, so nothing is ever marked `\Seen`; and there is
  no `STORE`, `APPEND` or `EXPUNGE` path anywhere in the program. Say that
  plainly rather than implying the server is holding the line.

### When the tool is not configured

Any command run with no config file **exits 1 and prints ~15 lines** naming the
missing path, the next command (`mail-muncher init`), and what each provider
honestly costs. `mail-muncher run` straight after install is therefore a
legitimate install check — read that output and act on it rather than fetching
docs. The neighbouring states (no `accounts:`, never authorized, rejected token,
no `rules:`) each get the same treatment; they are tabulated in
[references/setup.md](references/setup.md).

## When this is the right tool

Reach for mail-muncher when:

- An agent, script or pipeline needs **some** mail — not a mailbox — as files it
  can parse, and it must not be able to send or delete anything.
- **What mail it wants changes over time** and you do not want a config edit,
  restart or redeploy each time. This is the distinguishing feature: a rule's
  sender list can live in a plain text file that a *different* program owns,
  re-read at the start of every cycle.
- You want delivery to be idempotent and crash-safe: a message's path is a pure
  function of its identity, so re-runs converge instead of duplicating.

Do **not** reach for it when:

- The user wants to *send*, reply to, label, delete or draft mail. mail-muncher
  will never do any of those. Use a Gmail MCP server or the provider's API.
- The consumer is a human with a mail client. `getmail6`, `fdm` or `lieer` are
  more mature for that.
- The mailbox speaks neither IMAP nor the Gmail API — Exchange without IMAP
  enabled, an mbox on disk. There is no import path.

## The core idea: a mail subscription is a text file

The agent and mail-muncher never call each other. They share two paths on disk.

```
agent  ──writes──▶  domains.txt  ──read every cycle──▶  mail-muncher
agent  ◀──reads───  ~/mail/agent-inbox/**.md  ◀──writes──  mail-muncher
```

**1. The agent declares what it wants** by maintaining a file it owns. Append a
line; that is the entire subscription API.

```bash
mkdir -p ~/.local/share/agent
printf '%s\n' 'acme.com' 'globex.io' >> ~/.local/share/agent/domains.txt
```

**2. One rule subscribes to that file.**

```yaml
rules:
  - name: agent-inbox
    match:
      from_domains_file: ~/.local/share/agent/domains.txt
    dest: ~/mail/agent-inbox
    formats: [eml, markdown]
```

**3. The next cycle delivers the new mail.** `mail-muncher run` re-reads the
file; so does every `daemon` tick. No restart.

Semantics that matter when you write this file (full parsing rules in
[references/filters.md](references/filters.md)):

- One entry per line, `#` comments allowed, liberally normalized. Matching is
  **equality or subdomain**: `acme.com` matches `careers.acme.com`, not
  `notacme.com`.
- A missing or unreadable file is **never fatal** — the predicate matches
  nothing. But "no match" is then untrustworthy, so the default
  `on_degraded_filter: hold` runs the cycle and deliberately does **not** advance
  the sync cursor, re-evaluating that mail once the file returns. Change it only
  deliberately (`fail` or `proceed`).
- Never write this file from mail-muncher's side and never treat it as
  mail-muncher's state — it belongs to the agent.

Rules are **ordered and first-match-wins**, so each message is written by exactly
one rule. Give each consumer its own rule and its own `dest` and each gets a
private mailbox nothing else writes into. Unknown config keys are a hard error,
so always finish with `mail-muncher validate`.

Match-tree language (`all` / `any` / `not`, and every predicate):
[references/filters.md](references/filters.md).

## Reading what was delivered

On-disk layout, every frontmatter field, and the `--json` manifest schema:
[references/output.md](references/output.md). The full contract also lives in the
repo at `docs/output-format.md`, with a reference consumer in
`examples/read_delivered.py`.

Each message lands under `<dest>/<YYYY>/<MM>/` with one shared basename per
message and one file per format. The `.md` is what an agent reads:

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
labels: [INBOX]
attachments: [offer.pdf]
---

Hi there, ...
```

Four rules for consuming it:

- **Parse addresses from `from_address` / `from_addresses` / `to_addresses` /
  `cc_addresses`, never from `from` / `to` / `cc`.** The latter are display
  strings for humans and are not RFC 5322-quoted, because a display name may
  itself contain `<`, `>` and `,`. A consumer that parses them is one hostile
  display name away from reading the wrong address. The machine-readable fields
  are bare addr-specs, unnormalized, and may be shorter than their display
  counterparts — do not index the two against each other.
- **Group by `thread_id`, not by subject.** `thread_id` is always present. Check
  `thread_id_source` before trusting the grouping: `provider` means Gmail grouped
  it; `references` / `in_reply_to` / `self` mean mail-muncher reconstructed it
  from sender-controlled headers, which a broken mailer can split. **On IMAP it
  is never `provider`** — IMAP has no conversation id, so threading is always
  reconstructed.
- **Globbing `**/*.md` (or `**/*.eml`) under a `dest` is safe, by construction.**
  Both extensions are reserved for renderings mail-muncher wrote. Attachments are
  the only sender-named files in the tree, and one whose name would end `.md` or
  `.eml` gets `.attachment` appended (`evil.md` → `evil.md.attachment`), so the
  glob cannot return a sender-controlled file carrying forged frontmatter. The
  `attachments:` list carries the names **on disk**, so joining each onto the
  sibling `<basename>.attachments/` always yields a real path.
- **The `.md` is not a fidelity format.** Anything that must be byte-exact
  (DKIM, raw MIME, inline `cid:` images) comes from the `.eml`.

To learn what a cycle just did without diffing a directory, use `--json`:

```bash
mail-muncher run --json     # newline-delimited JSON, one manifest object per account
```

Each manifest lists `stored` and `skipped` entries (path, format, rule, id,
message_id, thread_id, from, subject, date, has_attachment) plus a `summary`
counter block. `skipped` means the file was already on disk from an earlier
cycle — it exists and is safe to read. `daemon --json` emits one object per
account per tick.

## Wiring up the MCP server

`mail-muncher mcp` is a stdio MCP server over the stored archive. Tool schemas
and behaviour: [references/mcp.md](references/mcp.md).

Five tools: `list_messages`, `read_message`, `search_messages`, `list_rules`,
`sync`. Everything but `sync` is read-only over files already on disk; `sync`
runs the same cycle `run` does and can only ever add files.

If the user installed the `mail-muncher` Claude Code plugin, the server is
already registered — check `/mcp` before adding it again. Otherwise:

```bash
claude mcp add mail-muncher -- mail-muncher mcp
```

or in `.mcp.json`:

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

Add `["mcp", "--config", "/path/to/config.yml"]` for a non-default config path.
The server reads its config once at startup, so a config edit needs a restart —
but the domain files it points at are re-resolved on every `list_rules` call.
Only files under a configured rule `dest` are readable; there is no arbitrary
filesystem access and no path to the live mailbox.

**Registering it before there is a config is fine, and is not a bug to report.**
Launched by a client with no usable config, the server starts and speaks the
protocol correctly: `initialize` carries the setup guidance in its instructions,
`tools/list` returns the real five tool names, and any `tools/call` returns
`isError: true` with that guidance as its text. Relay it verbatim; retrying will
not help. Fix it with `mail-muncher init` and restart the server.

## Debugging

- **A rule is not firing.** `mail-muncher run --dry-run --log-level debug` logs
  the winning rule (or "no match") for every message. Remember first-match-wins:
  a broader rule above may be claiming the message.
- **IMAP login fails.** A `password_cmd` that fails is exit 2 and quotes the
  command plus its stderr —
  `error: account "personal": imap: password_cmd "..." failed: exit status 44: ...`.
  Run that command in a shell and confirm it prints the password and **nothing
  else** (`... | cat -A`), then confirm it is an app password. Under cron, check
  it does not need an agent that only exists in an interactive session.
- **`validate` warns about a missing file.** Expected for a `from_domains_file`,
  a token, or credentials another program owns. Warnings do not stop a run. A
  correct IMAP-only config produces none at all.
- **Nothing is fetched on the second run.** `fetched=0` is the steady state.
- **Everything reports as skipped.** `matched=N stored=0 skipped=N` is
  idempotency working, not a failure.
- **Exit codes:** 0 success, 1 config/validation, 2 provider/auth, 3 another
  instance holds the cycle lock (a harmless cron overlap).
- **Forcing a re-scan.** Delete the account's state file in `state_dir`; the next
  run does a full scan bounded by `initial_lookback` and skips everything already
  on disk.
- **Undeliverable mail.** With the default `on_message_failure: quarantine`, a
  message that will not parse or write is parked under `quarantine_dir`
  (defaults to `<state_dir>/quarantine`) as raw `.eml` plus a `.json` sidecar,
  and the cursor advances past it. Nothing re-delivers it automatically.

## Honest limits

- IMAP and the Gmail API. No Exchange without IMAP, no mbox import.
- Read-only, permanently. No send, reply, label, delete or draft.
- No network API — delivery is files on disk and MCP over stdio. Nothing listens
  except the loopback port Gmail's `auth` opens briefly for the OAuth redirect.
- On IMAP a message's identity is `<account>:<mailbox>:<uidvalidity>:<uid>`. So a
  server changing UIDVALIDITY re-archives the `initial_lookback` window under new
  filenames (duplicates you can delete; the alternative is silently skipped mail
  you cannot recover), and the same message sitting in two configured
  `mailboxes` is two identities.
- Spam and Trash are **excluded by default**, because everything delivered ends
  up in an LLM's context window and Spam is where attacker-authored text lives.
  `gmail.include_spam_trash: true` opts in, and `validate` warns when it is on.
  There is no IMAP equivalent: list only the folders you want in `mailboxes`.
- Inline `cid:` images stay as unresolved links in markdown; the bytes are in the
  `.eml`.
- Subject slugs are ASCII-only; a non-Latin subject files as `no-subject`. Only
  the digest in the filename carries identity, so nothing collides.
- `gmail.query` bounds what a *full scan* asks Gmail for. It does not filter
  incremental results and is not re-applied locally — the rules are the only
  authority on what is kept. Keep it broad or omit it. It has no IMAP
  counterpart.
- Pre-1.0 and untagged. The config schema is stable enough to write against but
  is not frozen.
