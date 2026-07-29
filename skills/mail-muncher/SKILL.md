---
name: mail-muncher
description: Give an agent or script its own read-only mailbox as files on disk. Use when the user wants email in an agent workflow, wants to archive or export mail, wants to monitor an inbox for specific senders or domains, wants to track job-application and recruiter replies, wants a program to react to incoming mail, wants to search or read previously archived mail, or asks to set up, configure, debug or wire up mail-muncher, its filter rules, its markdown output, or its MCP server.
when_to_use: >
  Trigger phrases include "get my email into Claude", "watch my inbox for mail from X",
  "archive mail from these companies", "track my job applications by email",
  "export Gmail to markdown", "let my agent read my mail", "monitor emails from a domain",
  "save emails to disk", "email MCP server", "mail-muncher", "from_domains_file",
  "why isn't my rule matching", "what mail arrived for rule X".
---

# mail-muncher

An email client for AI agents. It pulls mail from Gmail with a **read-only**
OAuth scope, evaluates every message against ordered filter rules, and writes
matches to a directory as byte-faithful `.eml` plus (optionally) markdown with
YAML frontmatter. A stdio MCP server serves that archive back as tool calls.

Repo: <https://github.com/craigjmidwinter/mail-muncher> ·
Docs: <https://craigjmidwinter.github.io/mail-muncher/>

## When this is the right tool

Reach for mail-muncher when:

- An agent, script or pipeline needs **some** mail — not a mailbox — as files
  it can parse, and it must not be able to send or delete anything.
- **What mail it wants changes over time** and you do not want a config edit,
  restart or redeploy each time. This is the distinguishing feature: a rule's
  sender list can live in a plain text file that a *different* program owns,
  re-read at the start of every cycle.
- You want mail delivery to be idempotent and crash-safe: a message's path is a
  pure function of its identity, so re-runs converge instead of duplicating.

Do **not** reach for it when:

- The user wants to *send*, reply to, label, delete or draft mail. mail-muncher
  holds `gmail.readonly` by design and will never do any of those. Use the Gmail
  API or a Gmail MCP server instead.
- The consumer is a human with a mail client. `getmail6`, `fdm` or `lieer` are
  more mature for that.
- The mailbox is not Gmail. **Gmail is the only provider implemented.** IMAP is
  specced but not built, and the config rejects any `provider` other than
  `gmail`. Say so plainly rather than writing a config that cannot load.

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

Semantics that matter when you write this file:

- One entry per line. `#` starts a comment; blank lines are ignored; whitespace
  is trimmed; a leading `@` and a trailing `.` are stripped; everything is
  lowercased; duplicates collapse.
- Matching is **equality or subdomain**: `acme.com` matches `acme.com` and
  `careers.acme.com`, but not `notacme.com`.
- A missing or unreadable file is **never fatal** — the predicate simply matches
  nothing. The owning program may not have written it yet.
- Because "no match" is untrustworthy while the file is unreadable, the default
  `on_degraded_filter: hold` runs the cycle but does **not** advance the sync
  cursor, so the same mail is re-evaluated once the file returns. Change this
  only deliberately (`fail` or `proceed`).
- Never write this file from mail-muncher's side and never treat it as
  mail-muncher's state — it belongs to the agent.

## Getting it running

Full walkthrough: [references/setup.md](references/setup.md). The short version:

```bash
go install github.com/craigjmidwinter/mail-muncher/cmd/mail-muncher@latest   # needs Go 1.25+
mkdir -p ~/.config/mail-muncher                                            # then write config.yml
mail-muncher auth --account personal                                       # OAuth, opens a browser
mail-muncher validate                                                      # parse + compile + check files
mail-muncher run --dry-run                                                 # evaluate, write nothing
mail-muncher run                                                           # for real
```

`auth` needs an OAuth **client** JSON that the user creates in their own Google
Cloud project — mail-muncher ships no OAuth client. That step needs a browser
and cannot be automated for them; walk them through
[references/setup.md](references/setup.md) or `docs/gmail-setup.md` in the repo.

A minimal config:

```yaml
state_dir: ~/.local/state/mail-muncher

accounts:
  - name: personal
    provider: gmail
    gmail:
      credentials_file: ~/.config/mail-muncher/credentials.json
      token_file: ~/.config/mail-muncher/token.json
      initial_lookback: 2160h

rules:
  - name: job-search
    account: personal
    match:
      any:
        - from_domains_file: ~/.local/share/jobsearch/domains.txt
        - from_domains: [greenhouse.io, lever.co, ashbyhq.com]
        - subject_regex: "(?i)(your application|application received)"
    dest: ~/Mail/job-search
    formats: [eml, markdown]
```

Rules are **ordered and first-match-wins**, so each message is written by
exactly one rule. Give each consumer its own rule and its own `dest` and each
gets a private mailbox nothing else writes into. Unknown config keys are a hard
error, so always finish with `mail-muncher validate`.

Match-tree language (`all` / `any` / `not`, and every predicate):
[references/filters.md](references/filters.md).

## Reading what was delivered

On-disk layout, frontmatter fields and the `--json` manifest schema:
[references/output.md](references/output.md).

Each message lands under `<dest>/<YYYY>/<MM>/` with one shared basename per
message and one file per format. The `.md` is what an agent reads:

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
labels: [INBOX]
attachments: [offer.pdf]
---

Hi there, ...
```

Two rules for consuming it:

- **Group by `thread_id`, not by subject.** A hiring process, a booking or a
  support case is a conversation. `thread_id` is always present. Check
  `thread_id_source` before trusting the grouping: `provider` means Gmail
  grouped it; `references` / `in_reply_to` / `self` mean mail-muncher
  reconstructed it from sender-controlled headers, which a broken mailer can
  split.
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

## Debugging

- **A rule is not firing.** `mail-muncher run --dry-run --log-level debug` logs
  the winning rule (or "no match") for every message. Remember first-match-wins:
  a broader rule above may be claiming the message.
- **`validate` warns about a missing file.** That is expected for a
  `from_domains_file`, a token, or credentials another program owns. Warnings do
  not stop a run.
- **Nothing is fetched on the second run.** `fetched=0` is the steady state; the
  Gmail history cursor means an incremental cycle barely talks to Gmail.
- **Everything reports as skipped.** `matched=N stored=0 skipped=N` is
  idempotency working, not a failure.
- **Exit codes:** 0 success, 1 config/validation, 2 provider/auth, 3 another
  instance holds the cycle lock (a harmless cron overlap).
- **Forcing a re-scan.** Delete the account's state file in `state_dir`; the
  next run does a full scan bounded by `initial_lookback` and skips everything
  already on disk.
- **Undeliverable mail.** With the default `on_message_failure: quarantine`, a
  message that will not parse or write is parked under `quarantine_dir`
  (defaults to `<state_dir>/quarantine`) as raw `.eml` plus a `.json` sidecar,
  and the cursor advances past it. Nothing re-delivers it automatically.

## Honest limits

- Gmail only. No IMAP, no Exchange, no mbox import.
- Read-only, permanently. No send, reply, label, delete or draft.
- No network API — delivery is files on disk and MCP over stdio. Nothing
  listens except the loopback port `auth` opens briefly for the OAuth redirect.
- Inline `cid:` images stay as unresolved links in markdown; the bytes are in
  the `.eml`.
- Subject slugs are ASCII-only; a non-Latin subject files as `no-subject`. Only
  the digest in the filename carries identity, so nothing collides.
- `gmail.query` bounds what a *full scan* asks Gmail for. It does not filter
  incremental results and is not re-applied locally — the rules are the only
  authority on what is kept. Keep it broad or omit it.
- Pre-1.0 and untagged. The config schema is stable enough to write against but
  is not frozen.
