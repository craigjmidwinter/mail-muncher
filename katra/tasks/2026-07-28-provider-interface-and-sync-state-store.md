---
title: Provider interface and sync state store
date: "2026-07-28"
time: "15:34:11"
tags:
    - architecture
summary: Provider contract yielding raw RFC822 + JSON sync-state persistence
type: task
status: done
effort: S
epic: gmail-provider
---

## Context
The seam that keeps mail-muncher provider-agnostic: providers stream raw messages plus opaque-ish sync state; the pipeline never sees a provider SDK type.

## Spec
In `internal/provider`:

```go
type RawMessage struct {
    ID           string    // provider-scoped stable id
    Raw          []byte    // RFC822
    InternalDate time.Time
    Labels       []string
}

type SyncState struct {         // JSON-serializable
    HistoryID    uint64            `json:"history_id,omitempty"`    // Gmail
    LastSyncTime time.Time         `json:"last_sync_time"`
    Extra        map[string]string `json:"extra,omitempty"`         // future providers
}

type Provider interface {
    Name() string
    // Fetch streams messages newer than state via fn; returns updated state.
    // fn returning an error aborts the fetch (state up to that point is returned).
    Fetch(ctx context.Context, state SyncState, fn func(RawMessage) error) (SyncState, error)
}
```

In `internal/state`: a store keyed by account name, one JSON file per account at `<state_dir>/<account>.json`. `Load(account) (SyncState, error)` (zero value if missing), `Save(account, SyncState) error` — write to temp file + rename (atomic). Also persist a bounded recently-seen ID set (`seen_ids`, cap ~2000, FIFO) for dedup belt-and-braces alongside the sink's idempotent filenames.

## Acceptance
- Unit tests for state round-trip, missing file → zero value, atomic write (no partial file on simulated failure), seen-ID cap eviction.
