---
title: 'Job-search wiring: sample config, GCP setup doc, first real pull'
date: "2026-07-28"
time: "15:35:28"
tags:
    - jobsearch
    - docs
summary: README + docs/gmail-setup.md, examples/job-search.yml, verified real run against Gmail
type: task
status: todo
effort: S
epic: job-search-integration
---

## Context
The proving-ground use case: pull messages from companies applied to (domain list owned by the job-search app) into `~/Mail/job-search` as eml+markdown.

## Spec
- `docs/gmail-setup.md`: step-by-step, screenshots optional — create GCP project → enable Gmail API → OAuth consent screen (External, test mode, add own address as test user) → create Desktop-app OAuth client → download credentials.json → `mail-muncher auth --account personal`. Note the 7-day token expiry caveat of test-mode consent screens and the fix (publish app or re-auth).
- `examples/job-search.yml`: complete working config for this use case — one gmail account, one rule with `any: [from_domains_file: …]`, `formats: [eml, markdown]`, `initial_lookback: 2160h` (90 days — job search mail is recent).
- README: what/why, quickstart (build → setup doc → validate → run --dry-run → run), config reference table (every key, one line each), cron + launchd pointers, exit codes.
- **Manual verification runbook, executed for real** (this task is done only when performed): seed a domains file with 2-3 real domains from the job search, `run --dry-run`, inspect, then `run`, confirm .eml + .md files land correctly, run again → all `skipped`, add a domain to the file → next run picks up that sender without restart. Paste the (redacted) summary lines into the katra entry for this work.

## Acceptance
- Docs exist and are accurate enough that a fresh machine could follow them.
- Runbook executed against the real account; results recorded in the katra draft.
