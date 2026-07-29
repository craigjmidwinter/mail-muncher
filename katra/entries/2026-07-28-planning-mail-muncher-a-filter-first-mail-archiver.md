---
title: 'Planning mail-muncher: a filter-first mail archiver'
date: "2026-07-28"
time: "15:36:14"
tags:
    - planning
    - architecture
---

```embed
src: media/mail-muncher-arch.html
height: 480
caption: The pipeline: provider seam → parse → rule engine (fed by the externally-owned domains file) → sinks
```

The job-search tracker knows which companies I've applied to; my inbox knows which of them have written back. Nothing connects the two. mail-muncher is the connector: a small Go tool that pulls mail down, runs each message through configurable rules, and files the matches on disk — one-shot for cron, or as a polling daemon.

The design bet is that **the filter config shouldn't own the data it filters on**. The domain list lives in a plain text file the job-search app maintains; mail-muncher re-reads it every cycle, so applying to a new company changes what gets archived without touching config or restarting anything. Rules are ordered, composable (`all`/`any`/`not` over predicates like `from_domains_file`, `subject_regex`, `has_attachment`), and first-match-wins routes each message to a destination directory as byte-faithful `.eml` — optionally with a markdown rendering alongside for greppability.

Before writing a line, we checked whether this tool already exists. It nearly does, several times over — getmail6 and fdm both fetch-filter-deliver, and gmail-archive was almost exactly this before dying in 2018 — but nothing handles an externally-managed filter source or markdown output, and the glue to fake it would be half a tool anyway. Details in [[build-mail-muncher-rather-than-adopt-getmail6-fdm]]. The other fork in the road was Gmail API vs IMAP for the first provider; the API won on OAuth + cheap incremental sync via the history API, with IMAP parked as a later epic behind the same interface ([[gmail-api-over-imap-for-the-first-provider]]).

The roadmap is 7 epics / 15 tasks, specced tightly enough to hand to implementation agents: four `now` epics (skeleton+config, gmail provider, filter engine, sinks), two `next` (run modes, the real job-search wiring), IMAP `later`.

```warning
Nothing is built yet — this entry is the plan. The riskiest unknowns are Gmail history-API expiry behavior (mitigated by a full-scan fallback) and the 7-day token expiry on test-mode OAuth consent screens, which the setup doc has to warn about.
```
