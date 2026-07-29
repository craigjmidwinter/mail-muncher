---
title: The run manifest
layout: default
nav_order: 6
description: >-
  The machine-readable JSON record mail-muncher writes for every cycle. Field
  names an agent codes against — stored, skipped, quarantined and summary.
---

# The run manifest

`run --json` and `daemon --json` write a machine-readable record of every cycle
to stdout. This page is the contract: an agent codes against these field names.

- [Why it exists](#why-it-exists)
- [stdout is data, stderr is logs](#stdout-is-data-stderr-is-logs)
- [A complete manifest](#a-complete-manifest)
- [Manifest fields](#manifest-fields)
- [`stored` and `skipped` entries](#stored-and-skipped-entries)
- [`quarantined` entries](#quarantined-entries)
- [`degraded_files` entries](#degraded_files-entries)
- [`summary`](#summary)
- [The daemon stream](#the-daemon-stream)
- [Reading it with jq](#reading-it-with-jq)
- [Exit status alongside the manifest](#exit-status-alongside-the-manifest)
- [The same shape over MCP](#the-same-shape-over-mcp)

## Why it exists

Without `--json` the contract is the directory: mail arrives as files, and a
consumer discovers it by walking the tree. That still works and is still the
default.

The manifest is the other half. It says *what this cycle did* — which paths it
created, which were already there, which messages it could not deliver, and
whether the cursor advanced — so a caller does not have to diff a directory to
find out. One JSON object per configured account, per cycle.

```bash
mail-muncher run --json 2>/dev/null | jq -r '.stored[].path'
```

That is the whole integration for "run a cycle, tell me the new files".

## stdout is data, stderr is logs

Every human-facing message — warnings, per-message decisions, errors, the
daemon's lifecycle log — goes to stderr through `log/slog`. stdout carries
manifests and nothing else.

So `2>/dev/null` leaves pure JSON, and `--log-level debug` is safe to leave on
while parsing stdout:

```bash
mail-muncher run --json --log-level debug 2>run.log | jq .
```

Without `--json`, stdout carries one human summary line per account instead
(see [the summary line](#the-human-summary-line)).

## A complete manifest

One object, pretty-printed here; on the wire it is a single line followed by a
newline.

```json
{
  "account": "personal",
  "started_at": "2026-07-28T09:15:00.512Z",
  "duration_ms": 1843,
  "stored": [
    {
      "path": "/Users/you/Mail/job-search/2026/07/1785230100-a00d5c5e383a1c08-re-your-application.eml",
      "format": "eml",
      "rule": "job-search",
      "id": "18fe9c0d1a2b3c4d",
      "message_id": "<abc123@acme.com>",
      "thread_id": "18fe9c0d1a2b3c4d",
      "thread_id_source": "provider",
      "from": "jane@acme.com",
      "subject": "Re: Your application for Senior Engineer",
      "date": "2026-07-28T09:15:00Z",
      "has_attachment": true
    },
    {
      "path": "/Users/you/Mail/job-search/2026/07/1785230100-a00d5c5e383a1c08-re-your-application.md",
      "format": "markdown",
      "rule": "job-search",
      "id": "18fe9c0d1a2b3c4d",
      "message_id": "<abc123@acme.com>",
      "thread_id": "18fe9c0d1a2b3c4d",
      "thread_id_source": "provider",
      "from": "jane@acme.com",
      "subject": "Re: Your application for Senior Engineer",
      "date": "2026-07-28T09:15:00Z",
      "has_attachment": true
    }
  ],
  "skipped": [],
  "summary": {
    "fetched": 42,
    "matched": 1,
    "stored": 2,
    "skipped": 0,
    "parse_errors": 0,
    "sink_errors": 0,
    "quarantined": 0
  }
}
```

Note that one message with `formats: [eml, markdown]` produces **two** entries
sharing an `id`, a `thread_id`, and a `subject`. Entries are renderings, not
messages.

## Manifest fields

| Field | Type | Always present | Meaning |
| --- | --- | --- | --- |
| `account` | string | yes | The configured account this cycle covered. |
| `started_at` | RFC3339 UTC | yes | When this account's cycle began. |
| `duration_ms` | integer | yes | How long this account's cycle took. |
| `dry_run` | bool | only when true | Nothing was written. Every path in `stored` is a path that *would* have been written, and no state was saved. |
| `stored` | array of [entry](#stored-and-skipped-entries) | yes (may be `[]`) | One entry per (message × format) this cycle wrote. |
| `skipped` | array of [entry](#stored-and-skipped-entries) | yes (may be `[]`) | One entry per (message × format) already on disk, so nothing was written. These files exist and are safe to read. |
| `quarantined` | array of [quarantine entry](#quarantined-entries) | only when non-empty | Messages parked under the quarantine directory instead of delivered. |
| `degraded` | bool | only when true | Some `from_domains_file` could not be read, so "no match" is not a trustworthy verdict for any message this cycle. |
| `degraded_files` | array of [degraded file](#degraded_files-entries) | only when non-empty | Which lists, and why. |
| `state_held` | bool | only when true | The sync cursor was deliberately not saved, so this mail is fetched and re-evaluated next cycle. Set by `on_degraded_filter: hold`. |
| `stopped` | bool | only when true | The cycle ended early because a shutdown was requested. The message in flight finished, the state reached was saved, the rest arrives next run. |
| `summary` | [summary](#summary) | yes | Counters for the cycle. |
| `error` | string | only when non-empty | The failure that ended the cycle early. `stored` and `skipped` are still authoritative for the work completed before it. |

Fields marked "only when true" or "only when non-empty" are **absent** from the
JSON, not `false` or `null`. Read them with a default:
`.state_held // false`.

A manifest is always complete enough to act on. A cycle that dies partway
through still lists everything that reached disk before the failure, and carries
the failure in `error`.

## `stored` and `skipped` entries

Both arrays hold the same shape. Every field is populated for both, so an agent
that finds a message under `skipped` can read its subject and sender without
opening the file.

| Field | Type | Always present | Meaning |
| --- | --- | --- | --- |
| `path` | string | yes | The destination file, as configured (absolute if `dest` was absolute). |
| `format` | `"eml"` \| `"markdown"` | yes | The rendering written there. |
| `rule` | string | yes | Name of the rule that claimed the message. |
| `id` | string | yes | Provider-scoped message id. Entries for the same message in different formats share it. |
| `message_id` | string | when the header exists | The RFC822 `Message-ID`, angle brackets included. |
| `thread_id` | string | yes, never empty | The conversation this message belongs to. |
| `thread_id_source` | `"provider"` \| `"references"` \| `"in_reply_to"` \| `"self"` | yes | Where `thread_id` came from. |
| `from` | string | when there is a `From` address | First `From` address, bare addr-spec, no display name. |
| `subject` | string | yes (may be `""`) | RFC 2047-decoded `Subject`. |
| `date` | RFC3339 UTC | yes | The `Date` header, or the provider's internal date when the header was missing or unparseable. |
| `has_attachment` | bool | yes | Whether the message carries a non-inline part. |

### Grouping a cycle by thread

`thread_id` is never empty, so grouping needs no special cases:

```bash
mail-muncher run --json 2>/dev/null \
  | jq -r '.stored[] | select(.format=="markdown") | "\(.thread_id)\t\(.subject)"' \
  | sort
```

`thread_id_source` says how much to trust that grouping:

| Value | Meaning |
| --- | --- |
| `provider` | The mail provider grouped the thread itself (Gmail's `threadId`). Authoritative. |
| `references` | Reconstructed from the root of the `References` chain. The usual case for a reply from a non-Gmail source. |
| `in_reply_to` | Reconstructed from `In-Reply-To` because there was no usable `References`. Keys on the parent rather than the root, so a deep chain from a mailer that omits `References` fragments. |
| `self` | The message carries no threading headers at all. A thread of one, keyed by its own `Message-ID`. |

Anything other than `provider` is best-effort: a mailer that breaks the
`References` chain splits a thread. An agent that needs a guarantee should check
this field before treating a grouping as complete.

## `quarantined` entries

Present when `on_message_failure: quarantine` (the default) had to act. The
message would not parse, or every rendering its rule asked for failed to write.
The raw bytes were written under the quarantine directory and the cursor
advanced past the message — so quarantine is where it lives now, and nothing
will re-fetch it.

| Field | Type | Always present | Meaning |
| --- | --- | --- | --- |
| `path` | string | yes | The `.eml` holding the raw message. A `.json` sidecar sits beside it. |
| `id` | string | yes | Provider-scoped message id. |
| `rule` | string | when a rule claimed it | Empty for a message that would not even parse. |
| `reason` | `"parse"` \| `"sink"` | yes | The stage that failed. |
| `error` | string | yes | The failure text. |
| `quarantined_at` | RFC3339 UTC | yes | When it was parked. |

A non-zero `summary.quarantined` is not a failed run — the process still exits
0 — but it is always mail a human has to look at. Alert on it.

See [configuration.md](configuration.md#on_message_failure) for the policy and
the on-disk layout.

## `degraded_files` entries

| Field | Type | Meaning |
| --- | --- | --- |
| `path` | string | The `from_domains_file` that could not be read. |
| `error` | string | Why: missing, unreadable, or truncated partway through. |

`degraded` is the boolean an agent branches on; `degraded_files` is the detail.
Under the default policy (`on_degraded_filter: hold`) a degraded cycle also sets
`state_held`, stores everything that *did* match, and returns exit 0 — the same
mail is re-evaluated next cycle once the file is back.

```bash
# Fail a wrapper script when the subscription file went missing.
mail-muncher run --json 2>/dev/null | jq -e 'select(.degraded == true) | halt_error(1)'
```

## `summary`

Every key is always present.

| Key | Counts | Meaning |
| --- | --- | --- |
| `fetched` | messages | Delivered by the provider this cycle. |
| `matched` | messages | Claimed by some rule. |
| `stored` | renderings | Actually written (or, under `--dry-run`, that would have been). |
| `skipped` | renderings | Not written because the destination already existed. |
| `parse_errors` | messages | Would not parse; logged and skipped. |
| `sink_errors` | renderings | Write failures; logged and counted, the cycle continues. |
| `quarantined` | messages | Parked under the quarantine directory. |

The counters work at two granularities on purpose:

- `fetched`, `matched`, `parse_errors` and `quarantined` count **messages**.
- `stored`, `skipped` and `sink_errors` count **renderings** — (message ×
  format) pairs. A rule with `formats: [eml, markdown]` contributes two.

So `summary.stored == (.stored | length)` and
`summary.skipped == (.skipped | length)`, always. Messages no rule claimed are
`fetched - matched - parse_errors`; they are not counted as `skipped`, because
"skipped" is about writing, not about matching.

`quarantined` deliberately overlaps `parse_errors` and `sink_errors`: those
count what went wrong, this counts what was done about it.

Two shapes worth recognising:

- `fetched=0` — a steady-state cron run. Nothing new since the last cursor.
- `matched=N stored=0 skipped=N` — a re-run over the same window. That is
  idempotency working.

## The human summary line

Without `--json`, stdout gets one line per account instead:

```
personal: fetched=42 matched=3 stored=3 skipped=39 parse_errors=0 sink_errors=0 quarantined=0 duration=1.84s
```

The field list runs contiguously from `fetched=` to `duration=`, so anything
unusual about the cycle is marked on the account label rather than spliced into
the middle:

```
personal (dry-run): fetched=42 ...
personal (degraded, state held): fetched=42 ...
personal (stopped): fetched=12 ...
```

The labels are `dry-run`, `degraded`, `state held` and `stopped`, in that order,
comma-separated inside one set of parentheses. They correspond exactly to the
`dry_run`, `degraded`, `state_held` and `stopped` manifest fields.

## The daemon stream

`daemon --json` writes the same objects, newline-delimited, forever: one line
per account per tick. It is a stream, not a document — there is no enclosing
array and no terminator.

```bash
mail-muncher daemon --json --interval 5m 2>daemon.log \
  | while read -r line; do
      printf '%s' "$line" | jq -r '.stored[]?.path'
    done
```

Use a streaming parser (`jq` without `-s`, `json.loads` per line,
`encoding/json.Decoder`). Buffering until EOF will wait forever.

`daemon --dry-run --json` works too, and is the honest way to watch what a
polling loop *would* do without writing anything.

## Exit status alongside the manifest

The manifest is written **before** the exit status is decided, so a cycle that
failed partway still reports the files it created.

| Code | Meaning | Manifest still written |
| --- | --- | --- |
| 0 | The cycle completed. Message-level failures are reported in the manifest, not escalated. | yes |
| 1 | Config or validation error. Nothing was fetched. | no — the failure precedes the cycle |
| 2 | Provider or auth failure. | yes, with `error` set |
| 3 | Another instance holds the cycle lock. | no — nothing ran |

`daemon` never exits 2: a failing cycle is logged with a consecutive-failure
count and retried on the next tick. It exits 0 on SIGINT or SIGTERM, 1 on a
config error, and 3 when another instance already held the instance or cycle
lock at startup.

## The same shape over MCP

The `sync` tool of [`mail-muncher mcp`](mcp.md) returns these same manifests:

```json
{
  "dry_run": false,
  "manifests": [ { "account": "personal", "...": "..." } ],
  "error": "..."
}
```

One `Manifest` per configured account, in config order, with the cycle's failure
(if any) in the outer `error`. There is exactly one manifest shape in this
project, and all three entry points emit it.
