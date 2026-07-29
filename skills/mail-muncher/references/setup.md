# Setup: install, authorize, configure

Load this when the user is installing mail-muncher, creating Google OAuth
credentials, writing a config, or scheduling it.

## 1. Install

Requires Go 1.25 or newer.

```bash
go install github.com/craigmidwinter/mail-muncher/cmd/mail-muncher@latest
```

or from a clone, which stamps the version from `git describe`:

```bash
git clone https://github.com/craigmidwinter/mail-muncher
cd mail-muncher
make build        # -> ./mail-muncher
```

`go install` and a plain `go build` leave the version as `dev`.

## 2. Create a Google OAuth client

mail-muncher ships no OAuth client. The user creates one in their own Google
Cloud project and it stays theirs — their quota, their audit log, their
credentials on their disk. This needs a browser and about ten minutes; it
cannot be done for them from a terminal. Full walkthrough with every error
message: `docs/gmail-setup.md` in the repo, or
<https://craigmidwinter.github.io/mail-muncher/gmail-setup>.

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

**Warn them about the seven-day expiry.** A consent screen left in *Testing*
publishing status issues refresh tokens that expire after seven days, so every
unattended run starts failing a week later with `stored token was rejected`.
The fix is to set the publishing status to **In production**; verification is
not required for the owner's own account, they just click through the
"Google hasn't verified this app" interstitial via **Advanced**. The
alternative is re-running `auth` weekly, which is fine interactively and poor
for cron.

## 3. Authorize

```bash
mail-muncher auth --account personal
```

Prints a consent URL (and tries to open a browser), listens on a loopback port
for the redirect, and writes the token to the account's `token_file` with mode
0600. `--account` is required when the config has more than one account.

## 4. Config file

Default path `~/.config/mail-muncher/config.yml`; `--config` overrides it on
every subcommand. Full reference:
<https://craigmidwinter.github.io/mail-muncher/configuration>.

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
| `provider` | `gmail` | `gmail` is the only recognized value. |
| `gmail.credentials_file` | — | Required. The OAuth **client** JSON from Google Cloud. |
| `gmail.token_file` | — | Required. Where `auth` caches the token, mode 0600. |
| `gmail.query` | none | Optional Gmail search expression. A cost optimization for **full scans only** — incremental history results are not query-filtered and the query is deliberately not re-applied locally. Keep it broad or omit it. |
| `gmail.initial_lookback` | `720h` | Go duration bounding how far back the first-ever scan reaches. Must be positive. |

### `rules[]`

| Key | Default | Meaning |
| --- | --- | --- |
| `name` | — | Required, unique. Appears in logs, in markdown frontmatter, and in MCP tool output. |
| `account` | all accounts | Restrict the rule to one account. |
| `match` | — | Required. See `filters.md`. |
| `dest` | — | Required. Destination directory, created on demand. |
| `formats` | `[eml]` | Any of `eml`, `markdown`. |

### Things that bite

- **Unknown keys are a hard error.** A typo fails the load; `validate` catches
  `initial_lookbak` before a run does.
- **`~` and `$VAR` are expanded** in every path-valued field, including
  `from_domains_file` inside a match tree. `~user` forms are not supported. An
  undefined variable expands to the empty string, as in a shell.
- Always run `mail-muncher validate` after editing. It parses the config,
  compiles every regex and duration, and resolves referenced files. Missing
  files another program owns (credentials, token, a `from_domains_file`) are
  **warnings**, not errors.

## 5. Run

| Command | What it does |
| --- | --- |
| `mail-muncher run` | One fetch/filter/store cycle. The cron entrypoint. |
| `mail-muncher run --dry-run` | Fetches and evaluates exactly as a real run, reports the path each match *would* take, writes nothing and saves no state. Safe to repeat. |
| `mail-muncher run --json` | Newline-delimited JSON manifest to stdout, one object per account. |
| `mail-muncher daemon --interval 5m` | Poll forever. Minimum 30s; each sleep jittered ±10%. Also takes `--dry-run` and `--json`. |
| `mail-muncher auth --account NAME` | Interactive OAuth. |
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
| 1 | Config or validation error. Nothing was fetched. |
| 2 | Provider or authentication failure. |
| 3 | Another instance holds the cycle lock. This is how an overlapping cron invocation reports "the previous one is still going". |

`daemon` does not exit on a failing cycle: it logs, counts consecutive
failures, and waits for the next tick. It exits 0 on SIGINT/SIGTERM after
letting the in-flight cycle finish and saving state. Only one daemon may poll a
given `state_dir` — a second one exits 3 immediately.

## 6. Scheduling

**cron** — `run` exits 3 on overlap, so this is safe:

```cron
*/10 * * * * /usr/local/bin/mail-muncher run --config /home/you/.config/mail-muncher/config.yml >> /home/you/.local/state/mail-muncher/cron.log 2>&1
```

Use absolute paths for both binary and config: cron's environment is not the
user's shell, and `~` expansion in the config depends on `$HOME`.

**launchd (macOS)** — a ready-to-edit plist and crontab sample live in
`contrib/launchd/`. Copy the plist into `~/Library/LaunchAgents/`, edit the
paths, then `launchctl load`.

**systemd** — no unit ships yet. `mail-muncher daemon --interval 5m` is a
straightforward `Type=simple` service, or pair `mail-muncher run` with a timer.

## 7. State

```
~/.local/state/mail-muncher/
├── personal.json          # one per account, mode 0600
└── mail-muncher.lock      # cycle lock, shared by run and daemon
```

`history_id` is the Gmail incremental cursor; Gmail keeps roughly a week of
history, and when it ages out the API answers 404, mail-muncher clears the
cursor and falls back to a full scan **in the same cycle**. `last_sync_time`
bounds the `after:` term of a full scan. `seen_ids` is a FIFO set of the last
2000 delivered ids.

**Deleting an account's state file forces a full re-scan** bounded by
`initial_lookback`. That is safe — the sinks skip everything already on disk —
and it is the supported way to recover from a corrupted cursor.
