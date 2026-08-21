---
title: Verify the Linux and Windows install paths on real machines
date: "2026-08-21"
time: "08:24:18"
tags:
    - ergonomics
    - platforms
    - testing
summary: The 2026-08-21 ergonomics pass could not execute either; both findings are static analysis
type: task
status: todo
effort: S
epic: adoption-and-onboarding
---

The ergonomics checklist box "fresh install + first run performed following only the docs, on each supported platform where feasible" is ticked for **darwin only**. This task is the honest remainder.

## Why it could not be done on 2026-08-21
Both fabric runners were unreachable for the whole pass:

    fabricvm (os=linux)    last runner-health  937 min (~15.6h) before the pass
    windesk  (os=windows)  same state

Two legs independently posted COMMAND jobs (`20260821-125620-7f31b6`, `20260821-130201-a8c124`, `20260821-125506-8d8d81`). Every one reached `job-queued` on the bus and was never claimed. Other sessions' jobs queued to the same runners in the same window also sat unclaimed; chatter on the bus attributes it to a pending human login step. Docker Desktop was tried as a fallback — its backend had been running since 5 Aug but the engine never answered `docker info`, so no container could be started either.

Neither leg fabricated a result, which is the right outcome, but it leaves real claims resting on reading rather than running.

## What is currently believed, and on what evidence
Verified by reading source or config (high confidence, still not executed):
- `CGO_ENABLED=0` in `.goreleaser.yml` → static binaries, no libc dependency. The Alpine-for-`/bin/sh` framing in the Dockerfile is therefore exactly right and complete.
- The archive `name_template` `{{ProjectName}}_{{Version}}_{{Os}}_{{Arch}}` matches the README's `curl` URL character for character, cross-checked against the real v0.4.0 release assets.
- `internal/provider/imap/password.go:60` hands every `password_cmd` to a hardcoded `/bin/sh -c`.
- `cmd/mail-muncher/init.go`'s `defaultPasswordCmd` falls through to `pass` on Windows.
- `internal/provider/gmail/auth.go` already branches to `rundll32` on Windows.

Predicted but untested:
- Wrong-architecture execution prints a bare kernel `Exec format error`. The README now documents this shape; **nobody has seen it print**.
- `install` without sudo prints the GNU wording `install: cannot create regular file '...': Permission denied`. The macOS wording *was* observed and differs (it names a scratch file); the GNU line in the README is inferred from coreutils behaviour, not captured.
- On Windows, `go install` succeeds and the first `run` fails on the missing `/bin/sh`. This is the load-bearing claim behind the new README **Windows** section and it has never been executed.

## What to do when a runner is back
1. Linux: run the documented binary-download install verbatim on a fresh box, time it, and confirm the ≤5-minute claim with a real number rather than an inference.
2. Capture the actual `Exec format error` and GNU `Permission denied` strings and correct the README if either differs from what is written there now.
3. Windows: run `go install`, then `init`, then `run`, and capture the real failure text. If it is not a missing-`/bin/sh` error, the Windows section needs rewriting — it currently states that mechanism as fact.
4. Pull `ghcr.io/craigjmidwinter/mail-muncher` and run the documented `docker run` line verbatim; that channel is the one the README now offers Windows users and it has not been exercised in this pass either.

Correct anything the docs got wrong, and only then tick the box.
