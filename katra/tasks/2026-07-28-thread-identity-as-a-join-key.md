---
title: Thread identity as a join key
date: "2026-07-28"
time: "16:40:00"
tags:
    - agents
    - parsing
    - threading
summary: Carry provider thread id through to frontmatter, with a References-derived fallback
type: task
status: done
effort: M
epic: agent-interface
---

## Context
A hiring process is a **thread**, not a message. A consumer asking "everything about the Acme process" needs a join key, and today the frontmatter carries `message_id` only. Reconstructing threads from `In-Reply-To`/`References` across a directory of `.eml` files is precisely the MIME drudgery this tool exists to absorb — every consumer would otherwise reimplement it.

Cheap now, painful to retrofit: every message already stored would need re-parsing, and the idempotent filenames mean a rewrite is not a re-run.

## Spec

### Provider seam
- Add `ThreadID string` to `provider.RawMessage`. Purely additive.
- Gmail: `users.messages.get` already returns `threadId` on the same response as `raw` — populate it, no extra API call. History-path downloads go through the same RAW get, so both paths get it free.
- IMAP (later): leaves it empty; the fallback below covers it.

### Model
- `model.Message` gains:
  - `ThreadID string` — provider-native when available, else synthesized (below)
  - `InReplyTo string` — the `In-Reply-To` header, angle brackets included
  - `References []string` — parsed `References` chain, oldest first
- **Do not change `model.Parse`'s existing signature** — `internal/pipeline` is being written against it right now. Add a variadic option instead:
  ```go
  func Parse(id, account string, raw []byte, internalDate time.Time, labels []string, opts ...ParseOption) (*Message, error)
  func WithThreadID(id string) ParseOption
  ```
- **Fallback synthesis**, when the provider supplies no thread id: use the root of the reference chain — `References[0]`, else `InReplyTo`, else the message's own `Message-ID`. A message with no threading headers is a thread of one, keyed by itself. This makes `ThreadID` non-empty for every message, so consumers can group unconditionally with no special cases.
- Mark synthesized ids distinguishably (e.g. `ThreadIDSource` field, or a documented prefix) so a consumer can tell a Gmail thread from a reconstructed one. Reconstructed threads are best-effort: mailers that break the `References` chain will split a thread.

### Sinks
- Markdown frontmatter gains `thread_id` (always) and `in_reply_to` (omit when empty). Do NOT add the full `references` chain — it is unbounded and the `.eml` has it.
- Regenerate the golden files (`go test ./internal/sink/... -update`) and eyeball the diff.

### Manifest
- `pipeline.Manifest` stored/skipped entries gain `thread_id`. An agent consuming the manifest can then group a cycle's deliveries by thread without opening a file.

## Acceptance
- Unit tests: Gmail-supplied thread id wins over synthesis; synthesis from `References[0]`; from `In-Reply-To` when `References` is absent; self-keyed when neither; malformed/garbage reference headers do not error.
- Fixture: a 3-message reply chain parses to one shared `ThreadID`.
- Golden-file update reviewed, manifest shape test updated.

## Note
Touches five packages that are otherwise complete (`provider`, `provider/gmail`, `model`, `sink`, `pipeline`). Land it as ONE change with the whole repo green — not five partial ones.
