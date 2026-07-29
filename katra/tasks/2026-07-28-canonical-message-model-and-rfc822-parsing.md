---
title: Canonical message model and RFC822 parsing
date: "2026-07-28"
time: "15:33:35"
tags:
    - parsing
summary: Parse raw RFC822 into a provider-neutral Message via enmime
type: task
status: todo
effort: M
epic: core-skeleton-and-config
---

## Context
Providers hand the pipeline raw RFC822 bytes. Everything downstream (filters, markdown sink) works off one canonical parsed struct, so filtering logic never touches provider APIs.

## Spec
In `internal/model`:

```go
type Message struct {
    ID          string   // provider-scoped stable id (Gmail message id)
    Account     string
    Raw         []byte   // full RFC822, exactly as fetched
    From        []mail.Address
    To, Cc      []mail.Address
    Subject     string
    Date        time.Time    // Date header; fall back to provider internal date
    MessageID   string       // Message-ID header
    Labels      []string     // provider labels/folders if any
    TextBody    string       // best-effort text/plain part
    HTMLBody    string       // best-effort text/html part
    Attachments []Attachment // filename, content-type, content
}
```

- Parse with `github.com/jhillyerd/enmime` (`enmime.ReadEnvelope`). It handles charset conversion, RFC2047 header decoding, and multipart traversal.
- `Parse(id, account string, raw []byte, internalDate time.Time, labels []string) (*Message, error)`.
- Malformed messages must not kill a run: on parse failure return the error; callers log and skip (raw bytes can still be stored by a future "quarantine" feature — out of scope, just don't panic).
- Helper methods used by the filter engine: `FromDomains() []string` (lowercased domain of each From address), `HasAttachment() bool`.

## Acceptance
- Unit tests with fixture .eml files in `testdata/`: plain text, multipart HTML+text, message with attachment, RFC2047-encoded subject, broken/truncated message (error path). Construct fixtures by hand or with enmime's builder.
