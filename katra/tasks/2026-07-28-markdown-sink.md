---
title: Markdown sink
date: "2026-07-28"
time: "15:34:49"
tags:
    - storage
    - markdown
summary: 'Optional per-rule .md rendering: frontmatter + html-to-markdown body + attachments dir'
type: task
status: done
effort: M
epic: storage-sinks
---

## Context
The optional human-readable format ("formats: [eml, markdown]"). Goal: a message you can read in an editor / feed to other tooling, not a perfect fidelity copy — the .eml is fidelity.

## Spec
In `internal/sink`:
- Same basename as the EML sink (shared helper), extension `.md`, same skip-on-exists idempotency.
- File shape:

```markdown
---
subject: "..."
from: "Jane Doe <jane@acme.com>"
to: ["..."]
cc: ["..."]        # omit if empty
date: 2026-07-28T09:15:00Z
message_id: "<...>"
account: personal
rule: job-search
labels: [INBOX]     # omit if empty
attachments: [offer.pdf]   # omit if none
---

<body>
```

- Body selection: prefer `TextBody`; if only `HTMLBody`, convert with `github.com/JohannesKaufmann/html-to-markdown/v2`; if both empty, write `*(no body)*`. Normalize to LF, trim trailing whitespace per line.
- Attachments: when the message has any, write them to `<basename>.attachments/<sanitized-filename>` next to the .md (dedupe name collisions with `-2`, `-3`), and link them under an `## Attachments` section. Inline/cid images: don't resolve; leave as-is (note in README).
- YAML-escape frontmatter values properly (use yaml.Marshal for the frontmatter struct, don't hand-format).

## Acceptance
- Unit tests: text-only, html-only (golden-file the conversion), both-empty, attachment writing + collision dedupe, frontmatter escaping (subject containing `"` and `:`).
