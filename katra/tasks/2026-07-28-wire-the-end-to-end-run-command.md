---
title: Wire the end-to-end run command
date: "2026-07-28"
time: "15:35:28"
tags:
    - pipeline
summary: 'run: fetch -> parse -> evaluate -> sinks, dry-run mode, run summary, exit codes'
type: task
status: todo
effort: M
epic: run-modes-and-operations
---

## Context
First task where the whole pipeline exists: everything before this merges seams; this makes `mail-muncher run` real. Depends on all `now` epics.

## Spec
In `internal/pipeline`:
- `Cycle(ctx, cfg)` per account: load state → refresh domain-list files (`BeginCycle`) → `provider.Fetch` streaming each `RawMessage` → parse to `model.Message` (parse failure: log, count, skip) → `engine.Evaluate` → if matched, run each sink in `rule.formats` → after a successful fetch, save state. Message-level errors never abort the cycle; provider/auth errors do.
- Seen-ID set updated per stored message; state saved even when 0 messages matched (historyId still advances).
- `--dry-run`: full fetch+evaluate but sinks report what WOULD be written (`would store <path>`); state is NOT saved.
- Run summary logged at end (and printed): `fetched=N matched=N stored=N skipped=N parse_errors=N sink_errors=N duration=…` per account.
- Exit codes: 0 success (even with message-level errors), 1 config/validation error, 2 provider/auth failure. Cron cares about this.
- Logging: `log/slog`, text handler, `--log-level` persistent flag (default info). Debug level logs per-message evaluate decisions (rule name or "no match").

## Acceptance
- Integration-style test: fake Provider (canned RawMessages) + temp dirs + real filter/sinks → assert files on disk, state file contents, summary counts, dry-run writes nothing.
