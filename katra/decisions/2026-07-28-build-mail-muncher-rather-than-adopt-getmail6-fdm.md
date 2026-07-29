---
title: Build mail-muncher rather than adopt getmail6/fdm
date: "2026-07-28"
time: "15:33:03"
tags:
    - scope
    - research
summary: Existing tools miss the external domain list, markdown output, and single-binary combo
type: decision
status: accepted
---

Surveyed the landscape before writing stories. Closest existing tools:

- **getmail6** — mature, IMAP + Gmail OAuth2, Maildir delivery, filters via external programs. The strongest adopt candidate.
- **fdm** — fetch+filter+deliver with per-rule maildir destinations (exactly our shape), but app-password IMAP only, static config.
- **gmail-archive** (Node) — Gmail-query → maildir, incremental; unmaintained since 2018.
- **gmail-exporter** (Go) — label-based, spreadsheet-centric; no incremental sync.
- **lieer** — whole-mailbox Gmail↔maildir sync for notmuch; not filtered pulls.

Nothing covers the actual combination: filter config driven by an **externally-managed domain-list file**, **optional markdown rendering**, Gmail OAuth, single binary, cron/daemon modes. The adopt path (getmail6 + a generator script compiling the domains file into filter config) is ~50 lines of glue, gets no markdown, and the glue is half a tool anyway.

**Rejected:** adopting getmail6 + glue. It would ship this week, but every distinctive requirement would still be custom code bolted onto someone else's config format.
