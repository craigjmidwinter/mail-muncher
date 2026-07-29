---
title: Gmail fetch with query, pagination, and RAW download
date: "2026-07-28"
time: "15:34:11"
tags:
    - gmail
summary: users.messages.list + get format=RAW, bounded concurrency, rate-limit backoff
type: task
status: todo
effort: M
epic: gmail-provider
---

## Context
First fetch path (also the fallback when incremental history sync can't be used). Downloads full RFC822 so storage is byte-faithful.

## Spec
In `internal/provider/gmail`:
- Implement `Provider.Fetch` full-scan mode:
  - `users.messages.list` with `q` = account's configured `query` (if any) AND `after:<lastSyncTime as unix>` when state has one. Paginate via `nextPageToken`.
  - For each id not in the seen-set: `users.messages.get` with `format=RAW`; base64url-decode `raw`; build `RawMessage` with `internalDate` (ms epoch) and label ids resolved to names (fetch `users.labels.list` once per run, cache).
- Bounded concurrency for the `get` calls (worker pool, default 4; not configurable yet).
- Backoff: on 403 rate-limit / 429 / 5xx, exponential backoff with jitter, max ~5 tries, then fail the run with a clear error. Respect `ctx` cancellation everywhere.
- On success set `SyncState.HistoryID` from the profile (`users.getProfile`) and `LastSyncTime = now`.
- Construct the service so the HTTP client is injectable — tests use `httptest` with canned JSON; no real network in tests.

## Acceptance
- Unit tests: pagination across 2+ pages, base64url decode correctness (fixture with known bytes), backoff on a canned 429 then success, ctx cancellation stops the worker pool.
