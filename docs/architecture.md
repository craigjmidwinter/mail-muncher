---
title: Architecture
layout: default
nav_order: 8
description: >-
  How mail-muncher is put together — the provider and sink seams, the fetch and
  filter pipeline, and where a change belongs when you go to make one.
---

# Architecture

Written for someone about to change something and deciding where the change
belongs.

## The shape

```
                  provider seam                        sink seam
                       │                                   │
  ┌──────────┐         │    ┌───────┐    ┌────────┐        │   ┌──────────┐
  │  Gmail   │─────────┼───▶│ parse │───▶│ filter │────────┼──▶│ .eml     │
  │  (IMAP)  │         │    │       │    │ engine │        │   │ markdown │
  └──────────┘         │    └───────┘    └────────┘        │   └──────────┘
       ▲               │                      ▲                      │
       │               │                      │                      ▼
  ┌──────────┐         │                ┌───────────┐          files on disk
  │  state   │◀────────┴────────────────│domains.txt│
  │  store   │      cursor saved        │(owned by  │
  └──────────┘      after a cycle       │ another   │
                                        │ program)  │
                                        └───────────┘
```

Data flows left to right and never back. Nothing downstream of the provider
seam knows what a provider is; nothing upstream of the sink seam knows what a
file is.

## Packages

| Package | Owns | Depends on |
| --- | --- | --- |
| `cmd/mail-muncher` | Cobra command tree, flags, exit codes, logger setup. No behavior. | `internal/config`, `internal/filter`, `internal/provider/gmail`, `internal/pipeline` |
| `internal/config` | YAML schema, defaults, path expansion, validation. | `gopkg.in/yaml.v3` only |
| `internal/model` | The canonical `Message` and RFC822 parsing. | `enmime` |
| `internal/provider` | The `Provider` interface, `RawMessage`, `SyncState`, retry/backoff, a `Fake`. | nothing internal |
| `internal/provider/gmail` | OAuth, token cache, list/history/RAW fetch, label resolution. | `internal/provider`, `internal/config` |
| `internal/filter` | Match-tree compilation, the `Engine`, the domain-file cache. | `internal/config`, `internal/model` |
| `internal/sink` | On-disk layout, `.eml` and markdown writers, atomic writes. | `internal/config`, `internal/model` |
| `internal/state` | Per-account sync state files and the `flock`-based lockfile. | `internal/provider` |
| `internal/pipeline` | One cycle: fetch → parse → evaluate → sink → save state. The manifest, the exit-code grading, quarantine. | everything above |
| `internal/mcpserver` | The MCP tools, the archive index over the `dest` trees, and the path jail. | `internal/config`, `internal/model`, `internal/sink`, `internal/filter`, `internal/pipeline` |

The dependency graph is acyclic and shallow on purpose. `internal/config` at
the bottom knows about no other internal package; `internal/pipeline` and
`internal/mcpserver` at the top are the only places that know about all of them.

`internal/mcpserver` sits *beside* the pipeline rather than above it: it reads
the archive on disk directly, and reaches into the pipeline only through
`Runner.Cycle` for the `sync` tool. It never touches a provider.

## The two seams

### The provider seam — pluggable input

`internal/provider.Provider` is the architectural invariant:

```go
type Provider interface {
	Name() string
	Fetch(ctx context.Context, state SyncState, fn func(RawMessage) error) (SyncState, error)
}
```

A provider streams `RawMessage` values — a provider-scoped stable id, complete
RFC822 bytes, an internal date, and label names — and returns an updated
`SyncState`. Everything downstream sees those two types and never a provider
SDK type. That is what makes the choice of Gmail-over-IMAP a reversible
decision rather than a one-way door.

**The test of the seam: adding a provider must require zero changes to
`internal/pipeline`, `internal/filter`, or `internal/sink`.** If your new
provider needs a pipeline change, the seam is wrong and the fix belongs in the
interface, not in a special case downstream.

Three details the interface deliberately pins down:

- **`ID` must be stable across runs.** It is half the input to the sinks'
  idempotency digest, so an unstable id means re-downloading and re-writing the
  same message under a new name forever. Gmail uses the message id; the planned
  IMAP provider will use `account:mailbox:uidvalidity:uid`.
- **`Raw` must be byte-faithful.** The `.eml` sink writes it verbatim and
  `internal/model` parses from it. Re-encoding anywhere upstream breaks DKIM
  verification for every archived message.
- **`SyncState` is carried through, not owned.** A provider updates the fields
  it understands (`HistoryID` for Gmail, `Extra` keys for others) and leaves the
  rest alone. `SeenIDs` belongs to the pipeline.

`Fetch` streams rather than returning a slice so a large first scan does not
have to fit in memory, and so the pipeline can store as it goes.

### The sink seam — pluggable output

`internal/sink.Sink` is the other end:

```go
type Sink interface {
	Format() config.Format
	Path(msg *model.Message, rule *config.Rule) string
	Plan(msg *model.Message, rule *config.Rule) (path string, skipped bool, err error)
	Store(msg *model.Message, rule *config.Rule) (path string, skipped bool, err error)
}
```

`Path` is pure and does no I/O. `Plan` adds exactly one `Stat`. `Store` writes.
The split is what makes `--dry-run` honest: it reports the precise path a real
run would produce without touching the destination tree.

Sinks hold no per-message state and are safe to reuse across messages and
rules. `sink.ForRule` returns one sink per format on a rule, in the rule's
order, collapsing duplicates — that is the fan-out point.

A new output format is a new `Sink` implementation plus a `config.Format`
constant plus a case in `sink.New`. Nothing else changes.

### Reading back: the archive

`internal/mcpserver` is the only code that reads the archive rather than writing
it, and it treats the on-disk layout as the schema: it parses the `[:16]` digest
out of a basename as the message id, reads the `.md` frontmatter for metadata,
and falls back to parsing the `.eml` when there is no markdown rendering. The
index is lazy and keyed by size and mtime, so an unchanged file is never
re-parsed.

Its one security-relevant invariant is the **path jail**. Every rule `dest` is
resolved once at construction; a caller-supplied path is then checked lexically
against those roots, resolved with `filepath.EvalSymlinks`, and checked again
against the resolved roots. Two checks, because either alone is defeatable:
lexical alone follows a planted symlink, and post-resolution alone can be raced.
Only `.eml` and `.md` are readable, the walk never follows symlinks, and a
refusal is deliberately indistinguishable from "no such file" so the jail cannot
be used to probe the filesystem.

## The invariants

Break any of these and something subtle breaks downstream.

**Identity determines path.** `sink.Basename` is
`<unix-seconds>-<sha256(account + ":" + id)[:16]>-<slug(subject)>`, where the
width comes from the exported `sink.HashLen`. Only the digest carries identity;
the timestamp and slug are for humans. This is why:

- Re-running is safe. The file being there means an earlier cycle wrote it.
- The slug is ASCII-only. Non-ASCII filenames are subject to filesystem Unicode
  normalization (HFS+ stores NFD), which can make the name written differ from
  the name the next cycle stats for — and that existence check is the entire
  idempotency story.
- Renaming an account or changing the digest input rewrites every future
  filename and orphans everything already archived.
- The fragment is **public layout**, not an implementation detail:
  `internal/mcpserver` parses it back out of a filename as the message id it
  reports to agents. Changing `HashLen` changes an API.

**The seen set is belt and braces, not the idempotency key.**
`provider.SyncState.SeenIDs` is capped at 2000, FIFO, so any scan larger than
that evicts its earliest ids. This is harmless and must stay that way: the real
idempotency key is the deterministic filename plus skip-on-exists. A re-run
after eviction re-downloads the message and then skips it at the write, costing
bandwidth and nothing else. **Do not "fix" this by unbounding the set** — an
unbounded set grows without limit in a file that is rewritten every cycle, to
buy a guarantee the sinks already provide.

**Writes claim a name, they do not replace one.** A message file goes to a temp
file in its destination directory, is fsynced and chmodded, and is then
**hard-linked** into place with `os.Link`. `link(2)` fails with `EEXIST` rather
than clobbering — unlike `rename(2)`, which cannot express "only if absent" — so
the kernel decides whether the name is free at the instant it is claimed. That
*is* the idempotency check, not a step after one: there is no window between
deciding and acting. A filesystem without hard links falls back to
`O_CREAT|O_EXCL`, trading the no-partial-file guarantee for the same
no-clobber one.

Attachments and state files still use temp-and-rename, deliberately: their names
are not idempotency markers, so replacing is correct for them.

**Symlinks in the layout are refused, never followed.** Nothing a sink writes is
legitimately a link, so one means something else is placing them there.
`ErrSymlink` covers a link at a message's final path and at any directory
component the sink itself creates (`<YYYY>`, `<MM>`, `<basename>.attachments`).
The rule's own `dest:` is exempt — the operator chose that path. Existence
checks use `Lstat`, not `Stat`, so a link planted at a destination is an error
rather than a silent "already stored".

**Message-level errors never abort a cycle by default.** A message that will not
parse, or one whose renderings failed to write, is handled by
`on_message_failure`: `quarantine` (the default) parks the raw bytes under the
quarantine directory, counts it, and lets the cursor advance; `abort` returns a
`*MessageError` and the account's state is not saved. Either way the message is
never both undelivered and forgotten. Provider and auth errors always abort the
account — they will not get better by trying the next message — but the other
accounts still run.

**Domain files are read once per cycle, and unreadable is a reportable event.**
`filter.Engine.BeginCycle` clears the cache and `Preload` re-reads every
referenced list up front, so `Engine.Degraded()` is authoritative *before* the
first message is fetched. That ordering is what makes `on_degraded_filter: fail`
implementable at all: it can only promise to store nothing if it knows first. A
missing file is still not an evaluation error — the predicate matches nothing —
but it is reported, and by default (`hold`) it stops the cursor from advancing.

**State advances past unmatched messages — unless the filter was degraded.** A
message evaluated once is normally never evaluated again, whether or not a rule
claimed it. The exception is deliberate: under `hold` or `abort` the cursor is
held, precisely so mail evaluated against an unreadable list is evaluated again.
Changing rules still does not retroactively archive old mail; deleting a state
file and re-scanning does.

**There are two locks, and they answer different questions.**

- `<state_dir>/mail-muncher.lock` — `flock`-based, held for the duration of each
  cycle by `run`, by every daemon tick, and by the MCP `sync` tool. It answers
  "is a cycle running right now?" and is what stops a cron invocation racing a
  daemon on the same cursors.
- `<state_dir>/instance/mail-muncher.lock` — held by a daemon for its whole
  process lifetime. It answers "is something else already polling this state
  directory?", which the cycle lock cannot: that one is released between ticks,
  so a second daemon starting while the first sleeps would pass it and then poll
  forever alongside it. It lives in a sub-directory so it cannot be confused
  with the cycle lock.

Both return an error matching `state.ErrLocked`, which `pipeline.ExitCode` grades
as exit 3.

**There is one manifest shape.** `pipeline.Manifest` is what `run --json` writes,
what `daemon --json` streams one line at a time, and what the MCP `sync` tool
returns. Its JSON tags are API surface, not debug output — see
[manifest.md](manifest.md). A new field must be additive and `omitempty` unless
it is genuinely always present.

## Where does my change go?

| Change | Where |
| --- | --- |
| A new config key | `internal/config/config.go` (struct + default), `validate.go` (rule + severity), and `docs/configuration.md`. Add it to a test in `config_test.go`. |
| A new match predicate | `internal/filter/compile.go` (a `compileX` function, a case in `compileNode`, an entry in `predicates`), the package doc in `doc.go`, and `docs/filters.md`. If it needs message data that does not exist yet, add an accessor to `internal/model/message.go` first. |
| A new predicate whose value is a file path | Also add the key to `filePathPredicates` in `internal/config/expand.go`, or its `~`/`$VAR` will not expand and `validate` will not check it. |
| A new output format | A new `Sink` in `internal/sink`, a `config.Format` constant, a case in `sink.New`, and `validateFormats` prose. |
| A new provider | A package under `internal/provider/`, implementing `Provider`. A config block and a case in `validateAccounts`. **Nothing in pipeline, filter, or sink.** |
| Anything about what a message exposes | `internal/model`. Parsing lives in `parse.go`, accessors in `message.go`. |
| A new CLI flag | `cmd/mail-muncher`. Keep behavior in an internal package and the command thin. |
| A new manifest field | `internal/pipeline/manifest.go`, and `docs/manifest.md`. Additive and `omitempty`; the JSON tags are API surface. |
| A new MCP tool, or an argument on one | `internal/mcpserver/tools.go` (the input/output structs — schemas are inferred from them, so they cannot drift), `server.go` (`registerTools`, `ToolNames`), and `docs/mcp.md`. Anything that reads a file must go through `Archive.Resolve`. |
| Retry or backoff behavior | `internal/provider/retry.go`, which is shared. Gmail's judgment of *which* errors are retryable lives in `gmail.go` (`retryable`, `isRateLimited`) because it needs the error body. |

## Design decisions worth knowing

**Gmail API, not IMAP, first.** IMAP is more general, but Gmail-over-IMAP needs
an app password, has All-Mail/label duplication quirks, and means hand-rolled
per-folder UID bookkeeping. The Gmail API gives OAuth, `format=RAW` (so storage
and parsing stay provider-neutral anyway), labels as data, and `users.history`
for cheap incremental sync. Because the provider hands over raw RFC822 either
way, IMAP remains a clean second implementation.

**The history cursor is best-effort.** Gmail keeps roughly a week of history. A
404 from `users.history.list` is not an error: the cursor is cleared and the
same cycle falls through to a full scan. The full-scan path is not a legacy
path — it is the permanent fallback, and it must keep working.

**The mailbox cursor is read before listing, not after.** In `fullScan`,
`users.getProfile` is called first. Mail arriving during a scan then still shows
up in the next incremental cycle, and the seen-set absorbs the overlap. Taking
the cursor afterwards would silently skip those messages.

**`gmail.query` is an optimization, not a filter, and it applies to the
first-ever scan only.** The gate is `firstEver := state.HistoryID == 0 &&
state.LastSyncTime.IsZero()`, computed from the *incoming* state before the
expired-cursor branch clears anything — so a recovery scan is not a first run
even though it ends up on the same code path, and it deliberately sends no
query. History results are not query-filtered either, and the query is never
re-applied locally, because the filter engine is the single authority on what is
kept. Two places that both decide what is archived would eventually disagree.

Narrowing the query is therefore only ever a way to make one first run cheaper.
It cannot bound steady-state behavior, and a query that excludes something your
rules would keep makes the two scan modes inconsistent for no benefit.

**Full scans include Spam and Trash.** `users.messages.list` sets
`IncludeSpamTrash(true)` unconditionally. This is not an oversight: the history
path has no such parameter and has always reported everything added anywhere in
the mailbox, so a full scan that excluded Spam and Trash would archive a
different set of mail than an incremental cycle over the same window. Matching
them is what makes the two paths interchangeable — which the expired-cursor
fallback requires. Excluding Spam and Trash is a rule (`not: {label: SPAM}`),
because rules are the only authority on what is kept.

**Recovery scans overlap the watermark by 24 hours.** `RecoveryOverlap` is
subtracted from the stored `LastSyncTime` before it becomes the `after:` bound,
so mail that arrived while the previous cycle was listing is not stepped over.
The overlap re-fetches a day of mail and then skips all of it at the write —
cheap insurance bought with the idempotency the sinks already guarantee.

**Parsing never panics.** `model.Parse` recovers panics from the MIME parser and
returns them as errors. Mail is hostile input from strangers; one malformed
message must not kill a cycle.

**Markdown is a rendering, not a copy.** It is lossy by design — most visibly,
inline `cid:` images stay unresolved links. The `.eml` is the fidelity copy, and
any feature request of the form "make markdown byte-accurate" is really a
request to read the `.eml`.

**Read-only is structural.** The only scope requested is `gmail.readonly`. There
is no write path to remove, because there is no write path.

## Testing the seams

`internal/provider.Fake` replays canned `RawMessage` values and can simulate a
fetch dying partway through with partial state. It is how the pipeline gets
tested end to end with no network:

```go
p := provider.NewFake("gmail", msgs...)
```

The Gmail provider itself is tested against an `httptest` server holding canned
JSON, via `gmail.NewWithHTTPClient(ctx, account, srv.Client(), srv.URL, opts)`.
No test in this repo touches the network. See [CONTRIBUTING.md](../CONTRIBUTING.md).
