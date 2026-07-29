# Output: what lands on disk, and the run manifest

Load this when consuming delivered mail, parsing frontmatter, or reading
`run --json` / `daemon --json`.

## Directory layout

Every message is filed under its rule's `dest` by message date, in UTC:

```
~/Mail/job-search/
└── 2026/
    └── 07/
        ├── 1785230100-a00d5c5e-re-your-application-for-senior-engineer.eml
        ├── 1785230100-a00d5c5e-re-your-application-for-senior-engineer.md
        └── 1785230100-a00d5c5e-re-your-application-for-senior-engineer.attachments/
            ├── offer.pdf
            └── R-sum-2026.docx
```

The basename is shared by every format, so a message's renderings sort
together:

```
<unix-seconds>-<sha256(account + ":" + message-id)[:8]>-<subject-slug>
```

- The **timestamp** sorts a directory chronologically.
- The **digest** is the idempotency key. It depends only on the account name and
  the provider message id, so the path is a pure function of message identity.
  Re-run, replay after losing state, crash mid-cycle, or overlap two cron
  invocations: the tree converges and nothing is processed twice.
- The **slug** is cosmetic: the subject lowercased, every character outside
  `[a-z0-9]` collapsed to a single `-`, trimmed, truncated to 40 characters. It
  is ASCII-only, so a subject in a non-Latin script slugs to `no-subject`. Only
  the digest carries identity, so two messages with the same subject never
  collide.

Files are written to a temp file in the destination directory and renamed into
place, so an interrupted cycle leaves neither a partial file nor a temp file.
Directories are 0755 and files 0644.

## The `.eml` file

The raw RFC822 bytes exactly as the provider delivered them. Nothing is
re-encoded, re-wrapped or normalized, so it round-trips through any mail tool
and still verifies against DKIM signatures. **This is the fidelity copy.**
Anything that must be byte-exact comes from here.

## The `.md` file

YAML frontmatter, then the body, then links to any attachments.

```markdown
---
subject: 'Re: Your application for Senior Engineer'
from: Jane Doe <jane@acme.com>
to: [me@example.com]
date: 2026-07-28T09:15:00Z
message_id: <abc123@acme.com>
thread_id: 18f2a9c4d5e6
thread_id_source: provider
account: personal
rule: job-search
labels: [INBOX]
attachments: [offer.pdf]
---

Hi there,

Thanks for applying.

## Attachments

- [offer.pdf](1785230100-a00d5c5e-re-your-application-for-senior-engineer.attachments/offer.pdf)
```

### Frontmatter fields

| Key | Always present | Meaning |
| --- | --- | --- |
| `subject` | yes | RFC2047-decoded `Subject`. |
| `from` | yes | `From` header, display name included. |
| `to` | yes | `To` recipients. |
| `cc` | no | Omitted when empty. |
| `date` | yes | Message date. |
| `message_id` | yes | RFC822 `Message-ID`, angle brackets included. |
| `thread_id` | yes | The conversation this message belongs to. **Group by this, not by subject.** |
| `thread_id_source` | yes | How `thread_id` was arrived at: `provider`, `references`, `in_reply_to` or `self`. |
| `in_reply_to` | no | Omitted when absent. |
| `account` | yes | The configured account it was fetched from. |
| `rule` | yes | The rule that claimed it. |
| `labels` | no | Provider labels; omitted when empty. |
| `attachments` | no | Filenames; omitted when empty. |

`thread_id_source` is the trust signal. `provider` means Gmail grouped the
thread itself. `references` / `in_reply_to` / `self` mean mail-muncher
reconstructed the grouping from headers the *sender* controls, which is
best-effort — a mailer that breaks the `References` chain splits a thread. If
completeness matters, check this field before treating a grouping as whole.

### Body and attachments

- **Body selection**: the `text/plain` part if there is one; otherwise the
  `text/html` part converted to markdown; otherwise the literal `*(no body)*`.
  Line endings normalized to LF, trailing whitespace stripped per line, leading
  and trailing blank lines trimmed.
- Frontmatter is produced with a YAML encoder, not string formatting, so a
  subject full of quotes and colons cannot break the parse.
- **Attachments** go to `<basename>.attachments/` next to the `.md`, with
  filenames sanitized (no directory components, no path traversal) and
  collisions de-duplicated as `name-2.pdf`, `name-3.pdf`. They are written
  before the `.md`, so the document never links to a file that is not there.
- **Inline `cid:` images are not resolved.** An HTML body that embeds images by
  content id renders as `![alt](cid:...)` — an unresolved link, not a path into
  the attachments directory. The bytes are in the `.eml`. Known limitation.
- The `.md` is **not** a fidelity format.

## The run summary

Every cycle prints one line per account (and logs the same):

```
fetched=128 matched=6 stored=6 skipped=0 parse_errors=0 sink_errors=0 duration=4.1s
```

| Field | Counts | Meaning |
| --- | --- | --- |
| `fetched` | messages | Delivered by the provider this cycle. |
| `matched` | messages | Claimed by some rule. |
| `stored` | renderings | Actually written. |
| `skipped` | renderings | Not written because the destination already existed. |
| `parse_errors` | messages | Would not parse; logged and skipped. |
| `sink_errors` | renderings | Write failures; logged and counted, cycle continues. |
| `quarantined` | messages | Parked under `quarantine_dir`. Overlaps `parse_errors`/`sink_errors` by design: those count what went wrong, this counts what was done about it. Not a failed run, but always mail an operator should look at. |

A steady-state cron run reads `fetched=0`. A re-run over the same window reads
`matched=N stored=0 skipped=N` — that is idempotency working.

## `--json` manifests

`mail-muncher run --json` writes newline-delimited JSON to stdout, one object
per account. `daemon --json` writes one object per account per tick. All
logging stays on stderr, so stdout is always parseable.

```json
{
  "account": "personal",
  "started_at": "2026-07-28T09:15:00Z",
  "duration_ms": 4100,
  "dry_run": false,
  "stored": [
    {
      "path": "/Users/you/Mail/job-search/2026/07/1785230100-a00d5c5e-re-your-application.eml",
      "format": "eml",
      "rule": "job-search",
      "id": "18f2a9c4d5e6f7a8",
      "message_id": "<abc123@acme.com>",
      "thread_id": "18f2a9c4d5e6",
      "thread_id_source": "provider",
      "from": "jane@acme.com",
      "subject": "Re: Your application for Senior Engineer",
      "date": "2026-07-28T09:15:00Z",
      "has_attachment": true
    }
  ],
  "skipped": [],
  "summary": {
    "fetched": 128, "matched": 6, "stored": 6, "skipped": 0,
    "parse_errors": 0, "sink_errors": 0, "quarantined": 0
  }
}
```

Fields to know:

- `stored` and `skipped` are one entry per **(message x format)**. A rule with
  `formats: [eml, markdown]` contributes two entries sharing an `id` and a
  `thread_id`. Both arrays are always present (`[]` when empty), and every field
  is populated on both — an entry under `skipped` can be read without opening
  the file. `skipped` means the file was already on disk from an earlier cycle;
  it exists and is safe to read.
- `dry_run: true` means every path in `stored` is a path that *would* have been
  written and no state was saved.
- `quarantined` (present only when non-empty) lists messages parked under
  `quarantine_dir`: `path` (raw `.eml`, with a `.json` sidecar beside it), `id`,
  `rule`, `reason` (`parse` or `sink`), `error`, `quarantined_at`. The cursor
  advanced past these; nothing re-delivers them automatically.
- `degraded` / `degraded_files` mean some `from_domains_file` could not be read,
  so "no match" is not a trustworthy verdict for any message in that cycle.
  `state_held: true` means the cursor was deliberately not saved (the default
  `on_degraded_filter: hold`), so the same mail is re-evaluated next cycle.
  Files already stored stay stored; the re-run skips them.
- `stopped: true` means the cycle ended early on a shutdown request. The message
  in flight finished, the state reached was saved, and the rest is picked up
  next run — not lost.
- `error` is the failure that ended the cycle early. `stored` and `skipped` are
  still authoritative for the work completed before it.

Grouping a cycle's deliveries by `thread_id` — "everything that arrived about
the Acme conversation" — needs no file opens at all.
