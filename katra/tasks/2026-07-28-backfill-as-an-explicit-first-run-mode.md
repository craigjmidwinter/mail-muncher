---
title: Backfill as an explicit first-run mode
date: "2026-07-28"
time: "16:48:00"
tags:
    - ops
    - jobsearch
    - docs
summary: Name the long-lookback first run as a supported mode; 720h default hides it
type: task
status: todo
effort: S
epic: run-modes-and-operations
---

## Context
The 2026-07-28 email audit was done **by hand**. With `initial_lookback` set wide, the first run rebuilds that evidence base on disk, permanently, as a byproduct of setup — and every future audit becomes a directory listing instead of an afternoon.

Nothing new needs building; `initial_lookback` already does it. The problem is discoverability: the default is `720h` (30 days), which quietly means *"the thing you most want on day one is the thing that does not happen by default."* A capability nobody knows about is not a capability.

## Spec
- **Documentation is the deliverable.** Give backfill a named section in the README and in `docs/configuration.md` under `initial_lookback`, stating plainly that a wide first run is an *intended* use, not an abuse:
  ```yaml
  initial_lookback: 13140h   # ~18 months — first run only
  ```
  and that the value can be dropped back to the default afterwards, because subsequent runs sync incrementally from the stored cursor and ignore it entirely.
- Spell out what to expect on a wide backfill: it is one large full scan, it is rate-limited and retried, and it may take a while. Suggest `--dry-run` first to see the volume before committing.
- **Note the seen-set interaction explicitly**, because it looks alarming and is not: `SyncState.SeenIDs` is capped at 2000 (FIFO), so a backfill of more than 2000 messages evicts early ids. That is harmless — the seen-set is belt-and-braces, and the real idempotency key is the deterministic sink filename plus skip-on-exists. A re-run after eviction re-downloads and then skips at the write. Say so in `docs/architecture.md` so nobody "fixes" it later by unbounding the set.
- Consider (implement only if it stays small) a `run --since <duration|date>` override for a one-off backfill without editing config. If it complicates the state/cursor logic at all, **do not build it** — editing one line of YAML for a once-per-account operation is fine.

## Acceptance
- README and `docs/configuration.md` both name backfill as a supported first-run mode with the 18-month example.
- The seen-set-cap explanation lands in `docs/architecture.md`.
- Verified for real: a wide first run against the live account produces the audit evidence base on disk, and the run after it reports everything `skipped`.
