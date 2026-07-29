---
title: Gmail API over IMAP for the first provider
date: "2026-07-28"
time: "15:33:03"
tags:
    - gmail
    - architecture
summary: OAuth + history-based incremental sync + RAW fetch beats app-password IMAP; IMAP stays a later epic
type: decision
status: accepted
---

Two routes into Gmail:

- **IMAP** (go-imap/v2 + app password): generic — the same code path would work for any provider — but Gmail-over-IMAP needs a 2FA app password, has label/All-Mail duplication quirks, and incremental sync means hand-rolled UID bookkeeping per folder.
- **Gmail API** (google.golang.org/api/gmail/v1 + OAuth): needs a one-time GCP OAuth client setup, but gives `users.messages.get format=RAW` (a clean RFC822 message, so our storage/parsing layer is provider-neutral anyway), labels as data, and `users.history.list` for cheap incremental sync.

Chose the **Gmail API**. The provider interface hands the pipeline raw RFC822 bytes either way, so IMAP remains a clean second implementation (kept as a `later` epic) — this is not a one-way door.
