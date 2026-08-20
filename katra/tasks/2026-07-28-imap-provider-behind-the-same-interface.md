---
title: IMAP provider behind the same interface
date: "2026-07-28"
time: "15:35:28"
tags:
    - imap
summary: 'go-imap/v2 implementation: UID-based incremental fetch, any-provider config block'
type: task
status: done
effort: L
epic: imap-provider
---

## Context
Later-horizon flexibility payoff: any IMAP mailbox (Fastmail, a work account, self-hosted) through the exact same pipeline. No pipeline/filter/sink changes should be needed — that is the test of the provider seam.

## Spec (sketch — refine when scheduled)
- `provider: imap` config block: `host`, `port` (default 993), `username`, `password_cmd` (run a command to get the secret — never a plaintext password key), `mailboxes: [INBOX]` (list), `tls: true`.
- `github.com/emersion/go-imap/v2` + `imapclient`. Per mailbox: track `uidvalidity` + `last_uid` in `SyncState.Extra`; on uidvalidity change, resync from `initial_lookback`. Fetch `UID FETCH <last+1>:* (BODY.PEEK[] INTERNALDATE FLAGS)`; read-only (PEEK — never set \\Seen).
- Message ID for dedup/basename: `<account>:<mailbox>:<uidvalidity>:<uid>`.
- Mailbox name doubles as the `label` predicate value.
- Reuse the backoff helper from the gmail provider (extract to `internal/provider/retry.go` if not already shared).

## Acceptance
- Unit tests against go-imap's in-memory/mock server if practical, else an interface-level fake; uidvalidity-change resync covered.
- examples/ gains an imap config; README provider table updated.
