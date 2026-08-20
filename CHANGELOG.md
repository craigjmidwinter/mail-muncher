# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- CI now publishes the MCP Registry entry itself, from the pushed tag, instead
  of requiring a hand-run `mcp-publisher` device-code login before every
  release. A second job rewrites `version` and the image tag from
  `GITHUB_REF_NAME` and publishes over OIDC — no registry token lives in this
  repository to rotate or leak. The `version` committed in `server.json` is
  therefore last release's by design; CI stamps it on tag, and the README
  says so. `server.json` is also now validated on every PR, not just at
  release time.

## [0.4.0] - 2026-07-30

### Added

- A container image at `ghcr.io/craigjmidwinter/mail-muncher`, built for
  darwin/linux on amd64/arm64 on every release, and mail-muncher's first
  listing on the [MCP Registry](https://registry.modelcontextprotocol.io).
  OCI was the only distribution shape a GitHub release of Go binaries could
  fit into that registry. The image is Alpine, not scratch or distroless,
  because `imap.password_cmd` runs under `/bin/sh` — without a shell, IMAP
  auth can't work at all, and the README says so plainly rather than
  papering over the trade.

### Fixed

- The bundled Claude Code skill's README section no longer claims it only
  knows the Gmail path. The skill itself caught up with IMAP and `init`
  back in v0.2.0; this release fixed the one paragraph that still told
  readers otherwise, which was actively steering people away from the
  two-minute IMAP route the rest of the README argues for.

## [0.3.0] - 2026-07-30

### Added

- `from_regex_file: path`, a second program-owned filter predicate
  alongside `from_domains_file`. It reads one RE2 pattern per line,
  reloaded at the start of every cycle, so a program that only knows a
  company's name — not yet every domain it mails from — can subscribe to a
  pattern instead of waiting on a human to add each new domain by hand.
  Because a typo here can match (and archive) an entire mailbox rather
  than just matching nothing, an empty pattern or one that matches the
  empty string is refused outright, and a pattern that won't compile is
  refused on its own without taking the rest of the file down with it.
- `list_rules` (the MCP tool) now resolves and reports `from_regex_file`
  subscriptions the same way it already did for `from_domains_file`.

### Fixed

- A message deleted between being listed and being fetched no longer
  wedges Gmail sync permanently. Gmail's 404 (`notFound`) on a single
  message fetch is now treated as authoritative evidence the message is
  gone rather than a transient fetch failure — it's skipped, loudly, and
  the cycle completes. Previously the cursor refused to advance, so the
  next cycle re-listed the same dead id and 404'd again, forever, until
  someone deleted the sync cursor by hand.

### Changed

- The pattern-file accounting shared by both program-owned predicates now
  lives in `internal/filter`; the duplicate copy in `internal/mcpserver`
  is gone.

## [0.2.0] - 2026-07-29

### Added

- `provider: imap` — mail-muncher's first mailbox path that isn't gated
  behind a Google Cloud project. Two lines of config and an app password
  from a provider's settings page (host, username, `password_cmd`;
  everything else defaults) work against Gmail, Fastmail, iCloud, Proton
  Bridge, or a work account, in contrast to the Gmail API path's OAuth
  client registration and a refresh token Google expires weekly on a
  Testing-mode consent screen.
- `mail-muncher init`, and a genuinely useful unconfigured/
  partially-configured state. Every command that needs config now names
  the exact path it checked, the exact next command to run, and states
  plainly what each provider path costs — neither IMAP nor Gmail is
  presented as the "right" one.
- Signed release binaries for darwin/linux on amd64/arm64, plus a
  Homebrew tap (`brew install craigjmidwinter/tap/mail-muncher`) and a
  keyless cosign signature over the release checksums — installing no
  longer requires a Go toolchain, and the binary is verifiable.
- Machine-readable From/To/Cc address fields in the markdown sink's
  frontmatter, alongside the existing display strings.
- A brand: a pixel-art mark, a Silkscreen wordmark, and a two-scheme
  (light/dark) docs site.

### Changed

- Gmail sync no longer delivers Spam and Trash by default
  (`gmail.include_spam_trash`, default `false`). A real 186-message run
  had delivered spam straight into an LLM's context window — the one
  place attacker-authored text does the most damage. Both the full-scan
  and incremental sync paths honor the same setting, so a recovery scan
  can't pull in mail the incremental path was told to skip.
- `accounts[].provider` is now required and no longer silently defaults
  to `gmail`. A config that named no provider was enrolling its author in
  the ten-minute Google Cloud Console path and a weekly-expiring token
  without them ever choosing that.

### Fixed

- Attachments extracted alongside a delivered message can no longer be
  mistaken for delivered top-level messages themselves.

## [0.1.0] - 2026-07-29

First tagged release.

### Added

- A Gmail provider requesting exactly one OAuth scope, `gmail.readonly` —
  it cannot send, delete, label, or modify anything in the mailbox. A
  loopback authorization-code flow with PKCE — `auth` starts a temporary
  server on a loopback port and catches the redirect, so no secret is
  pasted through a terminal — plus a full scan, incremental history sync,
  and RAW message download.
- The filter engine: the full match-tree language of combinators and
  predicates. A rule can take its input from a plain text file that some
  other program owns (`from_domains_file`), re-read at the start of every
  cycle, so that program changes what it receives by editing one line —
  no config edit, no restart.
- `.eml` and markdown sinks, with attachments extracted alongside;
  markdown carries the headers as YAML frontmatter.
- `run` (one-shot, for cron) and `daemon` (a polling loop), both guarded
  by a cycle lock and an instance lock so overlapping invocations can't
  collide.
- `--json` run manifests on stdout, with stderr reserved for logs — a
  documented, machine-readable record of every cycle.
- `mcp`, a stdio MCP server over the archive, exposing `list_rules`,
  `list_messages`, `read_message`, `search_messages`, and `sync`.
- Quarantine and failure policies: messages that can't be delivered are
  parked, not dropped, and counted in the manifest.
- A Claude Code plugin bundling the MCP server.
- `mail-muncher validate`, which checks a config without touching the
  network.

Not in this release: IMAP was specified but not built — the config
rejected any provider other than `gmail` (shipped in 0.2.0). By design,
not planned for any release: writing to the mailbox (no labeling,
deletion, sending, or drafts), a UI, and a real search index —
`search_messages` was, and remains, a case-insensitive substring scan.

[Unreleased]: https://github.com/craigjmidwinter/mail-muncher/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/craigjmidwinter/mail-muncher/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/craigjmidwinter/mail-muncher/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/craigjmidwinter/mail-muncher/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/craigjmidwinter/mail-muncher/releases/tag/v0.1.0
