---
title: External domain-list file source with reload
date: "2026-07-28"
time: "15:34:49"
tags:
    - filters
    - jobsearch
summary: 'from_domains_file: newline list maintained by another app, re-read every cycle'
type: task
status: done
effort: S
epic: filter-engine
---

## Context
The motivating use case: another application (the job-search tracker) owns a file listing company domains. mail-muncher must always use the current contents without restarts or config edits.

## Spec
In `internal/filter`:
- File format: one domain per line; trim whitespace; ignore blank lines and `#` comments; lowercase everything; strip a leading `@` if present (be liberal). Reject nothing else — log a warn for lines that don't look like a domain (no dot) but keep going.
- `from_domains_file` predicates re-read the file **at the start of every pipeline cycle** (each `run`, each daemon tick) — not per message. Implement a small loader with per-cycle caching (e.g. the engine gets a `BeginCycle()` that clears the cache, or loaders keyed by path stored on the engine and refreshed explicitly by the pipeline).
- Missing/unreadable file at cycle time: the predicate matches **nothing**, and the cycle logs one warning per file. It must NOT fail the run — the other app may not have created it yet.
- Matching semantics identical to `from_domains` (equality or subdomain).

## Acceptance
- Unit tests: parsing (comments, blanks, case, `@`-prefix), reload picks up a file changed between two cycles, missing file → matches nothing + no error.
