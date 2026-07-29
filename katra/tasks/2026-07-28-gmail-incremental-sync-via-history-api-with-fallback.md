---
title: Gmail incremental sync via history API with fallback
date: "2026-07-28"
time: "15:34:11"
tags:
    - gmail
    - sync
summary: users.history.list from stored historyId; 404 => full-scan fallback
type: task
status: todo
effort: M
epic: gmail-provider
---

## Context
Cron/daemon runs should be cheap: ask Gmail "what changed since historyId X" instead of re-listing. History ids expire (Gmail keeps roughly a week), so the full-scan path from the previous task stays as fallback.

## Spec
In `internal/provider/gmail`, extend `Fetch`:
- If `state.HistoryID > 0`: call `users.history.list` with `startHistoryId`, `historyTypes=messageAdded`, paginate. Collect added message ids (dedupe across pages), then download each via the existing RAW-get worker pool.
- If the account has a configured `query`, history results are NOT query-filtered — apply the query semantics we care about by simply letting the local filter engine decide (document this in a code comment: server query is an optimization for full scans only).
- On HTTP 404 from history.list (expired historyId): log at warn, clear `HistoryID`, and fall through to the full-scan path in the same run.
- Update `state.HistoryID` to the max historyId seen (or profile historyId when falling back).
- First-ever run (zero state): full scan. Honor an optional account setting `gmail.initial_lookback` (e.g. "720h", default 30 days) so a first run doesn't trawl the whole mailbox — add this key to the config schema + validate + README table.

## Acceptance
- Unit tests with canned history JSON: multi-page history, 404 → fallback path invoked, historyId advancing, initial_lookback applied to the first-run query.
