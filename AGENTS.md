# AGENTS.md

Working notes for coding agents. Conventions and invariants only — the README
is the user-facing documentation and is not repeated here.

## What this is

mail-muncher gives a program its own read-only mailbox. It archives mail
matching user-written rules to `.eml` and markdown files on disk, and serves
that archive to agents over MCP. Go, no cgo, single binary. Two providers
(Gmail API, IMAP) sit behind one interface; a pipeline fetches, parses,
evaluates a match tree, and writes through sinks.

## Commands

Go 1.25+. No code generation, no build tags, no other toolchain.

```bash
make build   # go build -o ./mail-muncher, version stamped from git describe
make test    # go test ./...
make lint    # golangci-lint run if installed, else go vet ./...
make fmt     # go fmt ./...
make tidy    # go mod tidy
```

These are green on a clean checkout. If they are not, that is a bug worth
reporting rather than working around.

**`golangci-lint` is optional and CI does not run it.** Without the binary,
`make lint` prints `golangci-lint not found; falling back to go vet` and runs
`go vet`. The full set is pinned in `.golangci.yml` and is worth running
locally before any change to error handling, file paths, or anything that
writes to disk — it is not a CI gate because it was measured at 51 minutes on
a GitHub runner. CI runs `gofmt -l .`, `go mod tidy` verification, `go build`,
`go vet`, and `go test -race`.

## Invariants a change must not break

These are the product, not preferences. A change that breaks one is wrong even
if it compiles and the suite passes.

- **No new OAuth scope.** `internal/provider/gmail/auth.go` declares exactly
  one — `gmail.readonly` — and it is the only scope constant in the tree.
- **Nothing that can send, delete, or modify mail.** No Gmail
  `Modify`/`Trash`/`Delete`/`Send`/`Insert`/`BatchModify`; no IMAP
  `STORE`/`EXPUNGE`/`APPEND`/`MOVE`/`COPY`. IMAP opens mailboxes with
  `EXAMINE` (`SelectOptions{ReadOnly: true}`) and fetches with `BODY.PEEK[]`
  so reading never sets `\Seen`. This is enforced mechanically by
  `TestProviderSourceNeverCallsAWriteCapableMailboxMethod` in
  `internal/provider/`, which AST-walks provider source and fails on any write
  verb. If that test fires, the change is the problem, not the test.
- **No credentials, tokens, real email addresses, or real message ids in a
  diff.** Fixtures use `example.com`, `acme.example`, and `.test`. The OAuth
  client fixture at `internal/provider/gmail/testdata/credentials.json` is
  deliberately fake and must stay that way.
- **The provider seam holds.** Adding a provider must require zero changes to
  `internal/pipeline`, `internal/filter`, or `internal/sink`. A
  `if provider == "imap"` outside `internal/provider/` means the interface is
  wrong; fix the interface.

## Style

- **`cmd/` holds no behavior.** It parses flags, builds internal types, maps
  errors to exit codes, and prints. Logic belongs in an internal package where
  it is testable without the CLI.
- **`internal/config` imports no other internal package**; `internal/model` is
  provider-neutral and must not reference a provider SDK type.
- **Errors carry the fix, not just the fault.** They name the command to run
  or the value that was wanted: `run mail-muncher auth --account personal`,
  `want a Go duration such as "720h"`, `combine them with all: or any:`. Error
  and empty states are a documented surface; several are asserted verbatim in
  tests and quoted verbatim in the README, so changing one means updating both.
- **Tests are table-driven**, use `stretchr/testify`'s `require`, and are named
  as guarantees rather than as the function under test.
- **No network in tests, ever.** Use `internal/provider.Fake`, or drive the
  Gmail provider against an `httptest` server via `gmail.NewWithHTTPClient`.
- Filesystem tests use `t.TempDir()`. Time is injected (`filter.WithClock`,
  `gmail.FetchOptions.Now`), never slept on.
- Golden files: `go test ./internal/sink/... -update`. Read the diff before
  committing a regenerated one — that is what the golden file is for.
- Comments explain why, at length where the reasoning is not obvious. Match the
  surrounding density rather than stripping it.

## Security notes

- Everything the tool writes is owner-only: files 0600, directories 0700, set
  explicitly rather than left to umask. Keep it that way for anything new.
- `imap.password_cmd` is executed via `/bin/sh -c`. That is deliberate and
  documented; it is also why IMAP cannot work on native Windows.
- Config is decoded with `KnownFields(true)`, so an unknown key is an error
  rather than a silently ignored typo. Do not relax that.
- Mail content is attacker-controlled. Anything deriving a filesystem path from
  a subject, sender, message-id, or attachment name goes through the existing
  sanitizers in `internal/sink`; directory creation refuses to follow symlinks
  and file creation is exclusive. Do not add a path-building route that skips
  them.
- `katra/` is a committed chronicle of the project's planning and decisions. It
  is a historical record, not a spec to keep in sync — do not edit it, and
  where it disagrees with the code, the code is right.
