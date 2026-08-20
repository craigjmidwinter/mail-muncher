---
title: Cap the size of a single fetched message and its attachments
date: "2026-08-20"
time: "16:45:10"
tags:
    - security
    - robustness
summary: 'No ceiling exists today: one oversized message is buffered whole, several times over'
type: task
status: todo
effort: M
epic: run-modes-and-operations
---

Found in the 2026-08-20 standards-pass sweep. Deferred, not fixed, because choosing *where* the ceiling goes is a design decision rather than a one-line patch.

## What is true today
There is no message-size or attachment-size cap anywhere in the codebase. Greps for `MaxSize`, `MaxBytes`, `SizeLimit` and `LimitReader` across `internal/` and `cmd/` return nothing.

- `internal/provider/gmail/fetch.go:390` fetches with `Format("RAW")`; `decodeRaw` at :401 base64-decodes the whole response into one `[]byte`.
- `internal/provider/imap/imap.go:347` `msg.Collect()` buffers the whole `FetchMessageBuffer`; :357 `FindBodySection` returns the complete raw bytes.
- `internal/model/parse.go:83` parses those already-buffered bytes with no `enmime` option bounding part count, nesting depth, or decoded size.
- Decoded attachment bytes live in `model.Attachment.Content []byte` until `internal/sink/markdown.go:165` writes them.

Peak memory for one message is therefore at least two full copies (raw + decoded), unbounded.

## Honest severity
Lower than it first looks, and the task should not oversell it. Gmail caps messages at 25 MB, so the Gmail path tops out near ~100 MB peak on a worst case — unpleasant, not fatal. The unbounded case needs a hostile or broken IMAP server, which is a server the operator configured themselves.

What makes it still worth doing: `daemon` runs unattended, and the blast radius is a killed cycle rather than a bad byte on disk. A cap is cheap insurance for a process nobody is watching.

## Shape of the fix
A configurable ceiling (`max_message_bytes`, with a sane default) enforced at the provider boundary before the decode, so an oversized message is skipped and quarantined with a clear reason rather than OOM-ing the cycle. That routes it through the existing quarantine path, which already exists for 'this message could not be delivered' and already counts into the manifest.

Needs a decision on the default and on whether the cap is per-message, per-attachment, or both.
