# Contributing

Thanks for looking. This is a small Go project with a deliberately shallow
dependency graph, and the conventions below are most of what you need to send a
PR that lands.

## Get set up

Requires Go 1.25 or newer. No other toolchain, no code generation, no build
tags.

```bash
git clone https://github.com/craigjmidwinter/mail-muncher
cd mail-muncher
make build          # -> ./mail-muncher
make test           # go test ./...
make lint
```

That should be green on a clean checkout. If it is not, that is a bug — open an
issue.

| Target | What it runs |
| --- | --- |
| `make build` | `go build` into `./mail-muncher`, stamping the version from `git describe`. |
| `make test` | `go test ./...` |
| `make lint` | `golangci-lint run` **if it is installed**, otherwise `go vet ./...` |
| `make fmt` | `go fmt ./...` |
| `make tidy` | `go mod tidy` |
| `make clean` | Removes the binary. |

**`golangci-lint` is optional locally and enforced in CI.** If you do not have
it, `make lint` prints `golangci-lint not found; falling back to go vet` and
runs `go vet` — that fallback is still supported, and you can work all day
without installing anything. But CI's `build` job runs the full set on every
pull request, so a finding you never saw locally can still fail your PR. If
you would rather find out before pushing:

```bash
brew install golangci-lint   # or see golangci-lint.run/welcome/install
```

The set is pinned in `.golangci.yml`, which `golangci-lint run` discovers on
its own — local and CI lint the same rules, with nothing to keep in sync. CI
pins the binary to the version that file was written against; if your local
version is newer and reports something CI does not, that is worth an issue
rather than a silent `//nolint`.

Every `//nolint` in this repo carries a reason on the same line. Add yours the
same way, or fix the finding. `go vet` and `gofmt` remain the floor: both are
hard-failing CI steps of their own.

Run these before you push:

```bash
make fmt
make lint
make test
```

## Repository layout

```
cmd/mail-muncher/        cobra command tree, flags, exit codes — no behavior
internal/
  config/                YAML schema, defaults, path expansion, validation
  model/                 canonical Message + RFC822 parsing
  provider/              Provider interface, RawMessage, SyncState, retry, Fake
    gmail/               OAuth, token cache, list/history/RAW fetch, labels
  filter/                match-tree compilation, Engine, domain-file cache
  sink/                  on-disk layout, .eml and markdown writers
  state/                 per-account sync state files, cycle lockfile
  pipeline/              one cycle: fetch → parse → evaluate → sink → save
  mcpserver/             stdio MCP server over the stored archive
docs/                    user-facing documentation
examples/                runnable configs, verified against `validate`
contrib/                 launchd plist, crontab sample
katra/                   the project's planning chronicle — do not edit
```

`docs/architecture.md` has a "where does my change go?" table. Start there if
you are not sure which package owns the thing you want to change.

Two directories with rules attached:

- **`katra/`** is a hand-authored chronicle of the project's planning: design
  entries, decision records, epics, task specs. It is a historical record, not
  a spec to keep in sync. Do not edit it, and be aware that where it disagrees
  with the code, **the code is right**.

## Package boundaries

The dependency graph is acyclic and shallow, and keeping it that way is most of
the design.

- **`cmd/` holds no behavior.** It parses flags, builds internal types, maps
  errors to exit codes, and prints. If you find yourself writing logic in a
  `RunE`, it belongs in an internal package where it can be tested without the
  CLI.
- **`internal/config` depends on no other internal package.** It knows YAML and
  paths. It does not know what a message is.
- **`internal/model` is provider-neutral.** Nothing in it may reference a
  provider SDK type. Filters and sinks work off `model.Message` and nothing else.
- **The provider seam is the architectural invariant.** Adding a provider must
  require **zero** changes to `internal/pipeline`, `internal/filter`, or
  `internal/sink`. If your provider needs a pipeline change, the interface is
  wrong — fix the interface, do not special-case downstream. A PR that adds
  `if provider == "imap"` anywhere outside `internal/provider/` will be asked to
  move it.
- **Errors carry the fix, not just the fault.** The house style is an error that
  tells the user what to do: `run \`mail-muncher auth --account personal\``,
  `want a Go duration such as "720h"`, `combine them with all: or any:`. Match
  it.

## Testing

`go test ./...` is the whole story. Some conventions the existing tests hold to:

**Table-driven, always.** Named cases in a slice, one subtest each:

```go
tests := []struct {
	name    string
	input   string
	want    string
	wantErr string
}{
	{name: "strips leading at", input: "@Example.COM", want: "example.com"},
	// ...
}
for _, tt := range tests {
	t.Run(tt.name, func(t *testing.T) {
		// ...
	})
}
```

**No network. Ever.** A test that talks to the internet is not accepted. Two
established ways around it:

- `internal/provider.Fake` replays canned `RawMessage` values and can simulate a
  fetch failing partway through with partial state returned.
- The Gmail provider takes an injected HTTP client and endpoint, so tests drive
  it against an `httptest` server holding canned JSON, with no OAuth:

  ```go
  srv := httptest.NewServer(handler)
  p, err := gmail.NewWithHTTPClient(ctx, "work", srv.Client(), srv.URL, gmail.FetchOptions{})
  ```

**Fixtures live in `testdata/`.** Real `.eml` files for parser tests
(`internal/model/testdata/`), a real domain list
(`internal/filter/testdata/domains.txt`), OAuth client JSON good and bad
(`internal/provider/gmail/testdata/`), a real config
(`internal/config/testdata/config.yml`). Add fixtures rather than building
elaborate strings inline — a broken message fixture is worth more than a clever
constructor.

**Golden files for anything rendered.** The markdown sink compares against
`internal/sink/testdata/golden/*.md`. Regenerate them, review the diff, and
commit it:

```bash
go test ./internal/sink/... -update
```

Never regenerate a golden file to make a failing test pass without reading what
changed. That is what the golden file is for.

**Filesystem tests use `t.TempDir()`.** No writing into the repo, no shared
fixture directories, no cleanup code.

**Time is injected, not slept on.** `filter.WithClock` and
`gmail.FetchOptions.Now` exist so age predicates and sync timestamps are
testable. If you need "now" in new code, take it as a field or an option.

**Test the error text you promise.** Where an error message is part of the user
interface — and in this project most of them are — assert on it. `testify`
(`require`) is available and used throughout.

## Documentation

Documentation that describes behavior the code does not have is worse than no
documentation. If you change any of these, update the docs in the same PR:

| You changed | Update |
| --- | --- |
| A config key, default, or validation rule | `docs/configuration.md` and the README's config table |
| A match combinator or predicate | `internal/filter/doc.go`, `docs/filters.md`, and the README's filter table |
| The on-disk layout or a sink's output | `internal/sink/doc.go` and the README's layout section |
| A CLI flag, command, or exit code | The README's commands section |
| A Gmail error message a user can hit | The troubleshooting section of `docs/gmail-setup.md` |
| Anything about the seams | `docs/architecture.md` |

**Every example config must pass `mail-muncher validate`.** This is a hard
requirement, not a nicety:

```bash
make build
for f in examples/*.yml; do ./mail-muncher validate --config "$f" || exit 1; done
```

Warnings are expected — the files an example points at (OAuth credentials, a
domain list another program owns) do not exist in a fresh checkout, and that is
exactly the warning-not-error case the validator is built around. Errors are
not expected. Exit code must be 0.

Package doc comments are load-bearing here. `internal/filter/doc.go` is the
canonical match-tree schema and `internal/sink/doc.go` the canonical layout
description; the user-facing docs are derived from them. Keep the package doc
correct first.

## Pull requests

- **One concern per PR.** A new predicate and a refactor of the sink are two
  PRs.
- **Tests with the change**, in the same commit. New predicate, new table
  entries. New error path, a test that hits it.
- **`make fmt lint test` green** before you push.
- **Say what you verified.** Especially for anything touching Gmail: unit tests
  do not exercise the real API, so if you ran it against a real mailbox, say so
  and paste the (redacted) run summary.
- **No new dependencies without a reason in the PR description.** The current
  set is small and deliberate.

## Reporting a security issue

Do not open a public issue for anything involving credentials, tokens, or the
OAuth flow. Contact the repository owner directly.

Two things worth knowing if you are looking at this area:

- The only OAuth scope requested is `gmail.readonly`. Any change that widens it
  is a design change, not an implementation detail, and needs discussion first.
- Tokens and state are written 0600 in directories created 0700; archived mail
  is 0644/0755 because it is meant to be read by your own tools. Keep the
  distinction.

## License

MIT. See [LICENSE](LICENSE). By contributing you agree your contributions are
licensed under it.
