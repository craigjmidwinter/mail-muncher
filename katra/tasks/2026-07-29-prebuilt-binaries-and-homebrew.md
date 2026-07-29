---
title: Prebuilt binaries and Homebrew
date: "2026-07-29"
time: "09:22:00"
tags:
    - release
    - adoption
summary: Ship binaries on every release and a brew tap; drop the Go toolchain requirement
type: task
status: todo
effort: M
epic: adoption-and-onboarding
---

## Context
Installing currently requires a Go toolchain — `go install`, or clone plus `make build`. That is a filter, and the people it filters out are exactly the ones most likely to try a small tool on a whim. CI already cross-compiles four targets on every push; those artifacts are thrown away.

## Spec
- **goreleaser** (or an equivalent release workflow) attaching archives for darwin/amd64, darwin/arm64, linux/amd64, linux/arm64 to every tagged release. Reuse the version stamping the Makefile already does via `git describe`, so `mail-muncher --version` reports the release rather than `dev` — the current `go install` build reports `dev`, which is a real papercut when triaging a bug report.
- **Checksums file, and signed releases.** For a tool that reads mail, "verify what you downloaded" is proportionate. Prefer keyless signing (cosign/OIDC) over managing a long-lived key.
- **A Homebrew tap** — `brew install craigjmidwinter/tap/mail-muncher`. This needs a second repository (`homebrew-tap`) and a formula updated by the release workflow. Note the tap repo and any token it needs are the repo owner's to create.
- **README install section rewritten** with brew first, binary download second, `go install` third, build-from-source last. Current ordering optimizes for contributors over users.
- Keep `go install` working and documented — it is the right path for Go developers and costs nothing to retain.

## Acceptance
- A tagged release produces downloadable archives plus checksums for all four targets, and `--version` reports the tag.
- A clean machine with no Go toolchain can install and run `mail-muncher --help`.
- Signature verification instructions in the README are accurate — verify them by actually running the commands.
