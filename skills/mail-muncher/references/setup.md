# Setup: install, configure, authorize, schedule

Load this when the user is installing mail-muncher, choosing a provider, writing
a config, creating Google OAuth credentials, or scheduling it.

## 1. Install

Requires Go 1.25 or newer.

```bash
go install github.com/craigjmidwinter/mail-muncher/cmd/mail-muncher@latest
```

or from a clone, which stamps the version from `git describe`:

```bash
git clone https://github.com/craigjmidwinter/mail-muncher
cd mail-muncher
make build        # -> ./mail-muncher
```

`go install` and a plain `go build` leave the version as `dev`.

## 2. Choose a provider

Both are supported. Neither is the good one; they cost different things.

| | `provider: imap` | `provider: gmail` |
| --- | --- | --- |
| Setup time | ~2 minutes | ~10 minutes in the Google Cloud Console |
| Credential | an app password from the provider's settings page | an OAuth client **you register**, plus a token |
| `auth` step | none — the command refuses IMAP accounts | required, needs a browser, cannot be automated for the user |
| Expiry | none | 7 days on a Testing-mode consent screen; re-run `auth` weekly |
| Works with | Gmail, Fastmail, iCloud, Proton Bridge, work accounts, self-hosted | Gmail only |
| Read-only enforced by | mail-muncher's own code (`EXAMINE`, `BODY.PEEK[]`, no `STORE`/`APPEND`/`EXPUNGE`) | **Google**, via the `gmail.readonly` scope |

**Default to IMAP.** Reach for Gmail when the user specifically wants the
read-only guarantee enforced by someone other than this program — an app
password is a full mail credential, and that difference is real.

## 3. `mail-muncher init`

```
mail-muncher init [--provider imap|gmail] [--account NAME] [--dest DIR] [--yes] [--force]
```

Writes a starter config to `--config` (default
`~/.config/mail-muncher/config.yml`), mode 0600, in a directory created 0700,
then prints the exact next commands for the provider chosen.

- Interactive by default; fully non-interactive with the flags above.
- `--yes` takes the default for every answer **except `--provider`**, which has
  no honest default — omitting both exits 1 asking for one. Defaults:
  `--account personal`, `--dest ~/Mail/mail-muncher`.
- An existing config is **never** overwritten. Without `--force` it exits 1
  naming the path and suggesting `--config <path>` instead.
- The body is loaded and validated by the real loader and the real validator
  *before* it is written, so a config `init` produced always passes `validate`.
- It carries one starter rule — `newer_than: 72h` into the destination as
  `[eml, markdown]` — so the first run is guaranteed to store something, which
  is what proves the install works. Narrow it once mail has landed.
- Choosing `gmail` prints the Cloud Console cost *before* asking anything else.

## 4a. The IMAP path (~2 minutes)

1. `mail-muncher init --provider imap --account personal --yes`
2. Create an **app password** in the mail provider's settings and store it where
   a command can retrieve it (`pass`, `security`, `op`, `secret-tool`, `gpg`).
3. Edit three keys in the generated config: `imap.host`, `imap.username`,
   `imap.password_cmd`. Everything else has a default.
4. Run the `password_cmd` in a shell and confirm it prints the password **and
   nothing else** — `... | cat -A`. A prompt, a warning, or a trailing blank
   line becomes part of the password and the login fails.
5. `mail-muncher validate` → `OK`, with no warnings.
6. `mail-muncher run --dry-run`, then `mail-muncher run`.

There is no `auth` step, and `mail-muncher auth` on an IMAP account fails:

```
error: gmail: account "personal" uses provider "imap", not "gmail"
```

Common hosts: Fastmail `imap.fastmail.com`, iCloud `imap.mail.me.com`, Gmail
`imap.gmail.com` (needs 2FA plus an app password), Proton `127.0.0.1` via the
Bridge (`port: 1143`, `tls: false`). Worked example: `examples/imap.yml`.

## 4b. The Gmail path (~10 minutes)

mail-muncher ships no OAuth client and never will: `gmail.readonly` is a Google
restricted scope. The user creates one in their own Google Cloud project and it
stays theirs — their quota, their audit log, their credentials on their disk.
This needs a browser and cannot be done for them from a terminal. Full
walkthrough with every error message: `docs/gmail-setup.md` in the repo, or
<https://craigjmidwinter.github.io/mail-muncher/gmail-setup>.

The shape of it:

1. Create a Google Cloud project.
2. Enable the **Gmail API** in that project.
3. Configure the **OAuth consent screen**: External user type, app name, their
   own address for support and developer contact. Add the Gmail address they
   want to archive under **Test users** — an app in Testing mode refuses to
   authorize any account not on that list. No scopes need to be added here;
   mail-muncher requests `gmail.readonly` at authorization time.
4. Create an OAuth client of type **Desktop app** and download its JSON to
   `~/.config/mail-muncher/credentials.json`.
5. `mail-muncher auth --account personal` — prints a consent URL (and tries to
   open a browser), listens on a loopback port for the redirect, and writes the
   token to the account's `token_file` with mode 0600. `--account` is required
   when the config has more than one account.

**Warn them about the seven-day expiry.** A consent screen left in *Testing*
publishing status issues refresh tokens that expire after seven days, so every
unattended run starts failing a week later with `stored token was rejected`. The
fix is to set the publishing status to **In production**; verification is not
required for the owner's own account, they just click through the "Google hasn't
verified this app" interstitial via **Advanced**. The alternative is re-running
`auth` weekly, which is fine interactively and poor for cron.

## 5. Config file

Default path `~/.config/mail-muncher/config.yml`; `--config` overrides it on
every subcommand. Full reference:
<https://craigjmidwinter.github.io/mail-muncher/configuration>.

### Top level

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `state_dir` | path | `~/.local/state/mail-muncher` | Sync cursors (one JSON per account, 0600) and the cycle lockfile. Directory is 0700. |
| `quarantine_dir` | path | `<state_dir>/quarantine` | Where undeliverable messages are parked. |
| `on_message_failure` | `quarantine` \| `abort` | `quarantine` | `quarantine` parks a message that will not parse or write and lets the cursor advance. `abort` refuses to advance, which re-fetches it forever until it is dealt with by hand. |
| `on_degraded_filter` | `hold` \| `fail` \| `proceed` | `hold` | What to do when a `from_domains_file` cannot be read. `hold` stores what did match but does not save the advanced cursor. `fail` ends the cycle before fetching. `proceed` treats the list as empty and advances — the only option that accepts silent loss of wanted mail. |
| `accounts` | list | — | At least one required. |
| `rules` | list | — | Evaluated in order; first match wins. |

### `accounts[]`

| Key | Default | Meaning |
| --- | --- | --- |
| `name` | — | Required, unique. Names the state file; `rules[].account` refers to it. |
| `provider` | `gmail` | `gmail` or `imap`. **Omitting it means `gmail`**, which is the slower path — always write it out. |
| `gmail` | — | Required for `provider: gmail`, **forbidden** on an `imap` account. |
| `imap` | — | Required for `provider: imap`, **forbidden** on a `gmail` account. |

The two blocks are mutually exclusive and the wrong one is a hard error, never
silently ignored:

```
error: accounts[0].gmail: must not be set on a "imap" account (remove it, or set provider: gmail)
error: accounts[0].imap: must not be set on a "gmail" account (remove it, or set provider: imap)
error: accounts[0].provider: unknown provider "exchange" (known providers: gmail, imap)
```

### `accounts[].imap`

| Key | Default | Meaning |
| --- | --- | --- |
| `host` | — | **Required.** IMAP hostname. |
| `port` | `993` | Implicit-TLS IMAPS port, matching the `tls` default. Must be 1–65535. |
| `username` | — | **Required.** Usually the full address; a few providers want only the local part. |
| `password_cmd` | — | **Required.** A shell command run under `/bin/sh -c` once per fetch whose stdout is the password. Trailing newlines are stripped; empty output is an error. **There is deliberately no `password` key** — the secret stays wherever the password manager keeps it. |
| `mailboxes` | `[INBOX]` | Folders to fetch, each with its own independent cursor, in the order listed. A folder the server does not have is an error, not an empty folder, so a typo surfaces on the first run. A mailbox name is also the `label` predicate value on every message it delivers. |
| `tls` | `true` | Implicit TLS on connect. `false` sends the password and every body in the clear; `validate` warns. Legitimate only on loopback (Proton Bridge) or behind an stunnel. |
| `initial_lookback` | `720h` | Go duration bounding how far back a first-ever sync of each mailbox reaches, and any resync a UIDVALIDITY change forces. Must be positive. Go durations have no day unit: 90 days is `2160h`. |

A well-formed IMAP account validates with **zero warnings** — nothing it points
at has to exist on disk.

### `accounts[].gmail`

| Key | Default | Meaning |
| --- | --- | --- |
| `credentials_file` | — | Required. The OAuth **client** JSON from Google Cloud. Missing is a warning. |
| `token_file` | — | Required. Where `auth` caches the token, mode 0600. Missing is a warning. |
| `query` | none | Optional Gmail search expression. A cost optimization for the **first-ever scan only** — incremental history results are not query-filtered, a recovery scan drops it, and the query is deliberately not re-applied locally. Keep it broad or omit it. Do not put `-in:spam` here; see the next row. |
| `initial_lookback` | `720h` | Go duration bounding how far back the first-ever scan reaches. Must be positive. |
| `include_spam_trash` | `false` | Whether mail in Spam or Trash is fetched at all. Off by default because delivered mail is read by an AI agent and Spam is where hostile, attacker-authored text lives. Both sync paths honour it identically. `validate` warns when it is `true`. IMAP has no counterpart: list only the folders you want in `mailboxes`. |

### `rules[]`

| Key | Default | Meaning |
| --- | --- | --- |
| `name` | — | Required, unique. Appears in logs, in markdown frontmatter, and in MCP tool output. |
| `account` | all accounts | Restrict the rule to one account. |
| `match` | — | Required. See `filters.md`. |
| `dest` | — | Required. Destination directory, created on demand. |
| `formats` | `[eml]` | Any of `eml`, `markdown`. |

Nothing under `rules:` is provider-specific. Swapping an account between `imap`
and `gmail` does not touch this half of the file.

### Things that bite

- **Unknown keys are a hard error.** A typo fails the load; `validate` catches
  `initial_lookbak` before a run does.
- **A missing `provider:` silently means `gmail`.** Write it out.
- **`~` and `$VAR` are expanded** in every path-valued field, including
  `from_domains_file` inside a match tree. `~user` forms are not supported. An
  undefined variable expands to the empty string, as in a shell.
- Always run `mail-muncher validate` after editing. It parses the config,
  compiles every regex and duration, and resolves referenced files. Missing
  files another program owns (credentials, token, a `from_domains_file`) are
  **warnings**, not errors.

## 6. Run

| Command | What it does |
| --- | --- |
| `mail-muncher init` | Write a starter config and print the next commands. See §3. |
| `mail-muncher run` | One fetch/filter/store cycle. The cron entrypoint. |
| `mail-muncher run --dry-run` | Fetches and evaluates exactly as a real run, reports the path each match *would* take, writes nothing and saves no state. Safe to repeat. |
| `mail-muncher run --json` | Newline-delimited JSON manifest to stdout, one object per account. |
| `mail-muncher daemon --interval 5m` | Poll forever. Minimum 30s; each sleep jittered ±10%. Also takes `--dry-run` and `--json`. |
| `mail-muncher auth --account NAME` | Interactive OAuth. **Gmail only** — it refuses an IMAP account. |
| `mail-muncher validate` | Parse, compile, resolve; report problems. |
| `mail-muncher mcp` | Stdio MCP server over the stored archive. |
| `mail-muncher completion SHELL` | Shell completion script. |

Global flags on every subcommand: `--config`, `--log-level`
(`debug`/`info`/`warn`/`error`, logs are `log/slog` text on **stderr**), and
`-v`/`--version`.

`--log-level debug` logs the rule decision for every message — the fastest way
to find out why a rule is not firing.

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success. Per-message errors are counted in the summary, not escalated. |
| 1 | Config or validation error, including "not configured yet". Nothing was fetched. |
| 2 | Provider or authentication failure. |
| 3 | Another instance holds the cycle lock. This is how an overlapping cron invocation reports "the previous one is still going". |

`daemon` does not exit on a failing cycle: it logs, counts consecutive failures,
and waits for the next tick. It exits 0 on SIGINT/SIGTERM after letting the
in-flight cycle finish and saving state. Only one daemon may poll a given
`state_dir` — a second one exits 3 immediately.

## 7. The unconfigured and half-configured states

These are designed to be acted on without fetching documentation. Each exits 1
(or, for the advisory ones, proceeds) and names the exact next command.

| State | What you see |
| --- | --- |
| No config file | ~15 lines: the missing path, `mail-muncher init`, and what each provider costs. Exit 1. Every command that needs config does this, including `auth`. |
| Config with no `accounts:` | Names the file and suggests `mail-muncher init --force --provider imap --account personal`. Exit 1. |
| Gmail account never authorized | Names the config, the token path, and `mail-muncher auth --account NAME`, plus the 7-day expiry. Exit 2 when a cycle hits it; printed as advice at startup once the credentials file exists. |
| Stored token rejected | Same command, and says the usual cause is the 7-day Testing-mode expiry. Exit 2. |
| Config with no `rules:` | Advice, not a failure: the run proceeds and stores nothing. Includes a copy-pasteable `newer_than: 72h` smoke-test rule. |

`mail-muncher run` immediately after install is therefore a legitimate install
check: it will either fetch mail or tell you precisely what is missing.

## 8. Scheduling

**cron** — `run` exits 3 on overlap, so this is safe:

```cron
*/10 * * * * /usr/local/bin/mail-muncher run --config /home/you/.config/mail-muncher/config.yml >> /home/you/.local/state/mail-muncher/cron.log 2>&1
```

Use absolute paths for both binary and config: cron's environment is not the
user's shell, and `~` expansion in the config depends on `$HOME`. A
`password_cmd` must also work in that environment — a `pass` or `gpg` command
needing an unlocked agent is the usual thing that works by hand and fails
under cron.

**launchd (macOS)** — a ready-to-edit plist and crontab sample live in
`contrib/launchd/`. Copy the plist into `~/Library/LaunchAgents/`, edit the
paths, then `launchctl load`.

**systemd** — no unit ships yet. `mail-muncher daemon --interval 5m` is a
straightforward `Type=simple` service, or pair `mail-muncher run` with a timer.

## 9. State

```
~/.local/state/mail-muncher/
├── personal.json          # one per account, mode 0600
└── mail-muncher.lock      # cycle lock, shared by run and daemon
```

**Gmail.** `history_id` is the incremental cursor; Gmail keeps roughly a week of
history, and when it ages out the API answers 404, mail-muncher clears the cursor
and falls back to a full scan **in the same cycle**. `last_sync_time` bounds the
`after:` term of a full scan. `seen_ids` is a FIFO set of the last 2000 delivered
ids.

**IMAP.** Each mailbox gets its own pair of keys inside the same file:

```json
"extra": {
  "imap.INBOX.uidvalidity": "1650000000",
  "imap.INBOX.last_uid": "48213"
}
```

A cycle whose stored UIDVALIDITY still matches asks for `UID FETCH 48214:*` —
only what arrived since. A cycle that finds it changed throws the UID cursor away
and resyncs from `initial_lookback`, re-archiving that window **under new
filenames**: a message's identity is `<account>:<mailbox>:<uidvalidity>:<uid>`,
and the UIDVALIDITY in there is what stops the new UID 5 being mistaken for the
old one. Duplicated mail can be deleted; mail silently skipped cannot be
recovered. The same message in two configured `mailboxes` is likewise two
identities.

**Deleting an account's state file forces a full re-scan** bounded by
`initial_lookback`. That is safe — the sinks skip everything already on disk —
and it is the supported way to recover from a corrupted cursor.
