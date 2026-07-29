---
title: Core skeleton and config
date: "2026-07-28"
time: "15:29:00"
tags:
    - go
    - cli
summary: Go module, cobra CLI, config schema, canonical message model
type: epic
status: planned
horizon: now
---

Foundation for mail-muncher: a small Go tool that pulls mail from a provider, runs each message through configurable filter rules, and writes matches to the filesystem. Runs as a one-off CLI (cron-able) or a polling daemon.

Architecture (all later epics hang off these seams):

- `Provider` interface (fetch raw RFC822 messages incrementally) — Gmail first, IMAP later.
- Canonical `Message` model parsed from raw RFC822.
- Filter engine: ordered rules, composable predicates, external domain-list files.
- Sinks: raw `.eml` always available, markdown optional per rule.
- State store for incremental sync + dedup.
