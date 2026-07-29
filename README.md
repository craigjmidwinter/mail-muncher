# mail-muncher

Give a program its own read-only mailbox, filtered down to exactly the mail it
asked for, delivered as files on disk.

mail-muncher pulls messages from a mail provider, evaluates each one against
ordered rules, and writes the matches to a directory — byte-faithful `.eml`,
and optionally a markdown rendering with the headers as YAML frontmatter, the
body as text, and attachments extracted alongside. A rule can take its filter
input from a plain text file that *some other program owns*, which mail-muncher
re-reads at the start of every cycle. That other program changes one line in
that file, and the very next cycle delivers different mail — no config edit, no
restart, no redeploy.

It runs one-shot for cron, or as a polling daemon, or as a stdio MCP server an
agent can query directly. Every mode emits the same machine-readable manifest of
what it did. It requests exactly one OAuth scope, `gmail.readonly`: it cannot
send, delete, label, or modify anything in your mailbox.

## The problem

An automated process needs some mail. A job-search tracker wants replies from
companies you applied to. A support bot wants messages from one vendor's
domain. A research agent wants every newsletter from three publishers, as text
it can actually read.

The usual answers are all bad. Hand the process your inbox credentials and it
can read (and send, and delete) everything. Give it a mail API integration and
you now maintain an OAuth flow, a sync cursor, MIME parsing, and a dedup story
inside every process that wants mail. Or hard-code the filter into a config
file, and every change to *what it wants* is a config edit and a redeploy.

mail-muncher splits that in half. It owns the credentials, the incremental
sync, the parsing, and the dedup. The consuming program owns a text file
listing what it wants and a directory it reads results from — and, if it prefers
to ask rather than watch, a handful of MCP tools over that same directory.

## The agent workflow

There are two supported shapes, and they compose. Pick by whether your agent
runs on a loop of its own or waits to be asked.

- **File drop** — mail-muncher runs on a schedule and writes files; the agent
  reads the directory. Nothing calls anything. This is the shape below.
- **Tool call** — the agent talks to `mail-muncher mcp` over MCP and asks
  questions directly: what am I subscribed to, what arrived, what does this
  thread say, fetch now. See [Shape 2: tool call](#shape-2-tool-call).

Both read the same archive, and running both at once is normal: a daemon fills
the directory while the MCP server answers questions about it.

### Shape 1: file drop

The loop is fully decoupled: mail-muncher never calls the agent, and the agent
need never call mail-muncher. They share two paths on disk.

**1. The agent declares what it wants.** Append to a file it owns:

```bash
mkdir -p ~/.local/share/agent
cat >> ~/.local/share/agent/domains.txt <<'EOF'
# domains this agent is currently interested in
acme.com
globex.io
EOF
```

**2. mail-muncher subscribes to that declaration.** One rule, pointed at the
file:

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
mail-muncher run                 # one cycle — the cron entrypoint
mail-muncher daemon --interval 5m  # poll forever
```

**4. Matched mail lands in `dest` as files the agent reads.**

```
~/mail/agent-inbox/
└── 2026/
    └── 07/
        ├── 1785230100-a00d5c5e383a1c08-re-your-application-for-senior-engineer.eml
        ├── 1785230100-a00d5c5e383a1c08-re-your-application-for-senior-engineer.md
        └── 1785230100-a00d5c5e383a1c08-re-your-application-for-senior-engineer.attachments/
            └── offer.pdf
```

The `.md` is the consumable rendering — parse the frontmatter, feed the body
to a model, open the attachments from the sibling directory:

```markdown
---
subject: 'Re: Your application for Senior Engineer'
from: Jane Doe <jane@acme.com>
to: [me@example.com]
date: 2026-07-28T09:15:00Z
message_id: <abc123@acme.com>
thread_id: 18fe9c0d1a2b3c4d
thread_id_source: provider
in_reply_to: <application-000@example.com>
account: personal
rule: job-search
labels: [INBOX]
attachments: [offer.pdf]
---

Hi there,

Thanks for applying.

## Attachments

- [offer.pdf](1785230100-a00d5c5e383a1c08-re-your-application-for-senior-engineer.attachments/offer.pdf)
```

`thread_id` is on every message and is never empty, so grouping a directory into
conversations is a `sort` on one field — no reference chains to reassemble.

**5. Optionally, take the manifest instead of walking the tree.** `--json`
writes a machine-readable record of the cycle to stdout, one object per account,
while every log line goes to stderr:

```bash
mail-muncher run --json 2>/dev/null | jq -r '.stored[].path'
```

Full contract: [docs/manifest.md](docs/manifest.md).

Three properties make this safe to put in an autonomous loop:

- **Read-only by construction.** The only OAuth scope requested is
  `gmail.readonly`. Whatever consumes the output — and whatever bug it has —
  cannot send, delete, or modify mail.
- **Idempotent delivery.** A message's filename embeds a digest of
  `account + ":" + message id`, so its destination path is a pure function of
  its identity. A file that is already there means "an earlier cycle stored
  this", and the sink writes nothing. Re-run, replay after losing state, crash
  mid-cycle, or overlap two cron invocations: the tree converges, and nothing
  is processed twice.
- **Deterministic routing.** Rules are ordered and first-match-wins, so each
  message is written by exactly one rule. Give each consumer its own rule and
  its own `dest`, and each gets a private mailbox nothing else writes into.

Delivery is files on disk, and nothing here listens on a network. The contract
is the directory, with the manifest as an optional, machine-readable account of
what changed.

### Shape 2: tool call

`mail-muncher mcp` is a stdio MCP server over the mail already archived. The
agent asks; nothing is scheduled.

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

Five tools:

| Tool | What it answers |
| --- | --- |
| `list_rules` | What am I collecting, and which senders am I subscribed to *right now*? Each `from_domains_file` is re-read on every call. |
| `list_messages` | What has arrived? Filter by rule, account, thread or date; optionally grouped into conversations. |
| `search_messages` | Where is the message that mentions X? Substring search over subject, sender, recipients, labels, attachment names and body. |
| `read_message` | One message in full — metadata, body, attachment names and sizes — and optionally its whole thread in order. |
| `sync` | Fetch new mail once, returning the same manifest `run --json` writes. |

It is read-only over mail: no tool sends, deletes, or modifies anything, and
`sync` — the only tool that changes anything at all — can only add files.
Filesystem access is jailed to the configured rule `dest` roots, so the config,
the OAuth token, and the state directory are unreachable and unnamed.

Full reference, client wiring, and every argument and return field:
[docs/mcp.md](docs/mcp.md).

`list_rules` is the one that closes the loop. The agent writes a domain to its
own file, then asks `list_rules` and sees its own subscription reflected back —
the same list the next cycle will match against.

## Alternatives

Read this before adopting. Several tools do the fetch-filter-deliver shape
well, and some of them are a better fit than this one.

| Tool | Use it instead when |
| --- | --- |
| [getmail6](https://github.com/getmail6/getmail6) | You want a mature, widely packaged fetcher. It does IMAP and Gmail OAuth2, delivers to Maildir/MDAs, and filters through external programs. If a human (or mutt, or notmuch) is the consumer, this is the stronger tool. |
| [fdm](https://github.com/nicm/fdm) | You want per-rule Maildir destinations with a compact, well-tested config — exactly this tool's shape, minus the external filter source. Gmail access is app-password IMAP. |
| [lieer](https://github.com/gauteh/lieer) | You want your whole Gmail mailbox synced bidirectionally into a local Maildir for notmuch, not a filtered subset pulled out of it. |
| `gmail-archive` | It was almost exactly this — Gmail query to Maildir, incremental — and would be the obvious answer if it were still maintained. It has not been since 2018. |
| `gmail-exporter` | You want a one-off, label-based, spreadsheet-shaped export rather than incremental sync. |
| `mbsync` / `offlineimap` | You want full mailbox replication and will filter locally afterwards. |

What none of them do, and what this tool exists for: take filter input from a
file another program owns and re-read it every cycle, and emit a rendering
built for a program to consume rather than for a mail client to display. If you
do not need both of those, one of the tools above will serve you better and has
years more mileage.

## Install

Requires Go 1.25 or newer.

```bash
git clone https://github.com/craigmidwinter/mail-muncher
cd mail-muncher
make build          # -> ./mail-muncher
```

Or install straight into `$GOBIN`:

```bash
go install github.com/craigmidwinter/mail-muncher/cmd/mail-muncher@latest
```

`make build` stamps the version from `git describe`; `go install` and plain
`go build` leave it as `dev`.

### As a Claude Code skill

The repo ships a skill and plugin package under [`skills/`](skills/), which
installs mail-muncher as something an agent can set up and drive for you —
writing the config, running `auth`, and wiring the MCP server into your client.
If that is how you want to adopt it, start there instead of the quickstart
below.

## Quickstart

Ten minutes, end to end. Steps 2 and 3 are the only ones that need a browser.

**1. Build and write a config.**

```bash
make build
mkdir -p ~/.config/mail-muncher
cp examples/minimal.yml ~/.config/mail-muncher/config.yml
```

`~/.config/mail-muncher/config.yml` is the default path; `--config` overrides
it everywhere.

**2. Create a Google OAuth client.** mail-muncher ships no OAuth client of its
own — you create one, in your own Google Cloud project, and it stays yours.
Follow [docs/gmail-setup.md](docs/gmail-setup.md): create a project, enable the
Gmail API, configure the consent screen, create a **Desktop app** OAuth client,
and download its JSON to `~/.config/mail-muncher/credentials.json`.

**3. Authorize.**

```bash
./mail-muncher auth --account personal
```

This prints a consent URL (and tries to open a browser), listens on a loopback
port for the redirect, and writes the token to the account's `token_file` with
mode 0600.

**4. Check the config.**

```bash
./mail-muncher validate
```

```
config: /Users/you/.config/mail-muncher/config.yml
1 account(s), 1 rule(s), state_dir /Users/you/.local/state/mail-muncher
OK
```

`validate` parses the config, compiles every rule's match tree, and checks the
files it references. Missing files that another program owns — the OAuth
credentials, the token, a `from_domains_file` — are warnings, not errors:

```
warning: rules[0].match.any[0].from_domains_file: file does not exist yet: /Users/you/.local/share/jobsearch/domains.txt (it is maintained by another program; the rule matches nothing until it appears)
OK with 1 warning
```

**5. See what a real run would do.**

```bash
./mail-muncher run --dry-run
```

A dry run fetches and evaluates exactly as a real run does, and reports the
path each match *would* be written to. It writes no files and does not save
sync state, so you can run it as many times as you like.

**6. Run it.**

```bash
./mail-muncher run
```

Then run it again — everything already on disk reports as skipped, and the
incremental cursor means the second run barely talks to Gmail at all.

Once that works, put it on a schedule (see [Scheduling](#scheduling)) and read
[docs/configuration.md](docs/configuration.md) and
[docs/filters.md](docs/filters.md) to write real rules.

## Externally-managed filter files

This is the feature the tool is built around, so it is worth being precise
about the semantics.

`from_domains_file` names a file that mail-muncher does not own, does not
create, and never writes:

```yaml
match:
  from_domains_file: ~/.local/share/jobsearch/domains.txt
```

```
# ~/.local/share/jobsearch/domains.txt
# written by the job-search tracker

acme.com
globex.io          # inline comments are fine
@initech.com       # a leading @ is stripped
MAIL.Umbrella.COM  # case is irrelevant
```

- **Read once per cycle, on first use.** Not once per process, and not once per
  message. `run` re-reads it; every daemon tick re-reads it. A file referenced
  by several rules is read once and shared.
- **Missing or unreadable is never fatal.** The predicate simply matches
  nothing and one warning is logged for that file for that cycle. The owning
  program may not have created it yet, and mail-muncher must not fail because
  of that.
- **Liberal parsing.** One entry per line; `#` starts a comment; blank lines are
  skipped; surrounding whitespace is trimmed; a leading `@` and a trailing `.`
  are stripped; everything is lowercased; duplicates collapse. An entry with no
  dot in it is kept and logged as suspicious rather than dropped, because the
  file belongs to someone else and guessing wrong should not silently discard
  an entry.
- **Equality or subdomain.** `acme.com` matches `acme.com` and
  `careers.acme.com`, but not `notacme.com`.

The same matching rules apply to the inline `from_domains:` predicate; the only
difference is who owns the list.

## Configuration

Full reference: [docs/configuration.md](docs/configuration.md).

```yaml
state_dir: ~/.local/state/mail-muncher

on_message_failure: quarantine   # or: abort
on_degraded_filter: hold         # or: fail, proceed

accounts:
  - name: personal
    provider: gmail
    gmail:
      credentials_file: ~/.config/mail-muncher/credentials.json
      token_file: ~/.config/mail-muncher/token.json
      query: "-in:chats"
      initial_lookback: 2160h

rules:
  - name: job-search
    account: personal
    match:
      any:
        - from_domains_file: ~/.local/share/jobsearch/domains.txt
        - subject_regex: "(?i)your application"
    dest: ~/Mail/job-search
    formats: [eml, markdown]
```

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `state_dir` | path | `~/.local/state/mail-muncher` | Sync cursors (one JSON file per account), the cycle lock, the instance lock, and the quarantine directory. |
| `on_message_failure` | `quarantine`, `abort` | `quarantine` | What to do with a message that will not parse or that a sink failed on. See below. |
| `on_degraded_filter` | `hold`, `fail`, `proceed` | `hold` | What to do when a rule's `from_domains_file` cannot be read. See below. |
| `quarantine_dir` | path | `<state_dir>/quarantine` | Where quarantined messages are parked. |
| `accounts` | list | — | Mailboxes to pull from. At least one is required. |
| `accounts[].name` | string | — | Required, unique. Names the state file and is what `rules[].account` refers to. |
| `accounts[].provider` | `gmail` | `gmail` | The only recognized value today. |
| `accounts[].gmail` | mapping | — | Required when the provider is `gmail`. |
| `accounts[].gmail.credentials_file` | path | — | Required. The OAuth **client** JSON downloaded from Google Cloud. |
| `accounts[].gmail.token_file` | path | — | Required. Where `auth` caches the OAuth token, mode 0600. |
| `accounts[].gmail.query` | string | none | Optional Gmail search expression. A cost optimization for the **first-ever** scan only — see below. |
| `accounts[].gmail.initial_lookback` | Go duration | `720h` | How far back the first-ever scan reaches. Must be positive. See [Backfill](#backfill-the-first-run). |
| `rules` | list | — | Evaluated in order against every message; first match wins. |
| `rules[].name` | string | — | Required, unique. Appears in logs and in markdown frontmatter. |
| `rules[].account` | string | all accounts | Restricts the rule to one account. |
| `rules[].match` | match node | — | Required. See [Filters](#filters). |
| `rules[].dest` | path | — | Required. Destination directory; created on demand. |
| `rules[].formats` | list of `eml`, `markdown` | `[eml]` | Renderings to write. |

Notes that bite people:

- **Unknown keys are a hard error.** A typo fails the load rather than being
  ignored, so `validate` catches `initial_lookbak` before a run does.
- **`~` and `$VAR` are expanded** in every path-valued field, including
  `from_domains_file` values inside a match tree. `~user` forms are not
  supported. An undefined variable expands to the empty string, as in a shell.
- **`gmail.query` does not filter what gets kept, and applies to less than you
  think.** It is sent to Gmail on the **first-ever** scan of an account and
  nowhere else — not on incremental cycles, and not on a recovery scan after the
  history cursor expires. It is never re-applied locally. Your rules are the
  only authority on what is stored. Keep the query broad, or omit it.
- **Full scans include Spam and Trash.** Every `users.messages.list` call sets
  `includeSpamTrash`, so messages in Spam and Trash reach the filter engine and
  are archived if a rule claims them. The incremental history path always
  behaved this way; full scans now match it. Exclude them with a **rule**, not a
  query — see [Keeping Spam and Trash out](#keeping-spam-and-trash-out).

### Policies for the two things that can go wrong

Both keys sit at the top level, beside `state_dir`. The defaults are the safe
choices; you only change them if you have decided which failure you prefer.

**`on_message_failure`** — a message that will not parse, or where every
rendering its rule asked for failed to write.

| Value | Behavior |
| --- | --- |
| `quarantine` (default) | Write the raw bytes to `<quarantine_dir>/<account>/<id>.eml` with a `.json` sidecar naming the failure, then let the cursor advance past the message. Nothing is lost, and one poison message cannot wedge the pipeline. Counted as `quarantined` in the summary and manifest; the run still exits 0. |
| `abort` | Return the failure, so the cursor does **not** advance and the message is re-fetched next cycle. The trade-off is explicit: a permanently unparseable message wedges the account until a human deals with it. |

A quarantine write that itself fails falls back to `abort` semantics for that
message — refusing to advance is recoverable, losing the message is not.

**`on_degraded_filter`** — a rule's `from_domains_file` is missing, unreadable,
or truncated partway through. Such a file matches nothing, so without a policy
every message that cycle would be evaluated against an empty list, found not to
match, and consumed.

| Value | Behavior |
| --- | --- |
| `hold` (default) | Run the cycle and store everything that did match, log the degradation at error level, but do **not** save the advanced cursor — so the same mail is re-evaluated once the file returns. The manifest reports `degraded` and `state_held`. Exit 0. |
| `fail` | End the cycle before anything is fetched. Nothing stored, nothing advanced, non-zero exit. |
| `proceed` | Treat an unreadable list as an empty one and advance anyway. The old behavior, and the only option that accepts silent loss of wanted mail — `validate` warns about it. |

Files already stored under `hold` stay stored: the sinks are idempotent, so the
re-run skips them.

## Filters

Full reference and cookbook: [docs/filters.md](docs/filters.md).

A `match:` value is a mapping with **exactly one key** — a combinator or a
predicate. Two keys in one mapping is a compile error that tells you to combine
them with `all:` or `any:`. Regexes and durations are compiled when the config
loads, so a bad pattern is a `validate` failure, not a surprise at 3am.

### Combinators

| Key | Value | Matches when |
| --- | --- | --- |
| `all` | list of nodes | every child matches (at least one child required) |
| `any` | list of nodes | at least one child matches (at least one child required) |
| `not` | a single node | the child does not match |

```yaml
match:
  all:
    - any:
        - from_domains: [acme.com]
        - from_domains_file: ~/.local/share/agent/domains.txt
    - not:
        subject_regex: "(?i)^\\[newsletter\\]"
```

### Predicates

| Key | Value | Matches when |
| --- | --- | --- |
| `from_domains` | list of domains | any `From` address's domain equals or is a subdomain of a listed domain |
| `from_domains_file` | path | same, with the list read from an externally-owned file each cycle |
| `from_regex` | RE2 pattern | the pattern matches any `From` addr-spec (no display name) |
| `to_regex` | RE2 pattern | the pattern matches any `To` or `Cc` addr-spec |
| `subject_regex` | RE2 pattern | the pattern matches the decoded `Subject` |
| `header` | `{name: X-Foo, regex: ...}` | the pattern matches any value of that header |
| `has_attachment` | `true` / `false` | the message does (or does not) carry a real attachment |
| `label` | label name | the message carries that provider label, compared exactly |
| `older_than` | Go duration | the message `Date` is further in the past than the duration |
| `newer_than` | Go duration | the message `Date` is more recent than the duration |

One worked example each:

```yaml
# Mail from a company or any of its subdomains.
- from_domains: [acme.com, globex.io]

# The same list, owned and updated by another program.
- from_domains_file: ~/.local/share/jobsearch/domains.txt

# A specific sender, however they capitalize it.
- from_regex: "(?i)^no-?reply@acme\\.com$"

# Anything addressed to a plus-alias you hand out to vendors.
- to_regex: "(?i)^me\\+vendors@example\\.com$"

# Application acknowledgements, case-insensitively.
- subject_regex: "(?i)(your application|application received)"

# Everything a mailing list tags for you.
- header: {name: List-Id, regex: "golang-nuts"}

# Only messages that actually carry a file.
- has_attachment: true

# Gmail labels, exactly as shown in the UI. Nested labels use "Parent/Child";
# system labels are upper case (INBOX, SENT, UNREAD, STARRED).
- label: INBOX

# Message Date older than 90 days / newer than a day.
- older_than: 2160h
- newer_than: 24h
```

Details worth knowing:

- `from_regex` and `to_regex` test the bare address (`jane@acme.com`), never the
  display name. Use `header: {name: From, regex: ...}` to test the raw header
  including the display name.
- `has_attachment` counts parts marked `Content-Disposition: attachment`.
  Inline images referenced by `cid:` are not attachments.
- `label` is case-sensitive and exact — `label: inbox` does not match `INBOX`.
- `older_than` / `newer_than` compare against the message `Date` header, falling
  back to the provider's internal date when the header is missing or
  unparseable. A message with no usable date matches neither.
- Patterns are Go [RE2](https://github.com/google/re2/wiki/Syntax): no
  backreferences and no lookaround. Prefix with `(?i)` for case-insensitivity.
  In YAML, prefer double quotes and escape backslashes (`"\\."`), or use single
  quotes where no escaping is needed.
- Use `true` / `false` for `has_attachment`. YAML 1.2 treats `yes` and `no` as
  strings, and mail-muncher rejects them.

### Keeping Spam and Trash out

Fetches include Spam and Trash. That surprises people, so state it plainly:
**every message in your Spam and Trash folders is evaluated against your rules**
and will be archived if one claims it.

`gmail.query` cannot fix this. It is sent only on the first-ever scan, so
`-in:spam` there does nothing for any later cycle. Filter with a rule instead —
the filter engine is the only thing that sees every message:

```yaml
rules:
  - name: job-search
    match:
      all:
        - from_domains_file: ~/.local/share/jobsearch/domains.txt
        - not:
            any:
              - label: SPAM
              - label: TRASH
    dest: ~/Mail/job-search
```

Gmail's system labels are exact and upper case. If you want Spam and Trash out
of every rule, put the `not:` in each one — there is no global exclusion, by
design: rules are the single authority on what is stored.

## On-disk layout

Every sink files a message under the rule's `dest` by the message date, in UTC:

```
~/Mail/job-search/
└── 2026/
    └── 07/
        ├── 1785230100-a00d5c5e383a1c08-re-your-application-for-senior-engineer.eml
        ├── 1785230100-a00d5c5e383a1c08-re-your-application-for-senior-engineer.md
        └── 1785230100-a00d5c5e383a1c08-re-your-application-for-senior-engineer.attachments/
            ├── offer.pdf
            └── R-sum-2026.docx
```

The basename is shared by every format, so a message's renderings sort together:

```
<unix-seconds>-<sha256(account + ":" + message-id)[:16]>-<subject-slug>
```

The digest fragment is 16 hex characters — 64 bits. Two messages colliding on it
is not reachable at any volume a mailbox produces, and readers of the archive
parse it back out as a message id, so treat the width as part of the layout.

- The **timestamp** sorts a directory chronologically.
- The **digest** is the idempotency key. It depends only on the account name and
  the provider message id, so the path is a pure function of message identity.
- The **slug** is the subject lowercased, with every character outside `[a-z0-9]`
  collapsed to a single `-`, trimmed, and truncated to 40 characters.

Two caveats about the slug, both deliberate:

- **It is ASCII-only.** A subject written entirely in a non-Latin script, or
  entirely in emoji, slugs to `no-subject`. Non-ASCII filenames would be subject
  to filesystem Unicode normalization (HFS+ stores NFD), which can make the name
  written differ from the name the next cycle checks for — and that existence
  check is the entire idempotency story. The digest still keeps such messages
  apart.
- **It is cosmetic.** Only the digest carries identity. Two messages with the
  same subject never collide.

### How files are written

A message file is written to a temp file in its destination directory, fsynced,
and then **hard-linked** into place with `link(2)`. Three consequences worth
relying on:

- **A partial file is never published.** The temp file is complete before the
  name exists.
- **An existing file is never overwritten.** `link(2)` fails with `EEXIST`
  rather than clobbering, unlike `rename(2)`. That failure *is* the idempotency
  check — the kernel decides whether the name is free at the instant it is
  claimed, so there is no window in which another writer can slip a file in and
  have it silently replaced. "Already there" is reported as `skipped`.
- **Symlinks are refused, not followed.** A symlink at a message's final path,
  or standing in for the `<YYYY>` or `<MM>` directory below `dest`, is an error
  the message is counted and logged for. Nothing in the layout is legitimately a
  link, so one means something else is placing them there. The rule's own
  `dest:` is exempt — pointing that at another volume is ordinary.

On a filesystem without hard links (FAT, some network mounts) the fallback is an
`O_CREAT|O_EXCL` write in place: still atomically no-clobber and still
symlink-proof, at the cost of the no-partial-file guarantee.

Attachments are the one exception: they are written with a temp file and
`rename(2)`, because their names are not the idempotency marker — the `.md`
above them decides that.

**Directories are created 0700 and files 0600.** Archived mail is private
correspondence and decoded attachments, so it gets the same treatment as the
sync cursors and the OAuth token: nothing here is readable by other local users.
A tool you run as yourself is unaffected.

### The `.eml` file

`model.Message.Raw`, byte for byte, exactly as the provider delivered it.
Nothing is re-encoded, re-wrapped, or normalized, so it round-trips through any
mail tool and still verifies against DKIM signatures. This is the fidelity copy.

### The `.md` file

YAML frontmatter, then the body, then links to any attachments.

- **Body selection**: the `text/plain` part if there is one; otherwise the
  `text/html` part converted to markdown; otherwise the literal `*(no body)*`.
  Line endings are normalized to LF, trailing whitespace is stripped per line,
  and leading and trailing blank lines are trimmed.
- **Frontmatter** always carries `subject`, `from`, `to`, `date`, `message_id`,
  `thread_id`, `thread_id_source`, `account`, `rule`. `cc`, `in_reply_to`,
  `labels` and `attachments` are omitted when empty. It is produced with a YAML
  encoder, not string formatting, so a subject full of quotes and colons cannot
  break the parse.
- **Threading** is three fields, not four. `thread_id` is the join key and is
  **never empty**: the provider's own conversation id when there is one,
  otherwise one synthesized from the message's `References` chain. Group a
  directory by it without special cases. `thread_id_source` says how much to
  trust that grouping — `provider`, `references`, `in_reply_to` or `self` —
  because reconstruction is best-effort and a mailer that breaks the chain
  splits a thread. `in_reply_to` names the parent. The full `References` chain
  is deliberately left out: it is unbounded, and the `.eml` beside the file has
  it verbatim.
- **Attachments** are written to `<basename>.attachments/` next to the `.md`,
  with filenames sanitized (no directory components, no path traversal) and
  collisions de-duplicated as `name-2.pdf`, `name-3.pdf`. They are written
  before the `.md`, so the document never links to a file that is not there.
- **Inline `cid:` images are not resolved.** An HTML body that embeds images by
  content id renders as `![alt](cid:...)` — an unresolved link, not a path into
  the attachments directory. If you need the image bytes, they are in the
  `.eml`. This is a known limitation, not a bug.
- The `.md` is not a fidelity format. Anything that matters byte-exactly should
  be read from the `.eml`.

## Commands

```
mail-muncher [command]

  run         Run one fetch/filter/store cycle
  daemon      Run fetch/filter/store cycles repeatedly on an interval
  mcp         Serve the stored mail archive to agents over MCP (stdio)
  auth        Authenticate interactively against a mail provider
  validate    Parse the config, resolve referenced files, and report problems
  completion  Generate the autocompletion script for the specified shell
```

Persistent flags, available on every subcommand:

| Flag | Default | Description |
| --- | --- | --- |
| `--config` | `~/.config/mail-muncher/config.yml` | Path to the config file. |
| `--log-level` | `info` | `debug`, `info`, `warn`, or `error`. Logs are `log/slog` text on stderr. |
| `-v`, `--version` | — | Print the version. |

Per-command flags:

| Command | Flag | Default | Description |
| --- | --- | --- | --- |
| `run` | `--dry-run` | `false` | Fetch and evaluate, report what would be written, write nothing and save no state. |
| `run` | `--json` | `false` | Write a machine-readable manifest to stdout, one JSON object per account. |
| `daemon` | `--interval` | `5m` | Time between cycles, minimum 30s. Each sleep is jittered by up to ±10%. |
| `daemon` | `--dry-run` | `false` | As `run --dry-run`, on every tick. |
| `daemon` | `--json` | `false` | Write a manifest to stdout after every cycle: newline-delimited JSON, one object per account per tick. |
| `auth` | `--account` | — | Which account to authenticate. Required when the config has more than one. |

`mcp` takes no flags of its own; it is configured entirely by `--config`.

`--log-level debug` logs the rule decision for every message — the message id,
its subject, and the winning rule name or `no match` — which is the fastest way
to work out why a rule is not firing. Debug also logs each `already stored` skip
with its path. All of it goes to stderr, so it never disturbs `--json` on
stdout.

An interval below 30s is rejected before anything starts:

```
error: --interval 10s is below the 30s minimum
```

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success. A cycle that hit per-message errors (a message that would not parse, one sink write that failed) still exits 0 — those are counted in the summary, not escalated. |
| 1 | Config or validation error: the file would not parse, a rule would not compile, a required key is missing. Nothing was fetched. |
| 2 | Provider or authentication failure: no cached token, a refresh Google rejected, the API unreachable after retries. |
| 3 | Another instance holds the cycle lock. Exit code 3 is how an overlapping cron invocation reports "the previous one is still going". |

A quarantined message does **not** change the exit status: it is a message-level
failure that was handled, counted as `quarantined`, and reported in the
manifest. Alert on the counter, not on the exit code.

`daemon` never exits 2. A failing cycle is logged with a consecutive-failure
count and retried on the next tick — a daemon that quit on the first expired
token would need a human at exactly the wrong moment. Its statuses are 0
(stopped by SIGINT or SIGTERM, after letting the in-flight cycle finish and
saving state), 1 (config or validation error, including an `--interval` below
the minimum), and 3 (another instance already held the instance or cycle lock at
startup). A lock held by a *later* tick — a cron `run` overlapping a daemon tick
— is logged and skipped, not fatal.

### The run summary

Every cycle logs and prints one summary line per account:

```
personal: fetched=128 matched=6 stored=6 skipped=0 parse_errors=0 sink_errors=0 quarantined=0 duration=4.1s
```

| Field | Counts | Meaning |
| --- | --- | --- |
| `fetched` | messages | Messages the provider delivered this cycle. |
| `matched` | messages | Messages some rule claimed. |
| `stored` | renderings | Renderings actually written. |
| `skipped` | renderings | Renderings not written because the destination already existed. |
| `parse_errors` | messages | Messages that would not parse; logged and skipped. |
| `sink_errors` | renderings | Write failures; logged and counted, cycle continues. |
| `quarantined` | messages | Messages parked under the quarantine directory because they could not be delivered. |

The field list runs contiguously from `fetched=` to `duration=`, so anything
unusual about the cycle is marked on the account label instead:

```
personal (dry-run): fetched=42 ...
personal (degraded, state held): fetched=42 ...
personal (stopped): fetched=12 ...
```

A steady-state cron run looks like `fetched=0`. A re-run over the same window
looks like `matched=N stored=0 skipped=N` — that is idempotency working.

### The JSON manifest

`run --json` and `daemon --json` replace that line with a machine-readable
object per account — `stored[]`, `skipped[]`, `quarantined[]`, the counters, and
whether the cursor advanced. stderr keeps every log line, so
`run --json 2>/dev/null` is pure JSON and `daemon --json` is an NDJSON stream.

```bash
mail-muncher run --json 2>/dev/null | jq -r '.stored[] | "\(.rule)\t\(.path)"'
```

Full field-by-field contract: [docs/manifest.md](docs/manifest.md).

## State and locking

```
~/.local/state/mail-muncher/
├── personal.json                # one per account, mode 0600
├── mail-muncher.lock            # cycle lock, shared by run, daemon and mcp sync
├── instance/
│   └── mail-muncher.lock        # daemon lifetime lock — one daemon per state dir
└── quarantine/
    └── personal/
        ├── 18f2a1b2c3.eml       # raw bytes of an undeliverable message
        └── 18f2a1b2c3.json      # why it is here
```

Each account's state file is JSON:

```json
{
  "history_id": 918273,
  "last_sync_time": "2026-07-28T09:15:00Z",
  "seen_ids": ["18f2a...", "18f2b..."]
}
```

- `history_id` is the Gmail incremental cursor. When it is set, a cycle asks
  Gmail only what changed since. Gmail keeps roughly a week of history; when the
  cursor ages out the API answers 404, mail-muncher logs a warning, clears the
  cursor, and falls back to a full scan **in the same cycle**.
- `last_sync_time` bounds the `after:` term of a full scan. A recovery scan
  reaches 24 hours further back than the stored watermark, so mail that arrived
  while the previous cycle was listing is not stepped over. The overlap is
  harmless: everything already on disk is skipped at the write.
- `seen_ids` is a FIFO set of the last 2000 delivered message ids — belt and
  braces alongside the sinks' idempotent filenames.

The state directory is created 0700 and state files 0600: knowing which
accounts exist and which message ids were seen is not public information.

**Deleting an account's state file forces a full re-scan** bounded by
`initial_lookback`. That is safe — the sinks skip everything already on disk —
and it is the supported way to recover from a corrupted cursor.

### The two locks

- **The cycle lock**, `<state_dir>/mail-muncher.lock`, is `flock`-based and held
  for the duration of each cycle by `run`, by every daemon tick, and by the MCP
  `sync` tool. It is what stops a cron invocation racing a running daemon on the
  same cursors. Finding it held is "not now", not "broken": `run` exits 3, and a
  daemon tick logs and skips.
- **The instance lock**, `<state_dir>/instance/mail-muncher.lock`, is held by a
  daemon for its whole process lifetime. The cycle lock cannot do this job — it
  is released between ticks, so a second daemon starting while the first sleeps
  would sail past it and then poll forever alongside it, doubling the API
  traffic against the same cursors. A second daemon exits 3 immediately:

  ```
  another mail-muncher daemon is already running against this state directory; not starting
  ```

Both are released by the OS if the process crashes.

### Quarantine

Under the default `on_message_failure: quarantine`, a message that could not be
delivered is written to `<quarantine_dir>/<account>/<id>.eml` — the raw bytes,
verbatim — with a `.json` sidecar beside it naming the rule, the stage that
failed (`parse` or `sink`), the error, and the time. The sidecar is
self-contained on purpose: sweeping the directory must not require the run's
logs.

Nothing re-delivers automatically. Fix the cause and feed the `.eml` back in by
hand. The directory is 0700 and the files 0600, like the rest of the state
directory — a quarantined message is a whole email sitting outside its
destination tree.

## Scheduling

**cron** — one cycle every ten minutes. `run` exits 3 if the previous
invocation is still going, so overlaps are harmless:

```cron
*/10 * * * * /usr/local/bin/mail-muncher run --config /home/you/.config/mail-muncher/config.yml >> /home/you/.local/state/mail-muncher/cron.log 2>&1
```

Use an absolute path to the binary and to the config: cron's environment is not
your shell's, and `~` expansion in the config depends on `$HOME` being set.

A fuller sample with the environment cron does not give you is in
[`contrib/crontab.sample`](contrib/crontab.sample).

**launchd (macOS)** — run the daemon under `launchd` so it survives logout and
restarts on failure. A ready-to-edit plist lives in
[`contrib/launchd/`](contrib/launchd/); copy it into
`~/Library/LaunchAgents/`, edit the paths to the binary and config, then:

```bash
launchctl load ~/Library/LaunchAgents/<the-plist-you-copied>
```

**systemd** — no unit ships yet; `mail-muncher daemon --interval 5m` is a
straightforward `Type=simple` service, or pair `mail-muncher run` with a timer.

## Backfill: the first run

A wide first run is an **intended mode**, not an abuse. The default
`initial_lookback` is `720h` (30 days), which quietly means the thing you most
want on day one — the whole history of what you are collecting, on disk, once —
is the thing that does not happen by default.

Set it wide for the first run:

```yaml
accounts:
  - name: personal
    gmail:
      credentials_file: ~/.config/mail-muncher/credentials.json
      token_file: ~/.config/mail-muncher/token.json
      initial_lookback: 13140h   # ~18 months — first run only
```

Then run it once, and **drop the value back afterwards**:

```yaml
      initial_lookback: 720h
```

Dropping it back is safe because `initial_lookback` only ever bounds the
*first-ever* scan of an account. Every later cycle resumes from the stored
cursor and ignores the key entirely. It is re-armed only by deleting the
account's state file.

What to expect:

- It is **one large full scan**, rate-limited and retried, and it may take a
  while. Downloads run four at a time and pages are 500 messages; neither is
  configurable.
- **Look before you commit.** `mail-muncher run --dry-run` fetches and evaluates
  exactly as a real run does and reports every path it *would* write, without
  touching the destination tree. Add `--json` to count them:

  ```bash
  mail-muncher run --dry-run --json 2>/dev/null | jq '.summary'
  ```

- The run after it should report everything as `skipped`. That is the check that
  the backfill actually landed.

Go durations have no day or year unit — multiply hours. 18 months is `13140h`,
a year is `8760h`, 90 days is `2160h`.

The 2000-entry seen-set cap does **not** limit a backfill. See
[docs/architecture.md](docs/architecture.md#the-invariants) for why: the
deterministic filename plus skip-on-exists is the real idempotency key, and the
seen set is only belt and braces.

## Status and scope

Pre-1.0 and unreleased: there are no tagged versions yet, and a build without
`make` reports its version as `dev`. The config schema is stable enough to
write against, but treat it as subject to change until 1.0.

| Area | Status |
| --- | --- |
| Gmail provider (OAuth, full scan, incremental history sync, RAW download) | Built |
| Config loading and validation | Built |
| Filter engine (all combinators and predicates listed above) | Built |
| `.eml` and markdown sinks | Built |
| `run`, `daemon`, cycle lock, instance lock | Built |
| `--json` run manifests | Built |
| `mcp` — stdio MCP server, five tools | Built |
| Quarantine and the two failure policies | Built |
| IMAP provider | **Planned, not built.** No `provider: imap` block exists; the config rejects any provider other than `gmail`. |

### Deliberately out of scope

- **Writing to your mailbox.** The read-only scope is a design constraint, not
  a phase. No labeling, no deletion, no sending, no drafts.
- **Being a mail client.** No UI, and no index. `search_messages` is a
  case-insensitive substring scan over the stored files, not a search engine:
  no stemming, no ranking, no query language. Threads are grouped by an id
  carried on each message, not reassembled into a conversation model.
- **A network API.** Nothing listens on a socket. `mcp` speaks a stdio protocol
  to a client that launched it as a subprocess — no port, no HTTP endpoint, no
  network surface. The only thing that ever binds is the loopback port `auth`
  opens for a few seconds during the OAuth redirect.
- **Non-Gmail providers, today.** The provider seam exists and IMAP is the
  planned second implementation, but it is not written.

### Known limitations

- Inline `cid:` images are left as unresolved links in markdown output.
- Subject slugs are ASCII-only; non-Latin subjects file as `no-subject`.
- `gmail.query` applies only to the first-ever scan of an account — not to
  incremental cycles, and not to a recovery scan after the cursor expires.
- Full scans include Spam and Trash. Exclude them with a rule on the `SPAM` and
  `TRASH` labels.
- Gmail's download concurrency (4) and page size (500) are not configurable.
- No predicate sees the message body; matching is on headers, labels and dates.

## Documentation

Browsable at <https://craigmidwinter.github.io/mail-muncher/>, or in this repo:

- [docs/gmail-setup.md](docs/gmail-setup.md) — Google Cloud walkthrough and
  OAuth troubleshooting.
- [docs/configuration.md](docs/configuration.md) — every config key, its
  validation rule, and its failure mode.
- [docs/filters.md](docs/filters.md) — the complete match-tree language plus a
  cookbook of real rules.
- [docs/manifest.md](docs/manifest.md) — the `--json` manifest contract, field
  by field.
- [docs/mcp.md](docs/mcp.md) — the MCP server: client wiring and every tool's
  arguments and return shape.
- [docs/architecture.md](docs/architecture.md) — the pipeline, its seams, and
  where your change belongs.
- [CONTRIBUTING.md](CONTRIBUTING.md) — build, test, and the conventions the
  codebase holds itself to.

## License

MIT. See [LICENSE](LICENSE).
