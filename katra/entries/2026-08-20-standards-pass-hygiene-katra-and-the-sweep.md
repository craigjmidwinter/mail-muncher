---
title: 'Standards pass: hygiene, katra, and the sweep'
date: "2026-08-20"
time: "16:35:30"
tags:
    - standards
    - hygiene
    - security
    - katra
hash: 7c9a919
stat:
    f: 61
    a: 1192
    d: 78
summary: Gap check against the fleet PROJECT-STANDARDS sections this repo predates
closes:
    - standards-pass-hygiene-katra-practice-and-the-sweep
advances:
    - standards-pass-hygiene-katra-practice-and-the-sweep
---

mail-muncher is an exemplar behind the fleet's PROJECT-STANDARDS, which means
the parts of the standard it *predates* are exactly the parts nobody has
checked. The docs and the brand set the bar. This pass went after the three
sections written after that bar was set: project hygiene, katra practice, and
the timeboxed performance-and-security sweep.

## The sweep

**Secrets: clean, and no escalation.** The working tree carries nothing
credential-shaped; the history spot-check across all 46 commits found no
content hits for Google API keys, OAuth access tokens, GitHub tokens, AWS
keys, private key blocks or Slack tokens. Seven sensitive-*looking* filenames
have been committed over the project's life — `token.go`, `password.go`, the
OAuth task notes, and `internal/provider/gmail/testdata/credentials.json`.
That last one is the only real fixture, and every version of it that has ever
existed in history is the same fake: client id `1234567890-testclient`, secret
`TEST-not-a-real-secret`. Every email address in the tree is `example.com`,
`acme.example` or `.test`.

A history hit would have been an escalation to Craig rather than a quiet fix.
There was nothing to escalate.

**govulncheck: no reachable vulnerabilities.** One advisory appears, and it is
worth recording rather than hiding:

```
Vulnerability #1: GO-2026-5932
    The golang.org/x/crypto/openpgp package is unmaintained, unsafe by design,
    and has known security issues
  Module: golang.org/x/crypto  Found in: v0.54.0  Fixed in: N/A
```

`Fixed in: N/A` — there is no version to upgrade to, because the fix is that
the package should not be used. mail-muncher does not use it: it arrives as an
unreachable corner of a transitive `x/crypto`, and symbol analysis confirms
zero of our call paths reach it. Recorded as a written exception, not a
blocker. If `x/crypto` ever drops the package, the advisory goes away on its
own.

## The read-only stance still holds

The fleet cites this project's read-only OAuth posture as its exemplar, so the
pass verified it rather than assuming it:

- `internal/provider/gmail/auth.go:28` — `const Scope = gmailapi.GmailReadonlyScope`,
  the only scope constant in the tree, handed to `google.ConfigFromJSON` at
  line 121.
- No write-capable Gmail call exists anywhere: no `Modify`, `Trash`, `Delete`,
  `BatchModify`, `BatchDelete`, `Send`, `Insert`.
- `internal/provider/imap/imap.go:217` opens the mailbox with
  `&goimap.SelectOptions{ReadOnly: true}` — EXAMINE, not SELECT — and there is
  no `Store`, `Expunge`, `Append`, `Move` or `Copy`.

The claim was true. The gap was that nothing except code review was keeping it
true, so the pass added tests that fail loudly if it is ever widened — an
assertion on the scope constant, an assertion that the IMAP select is
read-only, and a source-level guard that walks `internal/provider/` and
refuses any write verb.

## Katra practice: the board was lying

The sharpest finding of the pass was not in the code.

Twenty-six tasks, every one of them `status: todo`. Nine epics, every one
`status: planned`. Meanwhile v0.4.0 had shipped the config loader, the filter
engine, both providers, both sinks, the run manifest, the MCP server, daemon
mode, the `init` command, prebuilt binaries and a container image. The board
described a project that had not started.

A task board that says `todo` about finished work is worse than no board,
because it costs a reader real time before they learn not to trust it. Each
task was checked against the tree before its status moved — `markdown-sink`
against `internal/sink/markdown.go`, `gmail-incremental-sync-via-history-api`
against `internal/provider/gmail/history.go`, `daemon-mode-with-poll-interval-and-lockfile`
against `cmd/mail-muncher/daemon.go` and the `flock` dependency — not marked
done on the strength of the title sounding familiar.

The board now reads: 23 done, 1 doing, 2 genuinely open. The two that are open
are open for real reasons — `plus-address-and-ats-domain-rules` has no code
behind it, and `move-tap-publishing-to-a-github-app` is still a PAT in
`.goreleaser.yml:139`. Epic status was rolled up from the children rather than
set by hand: six done, three active.

The other half of katra practice is thinner and stays honest about it. One
devlog entry covers 46 commits. Decisions are the bright spot — six ADRs, and
`from_regex_file` went through a committed spec before implementation, which
is the designed/specced phase the standard asks for, already working.


## Release discipline: the README had gone stale

Four tags shipped — v0.1.0 through v0.4.0, each with all four platform
archives, a `checksums.txt` and a keyless cosign signature attached. The
release machinery is in good shape.

What had rotted was the part a reader actually sees. `## Status and scope`
opened with "The current release is v0.1.0" while v0.4.0 had been out since
30 July. The link under that text pointed at `/releases/latest`, so it went to
the right place — only the words were wrong, which is the worst version of
this bug, because nothing ever 404s to reveal it.

There was also no `CHANGELOG.md` at all, against four releases with real
release notes on GitHub. That is now written to Keep a Changelog form, newest
first, in the project's voice and from the actual commit and release-note
evidence rather than from memory. Drafting it caught a second stale claim: the
Gmail `auth` command was described as a device flow, when
`internal/provider/gmail/auth.go:213` runs a loopback authorization-code flow
with PKCE — it binds a port, opens the browser, and catches the redirect.
Nobody pastes a code.

The README's one implementation number was checked rather than assumed:
"downloads run four at a time and pages are 500 messages" still matches
`DefaultConcurrency = 4` and `DefaultPageSize int64 = 500` in
`internal/provider/gmail/gmail.go:30,34`, and neither is reachable from config,
so "neither is configurable" holds too.

## What the pattern read found

Path traversal was the one to get right, because the sink builds filenames out
of mail — a subject, an attachment name, a message-id, all attacker-chosen.
It came back clean, and clean for structural reasons rather than luck.
`Slug()` whitelists to `[a-z0-9]`, so no input produces a dot or a slash. An
attachment named `..` is stripped by `strings.Trim(name, "-.")` down to the
empty string and falls back to `attachment`. Directory creation refuses to
follow symlinks at every level, and file creation is `link(2)`-based or
`O_EXCL`. Nothing mail-derived escapes `dest`.

Config parsing is strict — `internal/config/config.go:436` sets
`dec.KnownFields(true)`, so a misspelled `include_spam_trash` is a hard error
rather than a silently-ignored key, which is the failure mode that matters for
a security-relevant setting. No `unsafe`, no `gob`, nothing fetches a URL from
mail content, and `NewWithHTTPClient`'s endpoint override is hardcoded to `""`
everywhere outside tests. Every sensitive file is 0600 and every directory
0700, set explicitly rather than left to umask.

Three things are worth writing down rather than fixing on the spot:

**No message-size cap exists.** A message is buffered whole, decoded whole,
and held as at least two copies. Gmail caps messages at 25 MB so that path
tops out around 100 MB, and the unbounded case needs a hostile IMAP server the
operator configured themselves — but `daemon` runs unattended, so a ceiling is
cheap insurance. Filed as a task rather than patched, because where the
ceiling goes is a design decision and the quarantine path is probably the
right home for the rejection.

**`javascript:` and `data:` hrefs survive into the markdown.** The dangerous
part is already handled: `html-to-markdown` deletes `script`, `iframe`,
`style` and friends *with* their text before rendering. What survives is the
URL, because `converter/url.go` has no scheme allow-list. It is inert in an
archive nobody renders, and live only for a downstream consumer that turns the
`.md` into clickable HTML without sanitizing. Filed as a decision to make on
the record — strip the schemes, or document the sharp edge — rather than
silently picked. Worth noting `internal/sink/hostile_test.go` sounds like it
covers this and does not; it is entirely about filesystem symlinks.

**Two costs that are by design, not bugs.** `domainsMatch` is
O(from-domains × list-entries) because suffix matching cannot be a hash lookup,
and the run manifest holds one entry per message per format for a whole cycle
because `--json` and the MCP `sync` tool are documented to return one document.
Both are linear and both are inherent to a contract the project already made.
Recorded, not filed.

The reassuring find: `SyncState.Seen` is a linear scan, and would have been a
real O(n²) at 100k messages — except `MaxSeenIDs = 2000` caps it. Someone
already thought about it.

## A note on running four agents over one working tree

One leg reported seeing `p.svc.Users.Messages.Trash(...)` in `fetch.go` — a
call that would have flatly contradicted everything above. It did not report
it. It re-checked with `git diff`, `git blame` and a fresh read, found no such
line, and flagged the discrepancy instead.

It was real, briefly: another leg was deliberately breaking the invariant to
prove its new regression test actually fails. Two agents, one tree, one of
them reading while the other edited.

The lesson is not to stop parallelising — it is that a finding which
contradicts the rest of the evidence deserves a second look before it becomes
a headline. That instinct is worth more than the four minutes it cost.

## Postscript: the gate has a price, and it showed up in CI

The lint step went in, and the first CI run on main took the `build / vet /
test` job from under three minutes to over forty. Every earlier step passed in
seconds; `Lint` sat there alone.

This is the cold-cache cost, and it is not a surprise — a run against an empty
`GOCACHE` was measured locally at over 25 minutes, which is why the config's
timeout was raised from 10m to 30m before the commit went out. What is new is
seeing it land on a fresh runner, where there is no warm cache to fall back on.
`staticcheck` and `gosec` both build whole-program analysis facts, and this
module's dependency graph pulls in grpc, otel and google-api-go-client.

The open question is whether `golangci-lint-action`'s cache amortises it. If
run two comes back in a couple of minutes, this is a one-time cost paid on
cache misses and the gate is worth keeping as-is. If run two is also slow, the
set has to change — and the obvious candidate to move is `gosec`, since it is
the expensive one. That would be a real loss: `gosec` is what caught the
unescaped `error_description` in the OAuth callback, which is the only genuine
bug this whole pass turned up.

Measuring before changing. A forty-minute lint on every pull request is not a
healthy repo, and shipping the gate without knowing which of those two worlds
we are in would be leaving the job half done.

## The measurement that overturned the obvious answer

The gate came out, and the working theory went in with it: `gosec` is the
expensive linter, so split it into a scheduled job and keep a fast gate on
every PR. It is the tidy answer. It is also wrong, and the only reason we know
is that the measurement was run instead of assumed.

| Linter set, cold cache, same machine | Time |
| --- | --- |
| Full set, including `gosec` | ~26 min |
| Same set, `gosec` disabled | 25m 54s |

Six seconds. The security linter that found the only real bug in this pass is
very nearly free, because the whole-program analysis it needs is already being
built by `staticcheck` and `unused` for the dependency graph — grpc, otel,
google-api-go-client. `gosec` is a passenger, not the driver.

That reverses the recommendation. The lead worth chasing is the 579 KB that
run one saved to `~/.cache/golangci-lint`, which is far too small to be those
facts. Something about the caching is broken, and if it can be fixed the full
set goes straight back into CI with nothing dropped. Only if that fails does
splitting by *cost* — type-aware linters nightly, the rest per-PR — become the
answer.

The uncomfortable part is the order this happened in. The gate was added, the
cost was discovered by CI rather than by me, and the fix was recommended
before it was measured. The measurement took under half an hour and would have
prevented all three. The lesson is not "measure more"; it is that a number I
already had — 25 minutes, locally, before any of this shipped — was treated as
a timeout to accommodate rather than a verdict to act on.
