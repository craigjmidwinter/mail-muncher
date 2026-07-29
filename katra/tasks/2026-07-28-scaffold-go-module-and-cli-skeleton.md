---
title: Scaffold Go module and CLI skeleton
date: "2026-07-28"
time: "15:33:35"
tags:
    - go
    - cli
summary: go.mod, cobra commands (run/daemon/auth/validate), Makefile, package layout
type: task
status: todo
effort: S
epic: core-skeleton-and-config
---

## Context
mail-muncher is a small Go tool that pulls mail from a provider, evaluates each message against configurable rules, and writes matches to the filesystem. This task creates the repo skeleton every other task builds on.

## Spec
- `go mod init github.com/craigjmidwinter/mail-muncher` (Go 1.22+).
- CLI with `github.com/spf13/cobra`. Root command `mail-muncher` with a persistent `--config` flag (default `~/.config/mail-muncher/config.yml`) and `--version`.
- Subcommand stubs (each prints "not implemented" and exits 1 for now):
  - `run` — one-shot fetch/filter/store cycle (cron entrypoint). Flags: `--dry-run`.
  - `daemon` — run repeatedly on an interval.
  - `auth` — interactive provider authentication.
  - `validate` — parse config, resolve referenced files, report problems.
- Package layout (create the directories with a `doc.go` each):
  - `cmd/mail-muncher/` — main + cobra wiring only
  - `internal/config/` — config loading (next task)
  - `internal/model/` — canonical message model
  - `internal/provider/` — provider interface + implementations
  - `internal/filter/` — rule engine
  - `internal/sink/` — eml/markdown writers
  - `internal/state/` — sync state store
  - `internal/pipeline/` — orchestrates fetch → filter → sink
- `Makefile` with `build`, `test`, `lint` (golangci-lint if available, else `go vet`).
- `.gitignore` for the binary and local scratch.

## Acceptance
- `make build` produces a working binary; `mail-muncher --help` lists the four subcommands.
- `go test ./...` passes (empty packages are fine).
