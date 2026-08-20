---
title: Daemon mode with poll interval and lockfile
date: "2026-07-28"
time: "15:35:28"
tags:
    - daemon
summary: 'mail-muncher daemon --interval 5m: loop, jitter, graceful shutdown, single-instance lock'
type: task
status: done
effort: M
epic: run-modes-and-operations
---

## Context
Same pipeline as `run`, looped. Also protects the cron use case: a lockfile shared by `run` and `daemon` prevents overlapping cycles from racing on state files.

## Spec
- `daemon --interval 5m` (min 30s, default 5m): run a cycle, sleep interval ± up to 10% jitter, repeat. A failing cycle (provider error) logs and waits for the next tick — the daemon does not exit; consecutive-failure count in the summary log.
- Graceful shutdown on SIGINT/SIGTERM: cancel ctx, let the in-flight cycle finish message-in-progress, save state, exit 0.
- Lockfile with `github.com/gofrs/flock` at `<state_dir>/mail-muncher.lock`, taken by BOTH `run` and `daemon` for the duration of each cycle. `run` exits 3 with a clear message if the lock is held (another instance running); document exit code 3 in --help.
- README section: sample crontab line (`*/10 * * * * mail-muncher run …`) and a launchd plist example for macOS (this is the user's platform) under `contrib/`.

## Acceptance
- Tests: lock contention (second instance bails with code 3), SIGTERM during a fake slow cycle exits cleanly with state saved, interval jitter bounds.
