<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/brand/lockup-dark.svg">
    <img src="docs/assets/brand/lockup.svg" alt="mail-muncher" width="408" height="64">
  </picture>
</p>

# mail-muncher

[![CI](https://github.com/craigjmidwinter/mail-muncher/actions/workflows/ci.yml/badge.svg)](https://github.com/craigjmidwinter/mail-muncher/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/craigjmidwinter/mail-muncher.svg)](https://pkg.go.dev/github.com/craigjmidwinter/mail-muncher)
[![Go version](https://img.shields.io/github/go-mod/go-version/craigjmidwinter/mail-muncher)](go.mod)
[![Release](https://img.shields.io/github/v/release/craigjmidwinter/mail-muncher?color=blue)](https://github.com/craigjmidwinter/mail-muncher/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

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

It reads from any IMAP mailbox — Gmail, Fastmail, iCloud, Proton Bridge, a work
account, your own server — or from Gmail's API with a read-only OAuth scope. It
runs one-shot for cron, or as a polling daemon, or as a stdio MCP server an
agent can query directly. Every mode emits the same machine-readable manifest of
what it did, and no mode ever writes to your mailbox.

## Two ways to connect a mailbox

Pick one before you install anything. Both are supported, and everything
downstream — rules, formats, filenames, the archive layout, the MCP tools — is
identical either way.

| | `provider: imap` | `provider: gmail` |
| --- | --- | --- |
| Setup time | **~2 min** | **~10 min** in the Google Cloud Console |
| What you register | nothing | your own Google Cloud project and Desktop-app OAuth client |
| Credential | an app password from your provider's own settings page | an OAuth token, scope `gmail.readonly` |
| How wide that credential is | **a full mail credential.** An app password can send and delete | read-only, and nothing else |
| Who enforces read-only | **mail-muncher's own code** | **Google** |
| Expiry | none | **every 7 days** on a Testing-mode consent screen; `mail-muncher auth` has to be re-run weekly |
| Where the secret lives | wherever your password manager already keeps it: `password_cmd` is run and its stdout is the password. There is deliberately no `password` key | `token.json`, mode 0600, written by `mail-muncher auth` |
| Which mailboxes | the folders you list in `mailboxes:`; `[INBOX]` by default | the whole Gmail account, minus Spam and Trash unless you ask for them |
| Works with | Gmail, Fastmail, iCloud, Proton Bridge, work accounts, self-hosted | Gmail only |
| Extra steps | none. There is no `auth` command on this path | `mail-muncher auth`, after [docs/gmail-setup.md](docs/gmail-setup.md) |

The ~2 min / ~10 min / 7 days above are the same numbers `mail-muncher init`
and the unconfigured-run guidance print, because they are the numbers that
decide this.

**The read-only guarantee is real on both paths, but it is not the same
guarantee, and flattening the two would be dishonest.**

- **Gmail: enforced by Google.** The only scope requested is `gmail.readonly`.
  The token that comes back is *incapable* of sending, deleting, labelling or
  modifying — not because mail-muncher declines to, but because Google will
  refuse the call. A bug in this program cannot reach your mailbox.
- **IMAP: enforced by mail-muncher.** IMAP has no read-only credential to ask
  for. An app password is a full mail credential; the protocol will happily let
  its holder delete a folder. What mail-muncher does instead is refuse to: every
  folder is opened with `EXAMINE` and never `SELECT`, every body is fetched with
  `BODY.PEEK[]` and never `BODY[]` (so mail is never marked read), and there is
  no code path anywhere in the provider that issues `STORE`, `APPEND` or
  `EXPUNGE`. Both belts are worn because a server is not obliged to protect a
  client from itself. That is a strong guarantee and an auditable one — it is
  just this program's guarantee, not your mail provider's.

If you have no specific reason to want the Gmail API, start with IMAP. It works
on a Gmail account too, and it is the path the quickstart takes.

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
from_address: jane@acme.com
from_addresses: [jane@acme.com]
to: [me@example.com]
to_addresses: [me@example.com]
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

- **Read-only by construction.** Nothing in mail-muncher writes to a mailbox.
  On Gmail that is Google's enforcement of the `gmail.readonly` scope; on IMAP
  it is `EXAMINE` and `BODY.PEEK[]` and no write path at all. Either way,
  whatever consumes the output — and whatever bug it has — cannot send, delete,
  or modify mail. See [the comparison above](#two-ways-to-connect-a-mailbox)
  for which of those two guarantees you are getting.
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
any stored credential, and the state directory are unreachable and unnamed.

**An unconfigured `mcp` server starts anyway, and that is deliberate.** If a
client launches `mail-muncher mcp` before there is a config, the server does
*not* exit — it completes the handshake, registers the same five tool names, and
answers every call with the setup guidance as a tool error, so the agent has
something to relay instead of "server failed to start". If you are wiring this
up for an operator, that is expected behaviour and not a bug to file. The
guidance also goes to stderr at startup, where clients tee the server log.

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

No Go toolchain required for the first two options.

### Homebrew

```bash
brew install craigjmidwinter/tap/mail-muncher
```

That taps [craigjmidwinter/homebrew-tap](https://github.com/craigjmidwinter/homebrew-tap)
and installs a prebuilt binary. `brew upgrade mail-muncher` tracks new
releases.

### Download a binary

Every [release](https://github.com/craigjmidwinter/mail-muncher/releases/latest)
ships archives for macOS and Linux on both amd64 and arm64, plus a
`checksums.txt` and a signature over it.

```bash
# Latest release, without the leading v. Set this by hand to pin a version.
VERSION=$(curl -fsSL https://api.github.com/repos/craigjmidwinter/mail-muncher/releases/latest \
  | sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p')

OS=$(uname -s | tr '[:upper:]' '[:lower:]')     # darwin | linux
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')

curl -fsSLO "https://github.com/craigjmidwinter/mail-muncher/releases/download/v${VERSION}/mail-muncher_${VERSION}_${OS}_${ARCH}.tar.gz"
tar xzf "mail-muncher_${VERSION}_${OS}_${ARCH}.tar.gz" mail-muncher
sudo install -m 0755 mail-muncher /usr/local/bin/mail-muncher
```

If the binary then refuses to run at all —

```
bash: mail-muncher: cannot execute binary file: Exec format error
```

— you have an archive for the wrong architecture. That message comes from the
kernel and says nothing about mail-muncher, so it is worth knowing the shape of
it. Compare `uname -m` against the `_amd64` / `_arm64` in the filename you
downloaded; the `ARCH=` line above computes the right one for you, so this only
bites if you set the name by hand.

**No root?** `/usr/local/bin` needs it; `~/.local/bin` does not. Drop the
`sudo` and install there instead — nothing about mail-muncher wants a
system-wide location:

```bash
install -d ~/.local/bin
install -m 0755 mail-muncher ~/.local/bin/mail-muncher
```

If `mail-muncher` is then "command not found", `~/.local/bin` is not on your
`PATH`; add it in your shell profile.

Skipping the `sudo` without changing the destination fails with a `Permission
denied` from `install` itself — on macOS naming a scratch file rather than
`mail-muncher`, which is confusing the first time you see it:

```
install: /usr/local/bin/INS@LPh1Hz: Permission denied     # macOS
install: cannot create regular file '/usr/local/bin/mail-muncher': Permission denied   # GNU
```

Either message means the same thing: pick the `~/.local/bin` route above, or
put the `sudo` back.

On macOS, a binary you downloaded yourself is quarantined by Gatekeeper. Clear
it with `xattr -d com.apple.quarantine /usr/local/bin/mail-muncher`, or use the
Homebrew install above, which does this for you.

#### Verify what you downloaded

This tool reads your mail. Check that the archive is the one the release
workflow built. First the checksum:

```bash
curl -fsSLO "https://github.com/craigjmidwinter/mail-muncher/releases/download/v${VERSION}/checksums.txt"

# Linux
sha256sum --check --ignore-missing checksums.txt
# macOS
shasum -a 256 --check --ignore-missing checksums.txt
```

Then the signature over `checksums.txt`. Releases are signed keylessly with
[cosign](https://docs.sigstore.dev/cosign/system_config/installation/) — there
is no public key to fetch and no private key anyone has to guard. The
signing certificate is issued to the release workflow's own GitHub OIDC
identity and recorded in the public Rekor transparency log, so what you are
checking is "this was built by `release.yml` in this repo, from a tag":

`cosign` is not installed by default on any platform and is not in the usual
distro repositories, so `cosign: command not found` here means "not installed
yet", not "verification failed". Get it first — `brew install cosign`, or
`go install github.com/sigstore/cosign/v2/cmd/cosign@latest`, or a release
binary from [the install
docs](https://docs.sigstore.dev/cosign/system_config/installation/).

```bash
curl -fsSLO "https://github.com/craigjmidwinter/mail-muncher/releases/download/v${VERSION}/checksums.txt.sig"
curl -fsSLO "https://github.com/craigjmidwinter/mail-muncher/releases/download/v${VERSION}/checksums.txt.pem"

cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity-regexp '^https://github\.com/craigjmidwinter/mail-muncher/\.github/workflows/release\.yml@refs/tags/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

`Verified OK` means the checksum file is authentic; the `sha256sum` step then
ties your archive to it. cosign 3 prints a deprecation notice for
`--certificate` and `--signature` — the check still runs, and these detached
files are what cosign 2 understands too.

### `go install`

The right path if you already have Go 1.25 or newer:

```bash
go install github.com/craigjmidwinter/mail-muncher/cmd/mail-muncher@latest
```

Note that `go install` builds report `dev` for `--version`, because the version
is stamped at link time and the `go` tool does not do it. Released binaries and
`make build` report the real tag. If you file a bug from a `go install` build,
say which commit you installed.

### Build from source

```bash
git clone https://github.com/craigjmidwinter/mail-muncher
cd mail-muncher
make build          # -> ./mail-muncher, version stamped from git describe
```

`make snapshot` builds the full set of release archives locally (requires
[goreleaser](https://goreleaser.com)) if you want to check what a release would
contain.

The example configs referenced below live in [`examples/`](examples/) —
[`imap.yml`](examples/imap.yml), [`minimal.yml`](examples/minimal.yml) and
[`job-search.yml`](examples/job-search.yml). They are also bundled inside every
release archive, so a binary download has them too. You do not need them to get
started, though: `mail-muncher init` writes a config from scratch.

### Container image

```bash
docker pull ghcr.io/craigjmidwinter/mail-muncher:latest
```

`linux/amd64` and `linux/arm64`, built from the same binaries the release
archives carry. The image's default command is `mcp`, because serving the
archive over stdio is the mode a container suits: a client starts it, talks to
it, and stops it. `run` and `daemon` work too — override the command — but on a
host those are a cron line and a launchd/systemd unit, which fit better.

Two mounts, and both matter:

```bash
# -e IMAP_PASSWORD forwards the variable, it does not invent it: export it
# first, from wherever you actually keep the secret.
export IMAP_PASSWORD="$(security find-generic-password -s mail-muncher -w)"

docker run -i --rm \
  -e IMAP_PASSWORD \
  -v ~/.config/mail-muncher:/home/muncher/.config/mail-muncher:ro \
  -v ~/.local/share/mail-muncher:/home/muncher/archive \
  ghcr.io/craigjmidwinter/mail-muncher:latest mcp
```

That `export` pairs with `password_cmd: printenv IMAP_PASSWORD` in the config —
see the note below on why your host password manager is not reachable from
inside the container.

**Every path inside `config.yml` has to be a path the container can see.** A
`dest:` of `~/Mail/receipts` resolves against the container's home directory,
not yours, so mail lands on a layer that disappears when the container exits.
Point `dest:` at the mounted directory — `/home/muncher/archive/receipts` for
the mount above — or you will archive into the void and the manifest will
cheerfully tell you it worked.

**`password_cmd` runs inside the container**, under `/bin/sh`, which means your
host password manager is not there. `pass show mail/fastmail` cannot work. Use
the secret material the container does have:

```yaml
password_cmd: printenv IMAP_PASSWORD          # -e IMAP_PASSWORD
password_cmd: cat /run/secrets/imap-password  # docker secret or a mounted file
```

This is the one place the container path is genuinely worse than a host
install: it moves the credential out of your password manager and into the
container's environment. If that trade is not worth it to you, install the
binary — `password_cmd` is designed for the host case, and this is the
compromise, not the intent.

The image is also what backs the [MCP Registry](https://registry.modelcontextprotocol.io)
listing; [`server.json`](server.json) is that entry, and its `name` has to match
the `io.modelcontextprotocol.server.name` label baked into the image.

Publishing that entry is automatic. Tagging a release builds and pushes the
image, and then a second job rewrites `version` and the image tag in
`server.json` from the git tag and publishes to the registry, authenticating
with the workflow's own OIDC identity rather than a stored token.

**So the `version` committed in `server.json` is last release's, and lags by
one tag on purpose.** The tag is the source of truth; the file is a template
that CI stamps. Bumping it by hand achieves nothing.

### As a Claude Code skill

The repo ships a skill and plugin package under [`skills/`](skills/), which
installs mail-muncher as something an agent can set up and drive for you —
writing the config, running `auth`, and wiring the MCP server into your client.
If that is how you want to adopt it, start there instead of the quickstart
below.

The skill leads with `provider: imap` and drives `mail-muncher init`, so it
takes the same two-minute route this README does rather than sending you to the
Google Cloud Console.

### Windows

There is no Windows build, and none of the options above quietly work around
that. Homebrew does not run on Windows. The release archives are `darwin` and
`linux` only, and the download snippet above is a POSIX shell script built on
`uname`, which PowerShell and `cmd` cannot run at all.

`go install` is the one path that produces something, and that is the problem
worth stating plainly. Go cross-compiles this module cleanly — no cgo, no
platform build tags outside a test file — so you get a `mail-muncher.exe` that
starts, and `mail-muncher init` that writes a config without complaint. It
stops at the first `run`. The IMAP provider, the ~2 min path this README leads
with, executes `imap.password_cmd` by handing it to `/bin/sh -c`
([`internal/provider/imap/password.go`](internal/provider/imap/password.go)),
and a stock Windows machine has no `/bin/sh`. `init` is careful enough not to
seed a Windows config with a macOS or Linux secret tool, but the command it
does seed still goes to a shell that is not there, so the failure arrives late
and blames the wrong thing.

The Gmail provider has no such dependency — its OAuth flow already picks
`rundll32` on Windows — so it may work end to end. It is untested there and
unsupported.

What does work on Windows: the [container image](#container-image) under
Docker Desktop, or either install path inside WSL2, where a Linux binary and
`/bin/sh` both exist.

### Upgrade

```bash
brew upgrade mail-muncher            # Homebrew
go install github.com/craigjmidwinter/mail-muncher/cmd/mail-muncher@latest
```

For a downloaded binary, repeat the download steps above — the `install` step
overwrites in place. Nothing else has to change: the config schema and the
on-disk layout are stable within 0.x, and sync cursors in `state_dir` are read
by any newer version. [CHANGELOG.md](CHANGELOG.md) records anything that would
make that untrue, and there is nothing there yet.

Check what you landed on with `mail-muncher --version`. A build without `make`
reports `dev` — that is the `go install` version-stamp gotcha, not a broken
install.

### Uninstall

Removing the binary leaves everything else behind, so this is in the order
that removes the most sensitive material first. Nothing here is done for you:

```bash
# 1. Stop it, if you scheduled it.
launchctl unload ~/Library/LaunchAgents/com.craigjmidwinter.mail-muncher.plist
rm ~/Library/LaunchAgents/com.craigjmidwinter.mail-muncher.plist
rm -f ~/Library/Logs/mail-muncher.out.log ~/Library/Logs/mail-muncher.err.log
# or, if you used cron: crontab -e and delete the line.

# 2. The credential. This is the part nothing else will clean up.
security delete-generic-password -s mail-muncher      # macOS Keychain, IMAP
# Gmail instead: revoke the app at https://myaccount.google.com/permissions

# 3. Config, credentials and the OAuth token.
rm -rf ~/.config/mail-muncher

# 4. Sync cursors, both lockfiles, and the quarantine directory.
rm -rf ~/.local/state/mail-muncher

# 5. The binary.
brew uninstall mail-muncher          # Homebrew
rm -f /usr/local/bin/mail-muncher    # downloaded binary
rm -f "$(go env GOPATH)/bin/mail-muncher"   # go install
```

**Your archived mail is deliberately not on that list.** It lives at whatever
`dest` your rules named — `~/Mail/mail-muncher` if you took the `init` default
— and those are ordinary files that outlive the tool, which is the whole point
of the format. `grep -n 'dest:' ~/.config/mail-muncher/config.yml` before step
3 if you want the paths, and delete them yourself if you want the mail gone.

The Homebrew cask carries no `zap` stanza, so `brew uninstall` removes the
binary and nothing under your home directory. That is on purpose — mail this
tool has already written is yours, and an uninstaller is a bad place to
discover otherwise.

## Quickstart

About five minutes, no browser, no clone. This is the IMAP path; for the Gmail
API instead, read [Quickstart: Gmail](#quickstart-gmail) below **before** you
start, because it costs about ten minutes in the Google Cloud Console and the
token expires weekly.

**0. Check the install.** Right after installing, before there is any config:

```bash
mail-muncher run
```

That is a genuinely useful smoke test rather than a mistake. It exits 1 and
tells you exactly where it looked, what to run next, and what each provider
costs:

```
mail-muncher is not configured.
  missing config file: /Users/you/.config/mail-muncher/config.yml
  next command:        mail-muncher init
  then:                mail-muncher validate && mail-muncher run --dry-run

init asks which provider to use. Both are supported; the costs differ.
  provider: imap   ~2 min. Gmail, Fastmail, Proton Bridge, work accounts,
    self-hosted. Needs an app password, which is a broader credential than a
    read-only OAuth token; mail-muncher only ever issues BODY.PEEK.
  provider: gmail  ~10 min in the Google Cloud Console: gmail.readonly is a
    Google restricted scope, so mail-muncher ships no OAuth client and you
    register your own. Google enforces read-only, but on a Testing-mode
    consent screen the refresh token expires every 7 days, so
    "mail-muncher auth" must be re-run weekly. Read docs/gmail-setup.md.
Docs: https://craigjmidwinter.github.io/mail-muncher/
```

Every command that needs a config says this, so a broken install and an
unconfigured one never look alike.

**1. Get an app password.** From your mail provider's own settings page —
Gmail, Fastmail, iCloud, Proton, your work account. It is scoped to this one
use and you can revoke it without touching anything else. Put it wherever you
already keep secrets:

```bash
# macOS Keychain
security add-generic-password -s mail-muncher -a "$USER" -w

# or pass, 1Password, secret-tool, gpg — anything that prints it on stdout
```

mail-muncher never stores this. It runs a command you name and reads the
password off that command's stdout, so the secret stays in your password
manager. There is deliberately no `password:` key in the config schema.

**2. Write a config.**

```bash
mail-muncher init --provider imap
```

```
Account name [personal]:
Write matched mail to [~/Mail/mail-muncher]:
IMAP host: imap.fastmail.com
IMAP username: you@fastmail.com
Password command [security find-generic-password -s mail-muncher -w]:
Wrote /Users/you/.config/mail-muncher/config.yml

Next, for provider imap:
  1. Run that password_cmd in a shell and check it prints the password and
     nothing else, for example:
       security find-generic-password -s mail-muncher -w | cat -A
     Anything else on stdout - a prompt, a warning, a trailing blank line -
     becomes part of the password and the login fails. If it is not there
     yet, create an app password with your mail provider first and store it
     where password_cmd can read it.
  2. mail-muncher validate
  3. mail-muncher run --dry-run     then     mail-muncher run
Matched mail lands in ~/Mail/mail-muncher
Docs: https://craigjmidwinter.github.io/mail-muncher/
```

**There is no editing step.** `init` asks for everything the IMAP path needs
and writes a config that validates on the first try. The password command is
offered with your platform's default already filled in — Keychain on macOS,
`secret-tool` on Linux, `pass` elsewhere — so pressing Enter through it is a
real answer, not a placeholder.

Every question has a flag, for answering up front or scripting the whole
thing:

```bash
mail-muncher init --provider imap --yes \
  --host imap.fastmail.com --username you@fastmail.com
```

`--yes` takes the default for everything that has an honest one, which is why
it still requires `--provider`, `--host` and `--username`. Those three have no
default worth guessing, and it says so rather than writing a placeholder:

```
error: --host and --username required with --yes --provider imap; host and
username have no honest default to take. Run `mail-muncher init --provider
imap` without --yes to be prompted instead
```

Add `--account NAME`, `--dest DIR` and `--password-cmd CMD` to answer the rest.
An existing config is never overwritten without `--force`.

`~/.config/mail-muncher/config.yml` is the default path; `--config` overrides
it everywhere, including for `init`.

**3. Check the password command prints the password, and nothing else.**

```bash
security find-generic-password -s mail-muncher -w | cat -A
```

`| cat -A` makes a stray prompt, warning, or trailing blank line visible.
Anything extra on stdout becomes part of the password and the login fails —
this is the single most common reason a first run cannot authenticate.

Here is what `init` wrote, for reference; it is commented throughout, and
[`examples/imap.yml`](examples/imap.yml) is a fuller worked version:

```yaml
accounts:
  - name: personal
    provider: imap
    imap:
      host: imap.fastmail.com
      username: you@fastmail.com
      password_cmd: security find-generic-password -s mail-muncher -w
      mailboxes: [INBOX]
```

**4. Check the config.**

```bash
mail-muncher validate
```

```
config: /Users/you/.config/mail-muncher/config.yml
1 account(s), 1 rule(s), state_dir /Users/you/.local/state/mail-muncher
OK
```

An IMAP account validates clean: no credential file to find, no token to have
written yet, nothing on disk at all. `OK` with no warnings is the expected
result. `validate` parses the config, compiles every rule's match tree, and
checks the files it references. Missing files that another program owns — a
`from_domains_file`, or on the Gmail path the OAuth credentials and token — are
warnings, not errors:

```
warning: rules[0].match.any[0].from_domains_file: file does not exist yet: /Users/you/.local/share/jobsearch/domains.txt (it is maintained by another program; the rule matches nothing until it appears)
OK with 1 warning
```

**5. See what a real run would do.**

```bash
mail-muncher run --dry-run
```

A dry run connects, fetches and evaluates exactly as a real run does, and
reports the path each match *would* be written to. It writes no files and does
not save sync state, so you can run it as many times as you like. This is also
where a wrong host, username or `password_cmd` surfaces, named exactly:

```
error: account "personal": imap: password_cmd "security find-generic-password -s mail-muncher -w" failed: exit status 44: security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain.
```

**6. Run it.**

```bash
mail-muncher run
```

The config `init` wrote carries one starter rule matching everything newer than
72h, so this first run is guaranteed to store something — a run that stores
nothing is indistinguishable from a broken install. Then run it again:
everything already on disk reports as `skipped`, and the incremental cursor
means the second run barely talks to the server at all.

Once that works, narrow the starter rule into what you actually want
([docs/filters.md](docs/filters.md)), then put it on a schedule (see
[Scheduling](#scheduling)). [docs/configuration.md](docs/configuration.md) has
every key.

### Quickstart: Gmail

Take this path if you specifically want the Gmail API and a read-only guarantee
enforced by Google rather than by this program. **Know the two costs before you
begin**, because both are structural and neither goes away:

- **About ten minutes in the Google Cloud Console, up front.** `gmail.readonly`
  is a Google *restricted* scope, so mail-muncher ships no OAuth client and
  never will — you register your own project and Desktop-app client and
  download its JSON.
- **The token expires every 7 days.** Google applies that to every consent
  screen still in Testing mode, which yours will be. `mail-muncher auth` has to
  be re-run weekly. There is no setting that removes it;
  [docs/gmail-setup.md](docs/gmail-setup.md#the-seven-day-refresh-token-expiry)
  explains why and what the alternatives cost.

If neither is worth it to you, the IMAP path above works on a Gmail account.

```bash
mail-muncher init --provider gmail        # prints the cost warning, then writes the config
# → follow docs/gmail-setup.md: project, Gmail API, consent screen,
#   Desktop app OAuth client, save its JSON as
#   ~/.config/mail-muncher/credentials.json
mail-muncher auth --account personal      # browser consent; writes token.json 0600
mail-muncher validate
mail-muncher run --dry-run
mail-muncher run
```

`auth` prints a consent URL (and tries to open a browser), listens on a
loopback port for the redirect, and writes the token to the account's
`token_file` with mode 0600. It is a Gmail-only command — on an IMAP account it
refuses, because there is nothing to authorize. Steps 4 through 6 of the IMAP
quickstart above then apply unchanged; `validate` will report two warnings until
the credentials and token files exist.

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

### When a domain list cannot express it

Some senders cannot be enumerated in advance. One company's mail might arrive
from `wagepoint.teamtailor.com`, `mail.wagepoint.com` and
`notifications@wagepoint-hr.example` — a domain list can only name hosts you
already know about. `from_regex_file` is the same idea for patterns:

```yaml
match:
  from_regex_file: ~/.local/share/jobsearch/companies.txt
```

```
# one RE2 pattern per line, unanchored
wagepoint
(?i)^careers@acme\.io$
teamtailor\.com$
```

Lifecycle is identical — read once per cycle, missing is never fatal,
`on_degraded_filter` governs the cursor. Two deliberate differences from the
domain format:

- **Nothing is lowercased**, because a regex is case-sensitive by construction.
  Write `(?i)` when you want otherwise.
- **`#` only starts a comment at the start of a line.** Truncating a pattern at
  a mid-line `#` would silently change what it matches.

**The failure modes are opposites, and that is why the guards differ.** A typo
in a domain list matches *nothing* — the cost is silence. A typo in a pattern
list can match *everything*: `.*` or a stray blank claims the entire mailbox.
So an empty pattern, or any pattern that matches the empty string, is refused
outright; a pattern that fails to compile is refused **by itself** while the
rest of the file stays in force; and the count of patterns loaded is logged
every cycle, so a file that fell from twelve patterns to one catch-all is a
number in your run output rather than a discovery by way of a full disk.

## Configuration

Full reference: [docs/configuration.md](docs/configuration.md). Runnable files:
[`examples/imap.yml`](examples/imap.yml),
[`examples/minimal.yml`](examples/minimal.yml),
[`examples/job-search.yml`](examples/job-search.yml).

The account block is the only part that differs by provider. IMAP:

```yaml
accounts:
  - name: personal
    provider: imap
    imap:
      host: imap.fastmail.com
      port: 993                  # default
      tls: true                  # default
      username: you@fastmail.com
      password_cmd: pass show mail/fastmail   # stdout is the password
      mailboxes: [INBOX, Archive]             # default [INBOX]
      initial_lookback: 720h                  # default
```

Gmail:

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
| `on_degraded_filter` | `hold`, `fail`, `proceed` | `hold` | What to do when a rule's `from_domains_file` or `from_regex_file` cannot be read. See below. |
| `quarantine_dir` | path | `<state_dir>/quarantine` | Where quarantined messages are parked. |
| `accounts` | list | — | Mailboxes to pull from. At least one is required. |
| `accounts[].name` | string | — | Required, unique. Names the state file and is what `rules[].account` refers to. |
| `accounts[].provider` | `imap`, `gmail` | — | **Required**; there is no default. Which backend fetches. See [Two ways to connect a mailbox](#two-ways-to-connect-a-mailbox). |
| `accounts[].imap` | mapping | — | Required — and only permitted — when the provider is `imap`. |
| `accounts[].imap.host` | string | — | Required. `imap.fastmail.com`, `imap.gmail.com`, `127.0.0.1` for the Proton Bridge. |
| `accounts[].imap.port` | integer | `993` | 993 is implicit TLS (IMAPS) and pairs with the `tls: true` default. |
| `accounts[].imap.username` | string | — | Required. Usually the full address; some providers want the bare local part. |
| `accounts[].imap.password_cmd` | shell command | — | Required. Run under `/bin/sh -c`; its stdout is the password. **There is deliberately no `password` key** — the secret stays in your password manager. |
| `accounts[].imap.mailboxes` | list of strings | `[INBOX]` | Folders to fetch, each with its own cursor. A name doubles as the `label` predicate value. A folder the server does not have is an error, not an empty folder. |
| `accounts[].imap.tls` | boolean | `true` | Implicit TLS on connect. `false` sends the password and every body in the clear; `validate` warns. Legitimate only on loopback or behind an stunnel. |
| `accounts[].imap.initial_lookback` | Go duration | `720h` | How far back a first-ever sync of each mailbox reaches, and again after any UIDVALIDITY change. Must be positive. |
| `accounts[].gmail` | mapping | — | Required — and only permitted — when the provider is `gmail`. |
| `accounts[].gmail.credentials_file` | path | — | Required. The OAuth **client** JSON downloaded from Google Cloud. |
| `accounts[].gmail.token_file` | path | — | Required. Where `auth` caches the OAuth token, mode 0600. |
| `accounts[].gmail.query` | string | none | Optional Gmail search expression. A cost optimization for the **first-ever** scan only — see below. |
| `accounts[].gmail.initial_lookback` | Go duration | `720h` | How far back the first-ever scan reaches. Must be positive. See [Backfill](#backfill-the-first-run). |
| `accounts[].gmail.include_spam_trash` | boolean | `false` | Fetch messages in Spam and Trash. Honoured identically by both Gmail sync paths. `validate` warns when true. See [Spam and Trash](#spam-and-trash-gmail). |
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
  `from_domains_file` and `from_regex_file` values inside a match tree. `~user` forms are not
  supported. An undefined variable expands to the empty string, as in a shell.
- **`gmail.query` does not filter what gets kept, and applies to less than you
  think.** It is sent to Gmail on the **first-ever** scan of an account and
  nowhere else — not on incremental cycles, and not on a recovery scan after the
  history cursor expires. It is never re-applied locally. Your rules are the
  only authority on what is stored. Keep the query broad, or omit it.
- **Spam and Trash are not fetched by default (Gmail).** Both Gmail sync paths
  agree on this: full scans pass `includeSpamTrash=false`, and the incremental
  path drops messages labelled `SPAM` or `TRASH` before they reach the pipeline.
  Set `gmail.include_spam_trash: true` to fetch them anyway — see
  [Spam and Trash](#spam-and-trash-gmail). On IMAP there is no equivalent key: you
  fetch exactly the folders you list in `mailboxes:`, so simply not listing the
  junk folder is the whole mechanism.
- **The `gmail:` and `imap:` blocks are mutually exclusive.** Setting the one
  that does not match `provider:` is a hard error rather than a silently ignored
  block, so an `imap:` block under a Gmail account cannot leave you believing
  you are fetching over IMAP when you are not.

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

**`on_degraded_filter`** — a rule's `from_domains_file` or `from_regex_file` is missing, unreadable,
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
| `from_regex_file` | path | same, with the patterns read from an externally-owned file each cycle |
| `to_regex` | RE2 pattern | the pattern matches any `To` or `Cc` addr-spec |
| `subject_regex` | RE2 pattern | the pattern matches the decoded `Subject` |
| `header` | `{name: X-Foo, regex: ...}` | the pattern matches any value of that header |
| `has_attachment` | `true` / `false` | the message does (or does not) carry a real attachment |
| `label` | label name | the message carries that provider label, compared exactly. On Gmail that is a Gmail label; on IMAP it is the name of the mailbox the message came from |
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

# Patterns owned and updated by another program, for senders whose host cannot
# be enumerated in advance (wagepoint.teamtailor.com, mail.wagepoint.com).
- from_regex_file: ~/.local/share/jobsearch/companies.txt

# Anything addressed to a plus-alias you hand out to vendors.
- to_regex: "(?i)^me\\+vendors@example\\.com$"

# Application acknowledgements, case-insensitively.
- subject_regex: "(?i)(your application|application received)"

# Everything a mailing list tags for you.
- header: {name: List-Id, regex: "golang-nuts"}

# Only messages that actually carry a file.
- has_attachment: true

# On Gmail: labels exactly as shown in the UI. Nested labels use "Parent/Child";
# system labels are upper case (INBOX, SENT, UNREAD, STARRED).
# On IMAP: the mailbox the message came from, verbatim as the server names it,
# including its hierarchy separator ("Lists/golang", "Lists.golang").
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
  On an IMAP account the values are the mailbox names you listed under
  `imap.mailboxes`, so a message can carry only the one it was fetched from.
- `older_than` / `newer_than` compare against the message `Date` header, falling
  back to the provider's internal date when the header is missing or
  unparseable. A message with no usable date matches neither.
- Patterns are Go [RE2](https://github.com/google/re2/wiki/Syntax): no
  backreferences and no lookaround. Prefix with `(?i)` for case-insensitivity.
  In YAML, prefer double quotes and escape backslashes (`"\\."`), or use single
  quotes where no escaping is needed.
- Use `true` / `false` for `has_attachment`. YAML 1.2 treats `yes` and `no` as
  strings, and mail-muncher rejects them.

### Spam and Trash (Gmail)

**Spam and Trash are not fetched by default.** Nothing in those folders reaches
your rules, and nothing lands on disk. Spam is the likeliest source of hostile,
attacker-authored text in a pipeline that ends in a model's context window, so
the default is to leave it where Gmail put it.

This whole section is about the Gmail provider. IMAP has no equivalent key
because it needs none: an IMAP account fetches exactly the folders named in
`imap.mailboxes`, so junk arrives only if you ask for it by name.

If you want it anyway — a legitimate message misfiled as spam, or an archive
that is genuinely complete — set the key per account:

```yaml
accounts:
  - name: personal
    gmail:
      include_spam_trash: true   # validate warns; that is deliberate
```

The two settings do different jobs, and you may want both:

| | Decides |
| --- | --- |
| `gmail.include_spam_trash` | whether those messages are **fetched at all** |
| A rule on the `SPAM` / `TRASH` labels | what happens to them **once fetched** |

`gmail.query` cannot do either job. It is sent only on the first-ever scan, so
`-in:spam` there does nothing for any later cycle. With
`include_spam_trash: true` set, discriminate with a rule — the filter engine is
the only thing that sees every fetched message:

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

**The files mail-muncher writes are its public API.** This section is the tour;
[docs/output-format.md](docs/output-format.md) is the contract — every
frontmatter key, why the frontmatter needs a real YAML parser, and the rules
for enumerating a delivery tree safely. Read it before you write a consumer,
and see [examples/read_delivered.py](examples/read_delivered.py) for a short
correct one.

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
- **Frontmatter** always carries `subject`, `from`, `from_address`,
  `from_addresses`, `to`, `to_addresses`, `date`, `message_id`, `thread_id`,
  `thread_id_source`, `account`, `rule`. `cc`, `cc_addresses`, `in_reply_to`,
  `labels` and `attachments` are omitted when empty. **Parse addresses from the
  `*_address` / `*_addresses` fields, never from `from`/`to`/`cc`** — those are
  display strings, and a sender-chosen display name containing `<`, `>` or `,`
  makes them ambiguous. The machine-readable fields carry bare addr-specs and
  cannot be spoofed that way. It is produced with a YAML
  encoder, not string formatting, so a subject full of quotes and colons cannot
  break the parse — which also means **you need a real YAML parser to read it**.
  An emoji subject arrives double-quoted with a `\U0001F389` escape, and a
  subject containing a newline arrives as a `|-` block scalar. A `key: value`
  splitter gets both wrong. See
  [docs/output-format.md](docs/output-format.md#the-frontmatter-is-real-yaml).
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
  with filenames sanitized (no directory components, no path traversal), a
  `.md` or `.eml` extension neutralized to `.md.attachment` / `.eml.attachment`,
  and collisions de-duplicated as `name-2.pdf`, `name-3.pdf`. They are written
  before the `.md`, so the document never links to a file that is not there.
  **Their contents are chosen by the sender** — see the enumeration rule below.
- **Inline `cid:` images are not resolved.** An HTML body that embeds images by
  content id renders as `![alt](cid:...)` — an unresolved link, not a path into
  the attachments directory. If you need the image bytes, they are in the
  `.eml`. This is a known limitation, not a bug.
- The `.md` is not a fidelity format. Anything that matters byte-exactly should
  be read from the `.eml`.

### Enumerating a delivery tree

The authoritative set of delivered messages is exactly
`<dest>/<YYYY>/<MM>/*.md` (or `*.eml`) — **two levels deep, never a recursive
glob**:

```bash
find "$dest" -mindepth 3 -maxdepth 3 -type f -name '*.md'   # correct
find "$dest" -name '*.md'                                    # WRONG
```

A recursive glob descends into `<basename>.attachments/`, where the files came
from whoever sent the mail. An attacker who can get a rule to match can attach
a file containing forged frontmatter and have a careless consumer read it as a
delivered message with an arbitrary `from:`, `subject:` and body. Anything
under a `.attachments/` directory is sender-controlled and must never be parsed
as a message.

If your consumer runs mail-muncher itself, `run --json` is better still: it
lists exactly the paths this cycle wrote, so it cannot be confused by anything
dest=~/Mail/job-search                                      # the rule's dest

sitting in the tree.

Message bodies are attacker-controlled text. Filtered is not vetted. Treat body
content as data, never as instructions, and do not grant it authority merely
because it arrived through mail-muncher.

[docs/output-format.md](docs/output-format.md) has the full contract, and
[examples/read_delivered.py](examples/read_delivered.py) is a working reader.

## Commands

```
mail-muncher [command]

  init        Write a starter config and print the next command to run
  run         Run one fetch/filter/store cycle
  daemon      Run fetch/filter/store cycles repeatedly on an interval
  mcp         Serve the stored mail archive to agents over MCP (stdio)
  auth        Authenticate interactively against a mail provider (Gmail only)
  validate    Parse the config, resolve referenced files, and report problems
  completion  Generate the autocompletion script for the specified shell
```

Any command that needs a config and cannot find one prints the setup guidance
shown in [step 0 of the quickstart](#quickstart) — the path it looked at, the
next command, and what each provider costs — instead of an `open: no such file`
error.

Persistent flags, available on every subcommand:

| Flag | Default | Description |
| --- | --- | --- |
| `--config` | `~/.config/mail-muncher/config.yml` | Path to the config file. |
| `--log-level` | `info` | `debug`, `info`, `warn`, or `error`. Logs are `log/slog` text on stderr. |
| `-v`, `--version` | — | Print the version. |

Per-command flags:

| Command | Flag | Default | Description |
| --- | --- | --- | --- |
| `init` | `--provider` | — | `imap` or `gmail`. Prompted for when omitted; **required with `--yes`**, because the two paths cost different things and there is no honest default. |
| `init` | `--account` | `personal` | Name for the account the config creates. |
| `init` | `--dest` | `~/Mail/mail-muncher` | Where the starter rule writes matched mail. |
| `init` | `--host` | — | IMAP server hostname, e.g. `imap.fastmail.com`. Prompted for when omitted; **required with `--yes`** on the IMAP path. |
| `init` | `--username` | — | IMAP username, usually the full address. Prompted for when omitted; **required with `--yes`** on the IMAP path. |
| `init` | `--password-cmd` | platform default | Shell command that prints the app password on stdout. Defaults to Keychain on macOS, `secret-tool` on Linux, `pass` elsewhere. |
| `init` | `--yes` | `false` | Never prompt; take the default for every answer that has an honest one. Still requires `--provider`, and on IMAP `--host` and `--username`. |
| `init` | `--force` | `false` | Overwrite an existing config. Without it, `init` refuses and exits 1 rather than clobbering somebody's rules and credential paths. |
| `run` | `--dry-run` | `false` | Fetch and evaluate, report what would be written, write nothing and save no state. |
| `run` | `--json` | `false` | Write a machine-readable manifest to stdout, one JSON object per account. |
| `daemon` | `--interval` | `5m` | Time between cycles, minimum 30s. Each sleep is jittered by up to ±10%. |
| `daemon` | `--dry-run` | `false` | As `run --dry-run`, on every tick. |
| `daemon` | `--json` | `false` | Write a manifest to stdout after every cycle: newline-delimited JSON, one object per account per tick. |
| `auth` | `--account` | — | Which account to authenticate. Required when the config has more than one. Gmail accounts only — on an IMAP account `auth` refuses, because there is nothing to authorize. |

`mcp` takes no flags of its own; it is configured entirely by `--config`. Note
that `init` writes to `--config` rather than reading from it, and creates the
directory (0700) and the file (0600) as needed.

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
| 1 | Config or validation error: the file would not parse, a rule would not compile, a required key is missing, or there is no config file at all. Nothing was fetched. Also what `init` returns when a config already exists and `--force` was not given. |
| 2 | Provider or authentication failure: a Gmail token that was never written or that Google rejected, an IMAP `password_cmd` that failed or a login the server refused, the server unreachable after retries. |
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
| `vanished` | messages | Messages the provider listed and then found no longer existed — deleted between the listing and the download. Skipped, and the cursor advanced past them. Only printed when non-zero. |

The field list runs contiguously from `fetched=` to `duration=`, so anything
unusual about the cycle is marked on the account label instead:

```
personal (dry-run): fetched=42 ...
personal (degraded, state held): fetched=42 ...
personal (stopped): fetched=12 ...
```

`vanished=N` is the one counter appended conditionally. It appears after
`quarantined=` only in the runs where a message disappeared mid-cycle, and is
omitted everywhere else, so the published field list is unchanged for every
other run. Under `--json` it is `summary.vanished`, likewise omitted when zero
— parse it as `.summary.vanished // 0` if you want to alert on it.

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

Each account's state file is JSON. The cursor inside it is the provider's, so
its shape differs. Gmail:

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

IMAP tracks each mailbox independently, as a UIDVALIDITY/UID pair under `extra`:

```json
{
  "last_sync_time": "2026-07-28T09:15:00Z",
  "extra": {
    "imap.INBOX.uidvalidity": "1650000000",
    "imap.INBOX.last_uid": "48213"
  },
  "seen_ids": ["personal:INBOX:1650000000:48213"]
}
```

A cycle whose stored UIDVALIDITY still matches the server's asks for
`UID FETCH 48214:*` — only what arrived since. A cycle that finds it changed
throws the stored UID away and resyncs from `initial_lookback`, because
UIDVALIDITY changing is the protocol announcing that every UID mail-muncher
remembers now names a different message. That resync re-archives the window it
covers under fresh filenames, since a message's identity on this path is
`<account>:<mailbox>:<uidvalidity>:<uid>`. The trade is deliberate: duplicated
mail can be deleted, silently skipped mail cannot be recovered. Full detail:
[docs/configuration.md](docs/configuration.md#how-imap-sync-works).

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
[`contrib/launchd/`](contrib/launchd/). Copy it in, edit the four absolute
paths inside the copy — `launchd` expands neither `~` nor `$HOME` — then load
it:

```bash
cp contrib/launchd/com.craigjmidwinter.mail-muncher.plist ~/Library/LaunchAgents/
# edit the four paths in the copy, then:
launchctl load ~/Library/LaunchAgents/com.craigjmidwinter.mail-muncher.plist
```

**systemd** — no unit ships yet; `mail-muncher daemon --interval 5m` is a
straightforward `Type=simple` service, or pair `mail-muncher run` with a timer.

## Backfill: the first run

A wide first run is an **intended mode**, not an abuse. The default
`initial_lookback` is `720h` (30 days), which quietly means the thing you most
want on day one — the whole history of what you are collecting, on disk, once —
is the thing that does not happen by default.

Set it wide for the first run. The key exists on both providers, under whichever
block your account uses:

To stop it, `launchctl unload` the same path. Removing it for good means
deleting that file too — see [Uninstall](#uninstall).

```yaml
accounts:
  - name: personal
    gmail:
      credentials_file: ~/.config/mail-muncher/credentials.json
      token_file: ~/.config/mail-muncher/token.json
      initial_lookback: 13140h   # ~18 months — first run only
```

```yaml
accounts:
  - name: personal
    provider: imap
    imap:
      host: imap.fastmail.com
      username: you@fastmail.com
      password_cmd: pass show mail/fastmail
      initial_lookback: 13140h   # ~18 months — first run only, per mailbox
```

Then run it once, and **drop the value back afterwards**:

```yaml
      initial_lookback: 720h
```

Dropping it back is safe because `initial_lookback` only ever bounds the
*first-ever* scan — of an account on Gmail, of each mailbox on IMAP. Every later
cycle resumes from the stored cursor and ignores the key entirely. It is
re-armed only by deleting the account's state file, or, on IMAP, by the server
changing UIDVALIDITY.

What to expect:

- It is **one large full scan**, rate-limited and retried, and it may take a
  while. On Gmail, downloads run four at a time and pages are 500 messages;
  neither is configurable. On IMAP it becomes a `SINCE` UID search per mailbox.
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

Pre-1.0. The current release is
[v0.4.0](https://github.com/craigjmidwinter/mail-muncher/releases/latest), and
what changed in each one is in [CHANGELOG.md](CHANGELOG.md); a build without
`make` reports its version as `dev`. The config schema is stable enough to
write against, but treat it as subject to change until 1.0.

| Area | Status |
| --- | --- |
| IMAP provider (`password_cmd`, per-mailbox UID cursors, `EXAMINE` + `BODY.PEEK[]`) | Built |
| Gmail provider (OAuth, full scan, incremental history sync, RAW download) | Built |
| `init` — writes a validated config for either provider, interactive or scripted | Built |
| Setup guidance on every unconfigured or half-configured command | Built |
| `mcp` serving protocol-correctly when unconfigured | Built |
| Config loading and validation | Built |
| Filter engine (all combinators and predicates listed above) | Built |
| `.eml` and markdown sinks | Built |
| `run`, `daemon`, cycle lock, instance lock | Built |
| `--json` run manifests | Built |
| `mcp` — stdio MCP server, five tools | Built |
| Quarantine and the two failure policies | Built |

### Deliberately out of scope

- **Writing to your mailbox.** Read-only is a design constraint, not a phase.
  No labeling, no deletion, no sending, no drafts, and on IMAP not even marking
  a message read.
- **Being a mail client.** No UI, and no index. `search_messages` is a
  case-insensitive substring scan over the stored files, not a search engine:
  no stemming, no ranking, no query language. Threads are grouped by an id
  carried on each message, not reassembled into a conversation model.
- **A network API.** Nothing listens on a socket. `mcp` speaks a stdio protocol
  to a client that launched it as a subprocess — no port, no HTTP endpoint, no
  network surface. The only thing that ever binds is the loopback port `auth`
  opens for a few seconds during the OAuth redirect on the Gmail path.
- **Bundling an OAuth client.** `gmail.readonly` is a Google restricted scope,
  so the Gmail path will always mean registering your own — which is exactly why
  IMAP exists as the low-friction way in.

### Known limitations

- Inline `cid:` images are left as unresolved links in markdown output.
- Subject slugs are ASCII-only; non-Latin subjects file as `no-subject`.
- No predicate sees the message body; matching is on headers, labels and dates.

Gmail:

- The Gmail token expires every 7 days on a Testing-mode consent screen, so
  `mail-muncher auth` is a weekly chore. This is Google's policy, not a setting.
- `gmail.query` applies only to the first-ever scan of an account — not to
  incremental cycles, and not to a recovery scan after the cursor expires.
- Spam and Trash are not fetched at all unless
  `gmail.include_spam_trash: true`. Once fetched, a rule on the `SPAM` and
  `TRASH` labels decides what happens to them.
- Gmail's download concurrency (4) and page size (500) are not configurable.

IMAP:

- An app password is a full mail credential. Read-only is enforced by
  mail-muncher's code, not by your provider — see
  [the comparison](#two-ways-to-connect-a-mailbox).
- A server changing UIDVALIDITY re-archives the `initial_lookback` window under
  fresh filenames. Deliberate: duplicated mail is recoverable, skipped mail is
  not.
- IMAP has no server-side conversation id, so `thread_id` is always synthesized
  from the `References`/`In-Reply-To` chain and `thread_id_source` is never
  `provider`.
- A mailbox the server does not have is an error, not an empty folder — so a
  typo in `mailboxes:` fails the first run rather than looking like quiet mail.
- Only the folders listed in `mailboxes:` are fetched; there is no "everything"
  option and no server-side search.

## Documentation

Browsable at <https://craigjmidwinter.github.io/mail-muncher/>, or in this repo:

- [docs/configuration.md](docs/configuration.md) — every config key for both
  providers, its validation rule, and its failure mode. The
  [`accounts[].imap`](docs/configuration.md#accountsimap) section is the IMAP
  reference; there is no separate setup page because there is no setup beyond
  an app password.
- [docs/gmail-setup.md](docs/gmail-setup.md) — the Gmail path only: Google Cloud
  walkthrough, the seven-day expiry, and every OAuth error message with its fix.
  Nothing in it applies to an IMAP account.
- [docs/filters.md](docs/filters.md) — the complete match-tree language plus a
  cookbook of real rules.
- [docs/output-format.md](docs/output-format.md) — the on-disk contract:
  layout, filenames, every frontmatter key, and the security rules for
  enumerating a delivery tree. Read it before writing a consumer.
- [docs/manifest.md](docs/manifest.md) — the `--json` manifest contract, field
  by field.
- [docs/mcp.md](docs/mcp.md) — the MCP server: client wiring and every tool's
  arguments and return shape.
- [docs/architecture.md](docs/architecture.md) — the pipeline, its seams, and
  where your change belongs.
- [CONTRIBUTING.md](CONTRIBUTING.md) — build, test, and the conventions the
  codebase holds itself to.

Runnable configs: [`examples/imap.yml`](examples/imap.yml) (the IMAP path,
heavily commented), [`examples/minimal.yml`](examples/minimal.yml) (the smallest
useful Gmail config), [`examples/job-search.yml`](examples/job-search.yml) (an
externally-managed filter file in anger), and
[`examples/read_delivered.py`](examples/read_delivered.py) (a correct consumer).
All three configs pass `mail-muncher validate --config <file>`.

## Acknowledgements

The wordmark is set in [Silkscreen](https://github.com/googlefonts/silkscreen)
by Jason Kottke, used under the SIL Open Font License 1.1. Brand assets, the
palette and usage rules are in [branding/BRAND.md](branding/BRAND.md).

## License

MIT. See [LICENSE](LICENSE).
