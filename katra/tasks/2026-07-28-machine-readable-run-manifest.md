---
title: Machine-readable run manifest
date: "2026-07-28"
time: "16:12:00"
tags:
    - agents
    - pipeline
summary: 'run --json: per-message manifest of what a cycle stored, on stdout'
type: task
status: todo
effort: S
epic: agent-interface
---

## Context
An agent invoking `mail-muncher run` needs to know what landed without diffing the destination directory. Same cycle, same work — a second rendering of the result.

## Spec
- `run --json` (and `daemon --json`, one object per cycle, newline-delimited): emit the manifest to **stdout**. All human logging already goes to stderr via slog, so the two never interleave.
- Shape:

```json
{
  "account": "personal",
  "started_at": "2026-07-28T09:15:00Z",
  "duration_ms": 1840,
  "stored": [
    {
      "path": "/home/user/Mail/job-search/2026/07/1753...-a3f1b2c8-re-your-application.md",
      "format": "markdown",
      "rule": "job-search",
      "id": "18f2a...",
      "message_id": "<CAF...@mail.gmail.com>",
      "from": "recruiting@acme.com",
      "subject": "Re: your application",
      "date": "2026-07-28T09:15:00Z",
      "has_attachment": false
    }
  ],
  "skipped": [ { "path": "...", "format": "eml", "rule": "job-search" } ],
  "summary": { "fetched": 42, "matched": 3, "stored": 3, "skipped": 39, "parse_errors": 0, "sink_errors": 0 }
}
```

- One object per account. `stored` carries an entry per (message × format) — a rule with `formats: [eml, markdown]` yields two entries sharing an `id`.
- `--dry-run --json` emits the same shape with the paths that *would* be written (use the sinks' `Plan`), and a top-level `"dry_run": true`.
- Exit codes are unchanged. A cycle that fails mid-way still emits the manifest for what it managed to store before the error, so an agent never loses track of files that exist on disk.
- Marshal from an exported `pipeline.Manifest` type — the MCP server returns the same struct from its sync tool, so it must not be private to the command layer.

## Acceptance
- Unit tests: manifest shape golden-file, one entry per format, dry-run flag, partial manifest on provider error.
- Verify by hand that `mail-muncher run --json 2>/dev/null` emits *only* valid JSON — nothing from slog leaks into stdout.
