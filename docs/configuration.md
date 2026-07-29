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

  - name: fastmail
    provider: imap
    imap:
      host: imap.fastmail.com
      port: 993
      username: you@fastmail.com
      password_cmd: pass show mail/fastmail
      mailboxes: [INBOX, Archive]
      tls: true
      initial_lookback: 720h

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

Three runnable configs ship in the repo: [`examples/minimal.yml`](../examples/minimal.yml),
[`examples/imap.yml`](../examples/imap.yml), and
[`examples/job-search.yml`](../examples/job-search.yml).

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
- **Required.** There is no default.
- **Valid values:** `gmail`, `imap`

Lowercased and trimmed on load, so `Gmail` and ` gmail ` both work. Anything
else is an error:

```
error: accounts[0].provider: unknown provider "pigeon" (known providers: gmail, imap)
```

Omitting it is an error too, and the message says what each option costs:

```
error: accounts[0].provider: required: want "imap" (app password, ~2 min) or "gmail" (Google Cloud Console, ~10 min, plus a token to re-issue every 7 days)
```

The key has no default on purpose. It used to default to `gmail`, which meant a
hand-written config that said nothing was enrolled in ten minutes of Google
Cloud Console — and, on a consent screen still in Testing mode, a refresh token
Google expires every 7 days — without its author ever having chosen that. The
two paths cost different enough things that guessing on your behalf is worse
than asking, so write the key out in every account.

The two differ in what they cost you to set up, not in what they can archive.
Both hand the pipeline complete RFC822 bytes, so rules, formats, filenames and
the archive layout are identical either way.

| | `gmail` | `imap` |
| --- | --- | --- |
| Credential | your own OAuth client, registered in the Google Cloud Console | an app password from your mail provider's settings page |
| Setup | [docs/gmail-setup.md](gmail-setup.md), then `mail-muncher auth` | paste two lines into the config |
| Where the secret lives | a token file mail-muncher writes | wherever your password manager keeps it; `password_cmd` fetches it |
| Incremental sync | `users.history` cursor | per-mailbox UID cursor |
| `label:` predicate matches | Gmail label names | mailbox (folder) names |
| Works with | Gmail only | Fastmail, iCloud, a work server, self-hosted, and Gmail with IMAP enabled |

mail-muncher ships no OAuth client and cannot ship one — see
[gmail-setup.md](gmail-setup.md) — so `imap` is the shorter road in.

### `accounts[].gmail`

- **Type:** mapping
- **Required** when the provider is `gmail`, and only then.

```
error: accounts[0].gmail: required for provider "gmail"
error: accounts[0].gmail: must not be set on a "imap" account (remove it, or set provider: gmail)
```

### `accounts[].imap`

- **Type:** mapping
- **Required** when the provider is `imap`, and only then.

```
error: accounts[0].imap: required for provider "imap"
error: accounts[0].imap: must not be set on a "gmail" account (remove it, or set provider: imap)
```

A block belonging to the other provider is an error rather than an ignored key.
Silently dropping a `gmail:` block off an account that fetches over IMAP would
leave you believing `gmail.query` is filtering something.

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

## `accounts[].imap`

Every key in this block belongs to `provider: imap`. A runnable file is
[`examples/imap.yml`](../examples/imap.yml).

```yaml
accounts:
  - name: personal
    provider: imap
    imap:
      host: imap.fastmail.com
      port: 993
      username: you@fastmail.com
      password_cmd: pass show mail/fastmail
      mailboxes: [INBOX]
      tls: true
      initial_lookback: 720h
```

### `host`

- **Type:** string
- **Required.**

The IMAP server hostname: `imap.fastmail.com`, `imap.mail.me.com`,
`imap.gmail.com`, whatever your provider documents.

```
error: accounts[0].imap.host: must not be empty
```

### `port`

- **Type:** integer
- **Default:** `993`

993 is the implicit-TLS (IMAPS) port, which pairs with the `tls: true`
default. Use 143 only with `tls: false`, and read that key's warning first.

```
error: accounts[0].imap.port: must be between 1 and 65535, got 70000
```

### `username`

- **Type:** string
- **Required.**

Usually the full email address. Whatever your provider's IMAP settings page
calls the username — some providers want a bare local part.

### `password_cmd`

- **Type:** shell command
- **Required.**

**There is deliberately no `password` key.** mail-muncher runs this command and
reads the secret from its stdout, so the credential lives wherever your password
manager already keeps it: not in a config file you might commit, not in a file
mail-muncher copies, and not in a backup that outlives your memory of it.

```yaml
password_cmd: pass show mail/fastmail
password_cmd: security find-generic-password -s fastmail -w
password_cmd: op read op://Private/Fastmail/app-password
password_cmd: cat ~/.config/mail-muncher/fastmail.secret   # if you insist
```

The command runs under `/bin/sh -c`, once per fetch, so pipes and quoting work
as written. Trailing newlines are stripped — every password manager prints one
and none of them mean it. Nothing else is trimmed, since a real secret may
start or end with a space.

Use an **app password**, not your account password. Every provider that offers
IMAP offers one, it is scoped to this use, and you can revoke it without
changing anything else. Where Gmail is concerned, an app password requires
2-Step Verification and IMAP enabled in Gmail's settings.

Failures are reported with the command as written, so a typo is obvious:

```
error: imap: password_cmd "pass show mail/fastmail" failed: exit status 1: Error: mail/fastmail is not in the password store.
error: imap: password_cmd "pass show mail/fastmail" produced no output on stdout; it must print the password there
```

A command that hangs — a `gpg` waiting for a pinentry nobody will answer —
is given two minutes and then abandoned. The command is run with no stdin, so
anything that tries to prompt on the terminal fails instead of blocking a
daemon forever.

### `mailboxes`

- **Type:** list of strings
- **Default:** `[INBOX]`

The folders to fetch, each tracked with its own independent cursor. Order is
the order they are fetched in.

**A mailbox name is also the `label` predicate value** on every message it
delivers, so a rule reads the same as it would on Gmail:

```yaml
mailboxes: [INBOX, Archive, "Lists/golang"]
```

```yaml
match:
  label: Archive
```

Names are the server's, verbatim, including its hierarchy separator — usually
`/` or `.`. `mail-muncher` does not guess: a folder the server does not have is
an error, not an empty folder, so a typo shows up on the first run instead of
looking like a quiet mailbox.

```
error: accounts[0].imap.mailboxes[1]: must not be empty
warning: accounts[0].imap.mailboxes[2]: duplicate mailbox "INBOX" (already listed at accounts[0].imap.mailboxes[0])
```

### `tls`

- **Type:** boolean
- **Default:** `true`

Implicit TLS on connect (IMAPS). Leave it alone.

Setting it to `false` sends the password from `password_cmd`, and every message
body, in the clear. There are two legitimate reasons — a server on loopback, or
one already behind an stunnel — and `validate` says so either way:

```
warning: accounts[0].imap.tls: false sends the password from password_cmd, and every message, unencrypted; only do this over a loopback or an already-encrypted tunnel
```

The certificate is verified against the system roots. There is no
"skip verification" key and there will not be one.

### `initial_lookback`

- **Type:** Go duration string (`720h`, `2160h`)
- **Default:** `720h` (30 days)
- **Must be positive.**

Bounds how far back a **first-ever** sync of a mailbox reaches, so a first run
does not trawl a decade of folder. It becomes a `SINCE` term in a UID search.

It applies twice: on the first cycle for a mailbox, and again on any cycle where
the server's UIDVALIDITY has changed — which is the protocol announcing that
every UID mail-muncher remembers now names a different message. Steady-state
cycles ignore it and resume from the stored UID.

```
error: accounts[0].imap.initial_lookback: invalid duration "30d" (want a Go duration such as "720h")
```

### How IMAP sync works

Worth knowing before you go looking in a state file.

Each mailbox gets two keys in the account's state, written as a pair:

```json
"extra": {
  "imap.INBOX.uidvalidity": "1650000000",
  "imap.INBOX.last_uid": "48213"
}
```

A cycle whose stored UIDVALIDITY still matches the server's asks for
`UID FETCH 48214:*` — only what arrived since. A cycle that finds it changed
throws the UID away and resyncs from `initial_lookback`. That resync
re-archives the window it covers under new filenames, because a message's
identity is `<account>:<mailbox>:<uidvalidity>:<uid>` and the UIDVALIDITY in
there is what stops the new UID 5 being mistaken for the old one. Duplicated
mail can be deleted; mail silently skipped cannot be recovered.

**Nothing is ever marked read.** Folders are opened with `EXAMINE`, not
`SELECT`, and bodies are fetched with `BODY.PEEK[]`, not `BODY[]`. There is no
code path that issues `STORE`, `APPEND`, or `EXPUNGE`.

IMAP has no conversation id, so `thread_id` in the markdown frontmatter is
derived from the message's `References`/`In-Reply-To` chain rather than handed
over by the server. Threading behaves the same downstream; it is just computed
locally.

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
- every `from_domains_file` and `from_regex_file` value anywhere inside a match
  tree

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
| Missing/duplicate name, missing or unknown `provider`, unknown account, empty `dest`, missing `match`, bad format, uncompilable match tree, bad `initial_lookback` | error | The config cannot be used as written. |
| `credentials_file` missing on disk | warning | Written before the OAuth client is downloaded. |
| `token_file` missing on disk | warning | Created by `auth`, which runs after the config exists. |
| `from_domains_file` or `from_regex_file` missing on disk | warning | **Owned by another program**, which may not have created it yet. The predicate matches nothing until it appears. |
| A `from_regex_file` pattern that does not compile, or that would match every message | warning | Same reason. The bad line is skipped and named; the rest of the file stays in force. See [filters.md](filters.md#the-over-broad-hazard-and-why-this-predicate-is-guarded). |
| No rules configured | warning | Valid, just useless. |
| Duplicate format in one rule | warning | Harmless; collapsed at write time. |
| `include_spam_trash: true` | warning | Legal and sometimes what you want, but it feeds attacker-authored mail to an AI agent. Never turn it on by accident. |

Those two file rows are the important ones. The whole point of
`from_domains_file` and `from_regex_file` is that mail-muncher does not own the
file, so neither its absence nor a bad line inside it can ever be a hard failure
— not at validation time, and not at run time either.

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

The domain list read by `from_domains_file`, and the pattern list read by
`from_regex_file`, are not configuration either — they are input, owned by
another program, re-read every cycle. See
[filters.md](filters.md#from_domains_file) and
[filters.md](filters.md#from_regex_file).
