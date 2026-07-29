---
title: Configuration reference
layout: default
nav_order: 3
description: >-
  Every key mail-muncher reads from its YAML config, what it defaults to, and
  how it fails — accounts, rules, sinks, failure policies and path expansion.
---

# Configuration reference

Every key mail-muncher reads, what it defaults to, and how it fails. For the
match-tree language used by `rules[].match`, see [filters.md](filters.md).

- [File location](#file-location)
- [Complete example](#complete-example)
- [Top level](#top-level)
- [Failure policies](#failure-policies)
- [`accounts`](#accounts)
- [`accounts[].gmail`](#accountsgmail)
- [`rules`](#rules)
- [Path expansion](#path-expansion)
- [Validation](#validation)
- [What lives outside the config](#what-lives-outside-the-config)

## File location

Default: `~/.config/mail-muncher/config.yml`. Override with `--config` on any
command:

```bash
mail-muncher validate --config /etc/mail-muncher/config.yml
```

The file must be a single YAML document. A second `---` document is an error
rather than being silently ignored, and an empty file is an error too:

```
error: config.yml: config must contain exactly one YAML document
error: config.yml: config file is empty
```

**Unknown keys are a hard error.** A misspelled key fails the load with the
line number, instead of being ignored until you notice the behavior is wrong:

```
error: config.yml: yaml: unmarshal errors:
  line 6: field initial_lookbak not found in type config.GmailConfig
```

## Complete example

Every key mail-muncher understands, in one file:

```yaml
state_dir: ~/.local/state/mail-muncher
quarantine_dir: ~/.local/state/mail-muncher/quarantine

on_message_failure: quarantine
on_degraded_filter: hold

accounts:
  - name: personal
    provider: gmail
    gmail:
      credentials_file: ~/.config/mail-muncher/credentials.json
      token_file: ~/.config/mail-muncher/token.json
      query: "-in:chats"
      initial_lookback: 2160h
      include_spam_trash: false

rules:
  - name: job-search
    account: personal
    match:
      any:
        - from_domains_file: ~/.local/share/jobsearch/domains.txt
        - subject_regex: "(?i)your application"
    dest: ~/Mail/job-search
    formats: [eml, markdown]

  - name: receipts
    match:
      all:
        - from_domains: [stripe.com, squareup.com]
        - has_attachment: true
    dest: ~/Mail/receipts
    formats: [eml]
```

Two runnable configs ship in the repo: [`examples/minimal.yml`](../examples/minimal.yml)
and [`examples/job-search.yml`](../examples/job-search.yml).

## Top level

### `state_dir`

- **Type:** path
- **Default:** `~/.local/state/mail-muncher`
- **Error if:** present but empty or whitespace.

Where per-account sync cursors, the locks, and quarantined mail live:

```
<state_dir>/
├── <account-name>.json          # one per account, mode 0600
├── mail-muncher.lock            # cycle lock: run, each daemon tick, mcp sync
├── instance/
│   └── mail-muncher.lock        # daemon lifetime lock — one daemon per state dir
└── quarantine/                  # default quarantine_dir
    └── <account-name>/
        ├── <id>.eml
        └── <id>.json
```

The directory is created 0700 on first use, with files 0600. It is *almost*
always safe to delete: cursors and a bounded list of recently seen message ids
cost one full re-scan bounded by `initial_lookback`, and the sinks skip
everything already on disk, so a re-scan re-downloads but does not duplicate.
The exception is `quarantine/`, which holds the only copy of any message that
could not be delivered. Empty that deliberately, not as part of a reset.

Account names become filenames, so a name containing `/`, `..`, or a NUL byte
is rejected at runtime:

```
state: invalid account name: "a/b" contains a path separator
```

### `accounts`

- **Type:** list
- **Required:** at least one.

```
error: accounts: at least one account is required
```

### `rules`

- **Type:** list
- **Default:** empty, which is a warning:

```
warning: rules: no rules configured; no messages will be archived
```

A config with no rules is valid and does nothing useful — it fetches, evaluates
nothing, and stores nothing.

## `accounts`

### `accounts[].name`

- **Type:** string
- **Required.** Must be unique across accounts.

Used as the state file name, as the value `rules[].account` refers to, in log
lines, in markdown frontmatter, and as half of the input to the idempotency
digest. **Renaming an account changes every future filename**, so previously
archived messages will not be recognized as already stored and will be written
again under new names. Choose a name and keep it.

```
error: accounts[0].name: must not be empty
error: accounts[1].name: duplicate account name "personal" (already defined at accounts[0])
```

### `accounts[].provider`

- **Type:** string
- **Default:** `gmail`
- **Valid values:** `gmail`

Lowercased and trimmed on load, so `Gmail` and ` gmail ` both work. Anything
else is an error:

```
error: accounts[0].provider: unknown provider "imap" (known providers: gmail)
```

IMAP is planned but not implemented; there is no `imap:` block to configure.

### `accounts[].gmail`

- **Type:** mapping
- **Required** when the provider is `gmail`, which is to say always.

```
error: accounts[0].gmail: required for provider "gmail"
```

## `accounts[].gmail`

### `credentials_file`

- **Type:** path
- **Required.**
- **Missing file:** warning, not an error.

The OAuth **client** JSON downloaded from the Google Cloud Console — the file
whose top-level key is `"installed"`. See [gmail-setup.md](gmail-setup.md).

```
warning: accounts[0].gmail.credentials_file: file does not exist: /Users/you/.config/mail-muncher/credentials.json
```

It is a warning because a config is legitimately written before the client is
downloaded. At fetch time it becomes a hard error:

```
gmail: OAuth credentials file not found: /Users/you/.config/mail-muncher/credentials.json
```

### `token_file`

- **Type:** path
- **Required.**
- **Missing file:** warning, not an error.

Where `mail-muncher auth` caches the OAuth token. Written with mode 0600, in a
directory created 0700, by an atomic temp-file-and-rename. Rewritten whenever a
refresh produces a new token — Google may rotate the refresh token during a
refresh, and dropping the new one would eventually leave the account unable to
refresh at all.

```
warning: accounts[0].gmail.token_file: file does not exist yet: /Users/you/.config/mail-muncher/token.json (run `mail-muncher auth`)
```

This file grants read access to the mailbox. Treat it like a password: never
commit it, never sync it to a shared drive, keep it 0600.

### `query`

- **Type:** string (a Gmail search expression)
- **Default:** none.

A **server-side cost optimization for full scans only.** Understand precisely
what it does and does not do before using it:

- On a full scan (the first-ever cycle, or a fallback after the incremental
  cursor expired), it is ANDed with an `after:` bound and sent to Gmail as `q`.
  The configured query is parenthesized first, so an `OR` inside it cannot
  swallow the `after:` term.
- On an incremental cycle it does **nothing**. Gmail's history API does not
  filter by query, and mail-muncher deliberately does not re-apply the query
  locally.

The consequence: a message excluded by `query` may still be archived if it
arrives during an incremental cycle. **Your rules are the only authority on
what gets stored.** Keep the query broad — `-in:chats` is a reasonable ceiling —
or omit it entirely. A narrow query does not make your archive smaller; it
makes your first scan cheaper and your behavior inconsistent between scan modes.

Do **not** reach for `-in:spam` here. It never worked as a spam filter: it is
absent from incremental cycles, and it is dropped from recovery scans too, so it
bounded exactly one cycle in the life of an account. Spam and Trash have their
own key, which every cycle honours — see
[`include_spam_trash`](#include_spam_trash) below.

### `include_spam_trash`

- **Type:** boolean
- **Default:** `false` — Spam and Trash are **not** fetched.

```yaml
accounts:
  - name: personal
    gmail:
      credentials_file: ~/.config/mail-muncher/credentials.json
      token_file: ~/.config/mail-muncher/token.json
      include_spam_trash: true   # only if you mean it
```

With the default, a message living in Spam or Trash is never fetched, never
evaluated against your rules, and never written. With `true`, those messages are
fetched like any other and your rules decide, exactly as they do for the rest of
the mailbox.

**Why the default is `false`.** mail-muncher exists to put mail in front of an
AI agent, so everything it delivers ends up inside an LLM's context window. Spam
is, by construction, the single highest-density source of hostile,
attacker-authored text in a mailbox — prompt injection arrives by email like
everything else. Keeping it out by default is a security decision, not a
tidiness one. It is also why the key is a boolean rather than something
cleverer: the safe setting should be the one you get by saying nothing.

**Both sync paths honour it identically**, which is the point of it being a
config key rather than a rule:

| | full scan (`users.messages.list`) | incremental (`users.history.list`) |
|---|---|---|
| `false` (default) | `includeSpamTrash=false`; Gmail never lists them | listed by Gmail regardless, then dropped after download once labels are known |
| `true` | `includeSpamTrash=true` | delivered |

The history API has no `includeSpamTrash` parameter, so the incremental path
pays one `messages.get` per excluded message to find out what it is, then throws
it away. That cost buys the invariant: a first scan, a steady-state cycle and a
recovery scan after an expired cursor all deliver the same set of messages. When
they disagree, mail is lost silently — see
[architecture.md](architecture.md#the-two-sync-paths-must-agree).

Excluded messages are not errors and not parse failures. They never reach the
pipeline, so they are not in the manifest and not in `fetched`; a cycle that
dropped some logs one line saying how many. That is deliberate: on a full scan
the exclusion happens inside Gmail and the number is not knowable at all, so a
counter would only ever be filled by one of the two paths.

**Turning it on.** The usual reason is recovering a message Gmail misfiled. If
you want Trash but not Spam, set the key and put the discrimination in a rule:

```yaml
accounts:
  - name: personal
    gmail:
      include_spam_trash: true
rules:
  - name: everything-but-spam
    match:
      not:
        label: SPAM
    dest: ~/Mail/archive
```

`validate` emits a warning when the key is `true`. The config is still valid —
the warning is there so nobody turns it on by accident. See
[filters.md](filters.md#the-spam-and-trash-labels) for how the key and the
labels divide the work.

### `initial_lookback`

- **Type:** Go duration string (`720h`, `90m`, `2160h`)
- **Default:** `720h` (30 days)
- **Must be positive.**

Bounds how far back the **first-ever** full scan reaches, so a first run does
not trawl a decade of mailbox. It becomes an `after:` term in the scan query.

It applies only when there is no stored `last_sync_time`. Every later full scan
is bounded by the previous successful sync instead. Deleting an account's state
file therefore re-arms it — a useful way to deliberately re-scan a bounded
window.

Go durations have no day or year unit. Multiply hours: 30 days is `720h`, 90
days is `2160h`, a year is `8760h`.

```
error: accounts[0].gmail.initial_lookback: invalid duration "30d" (want a Go duration such as "720h")
error: accounts[0].gmail.initial_lookback: must be positive, got "-1h"
```

## `rules`

Rules are evaluated in **config order** against every fetched message, and the
**first** one whose match tree accepts it claims it. A message is written by
exactly one rule, or by none. Put narrow rules above broad ones.

A message no rule claims is not stored, and the sync cursor still advances — it
is never re-evaluated.

### `rules[].name`

- **Type:** string
- **Required.** Must be unique across rules.

Appears in logs, in `--log-level debug` match decisions, and in the `rule:` key
of markdown frontmatter.

```
error: rules[0].name: must not be empty
error: rules[1].name: duplicate rule name "job-search" (already defined at rules[0])
```

### `rules[].account`

- **Type:** string
- **Default:** empty, meaning the rule applies to every account.

Restricts the rule to one account by name.

```
error: rules[0].account: unknown account "work"
```

### `rules[].match`

- **Type:** match node
- **Required.**

A mapping with exactly one key: a combinator (`all`, `any`, `not`) or a
predicate. The full language is documented in [filters.md](filters.md).

Omitting it is an error, on purpose:

```
error: rules[0].match: required (a rule with no match would archive every message)
```

`validate` compiles every match tree, so unknown keys, multiple keys in one
node, malformed regexes, and malformed durations are all caught before a run:

```
error: rules[0].match: a match node must have exactly one key, got 2 (from_domains, subject_regex); combine them with all: or any:
error: rules[0].match: any[0].subject_regex: invalid regular expression "([a": error parsing regexp: missing closing ]: `[a`
```

Errors name the location inside the tree (`any[0].subject_regex`), so a deeply
nested mistake is findable.

### `rules[].dest`

- **Type:** path
- **Required.**

The destination directory. Created on demand, mode 0755. Messages are filed as
`<dest>/<YYYY>/<MM>/<basename>.<ext>` using the message date in UTC — see the
README's on-disk layout section.

```
error: rules[0].dest: must not be empty
```

Two rules may share a `dest`. Because filenames are derived from message
identity rather than from the rule, they will not collide; the only visible
difference is the `rule:` key in markdown frontmatter.

### `rules[].formats`

- **Type:** list of `eml`, `markdown`
- **Default:** `[eml]`

Values are lowercased and trimmed on load. Duplicates are collapsed at write
time and warned about:

```
error: rules[0].formats[1]: unknown format "pdf" (want one of: eml, markdown)
warning: rules[0].formats[1]: duplicate format "eml"
```

| Format | Extension | What it is |
| --- | --- | --- |
| `eml` | `.eml` | The RFC822 source byte for byte, exactly as fetched. Nothing re-encoded or normalized; DKIM signatures still verify. The fidelity copy. |
| `markdown` | `.md` (+ `.attachments/`) | YAML frontmatter, the body as markdown, attachments extracted to a sibling directory. Built to be parsed and read by a program. Not a fidelity format. |

An empty list (`formats: []`) is treated as omitted and defaults to `[eml]`.

## Path expansion

Every path-valued field is expanded when the config loads:

- `state_dir`
- `accounts[].gmail.credentials_file`, `accounts[].gmail.token_file`
- `rules[].dest`
- every `from_domains_file` value anywhere inside a match tree

Expansion runs in two steps, in this order:

1. **Environment variables.** `$VAR` and `${VAR}` are substituted. An undefined
   variable expands to the empty string, matching shell behavior — so a typo in
   `$MAILDIR` yields a surprising relative path, not an error.
2. **Home directory.** A leading `~` or `~/` becomes `$HOME` (falling back to
   the OS's idea of the user's home). `~user` forms are **not** supported and
   are left in place literally.

Because `$HOME` is consulted first, `$HOME/Mail` and `~/Mail` are equivalent.
Under cron, where the environment is minimal, prefer absolute paths or make
sure `HOME` is set in the crontab.

Everything downstream — validation, logs, error messages — sees expanded paths,
which is why `validate` warnings print absolute paths. That makes `validate`
the fastest way to check what a path actually resolved to.

## Validation

```bash
mail-muncher validate
mail-muncher validate --config examples/job-search.yml
```

`validate` loads the config, applies defaults, expands paths, compiles every
match tree, and reports every finding in config order. Output:

```
config: examples/job-search.yml
1 account(s), 2 rule(s), state_dir /Users/you/.local/state/mail-muncher
warning: accounts[0].gmail.credentials_file: file does not exist: /Users/you/.config/mail-muncher/credentials.json
warning: rules[0].match.any[0].from_domains_file: file does not exist yet: /Users/you/.local/share/jobsearch/domains.txt (it is maintained by another program; the rule matches nothing until it appears)
OK with 2 warnings
```

Errors are printed before warnings. The last line is one of `OK`,
`OK with N warning(s)`, or `FAILED: N error(s), M warning(s)`.

**Exit code 0 unless there is at least one error-severity problem.** Warnings
alone exit 0 — which is deliberate, and worth internalizing:

| Problem | Severity | Why |
| --- | --- | --- |
| Malformed YAML, unknown key, multiple documents | error (load fails) | The config cannot be understood. |
| Missing/duplicate name, unknown account, unknown provider, empty `dest`, missing `match`, bad format, uncompilable match tree, bad `initial_lookback` | error | The config cannot be used as written. |
| `credentials_file` missing on disk | warning | Written before the OAuth client is downloaded. |
| `token_file` missing on disk | warning | Created by `auth`, which runs after the config exists. |
| `from_domains_file` missing on disk | warning | **Owned by another program**, which may not have created it yet. The predicate matches nothing until it appears. |
| No rules configured | warning | Valid, just useless. |
| Duplicate format in one rule | warning | Harmless; collapsed at write time. |
| `include_spam_trash: true` | warning | Legal and sometimes what you want, but it feeds attacker-authored mail to an AI agent. Never turn it on by accident. |

That last-but-two row is the important one. The whole point of
`from_domains_file` is that mail-muncher does not own it, so its absence can
never be a hard failure — not at validation time, and not at run time either.

The same validation runs at the start of `run` and `daemon`, so a config error
stops a cycle before it touches the network.

## What lives outside the config

Some things are deliberately not configurable:

| Thing | Value | Where |
| --- | --- | --- |
| OAuth scope | `gmail.readonly` | Fixed. The tool cannot write to a mailbox. |
| Gmail download concurrency | 4 | Fixed. |
| Gmail list/history page size | 500 (the API maximum) | Fixed. |
| Retry policy | 5 attempts, 500ms base, doubling, 30s cap, 50% jitter | Fixed. |
| Seen-id set size | 2000, FIFO | Fixed. |
| Subject slug length | 40 characters | Fixed. |
| Attachment filename length | 120 characters | Fixed. |
| File modes | mail 0644/0755, state and tokens 0600/0700 | Fixed. |
| Log destination | stderr, `log/slog` text handler | `--log-level` sets verbosity only. |

The domain list read by `from_domains_file` is not configuration either — it is
input, owned by another program, re-read every cycle. See
[filters.md](filters.md#from_domains_file).
