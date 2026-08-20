---
title: EML sink with idempotent layout
date: "2026-07-28"
time: "15:34:49"
tags:
    - storage
summary: Write byte-faithful .eml under dest/YYYY/MM with deterministic collision-free names
type: task
status: done
effort: M
epic: storage-sinks
---

## Context
Default storage: the raw RFC822 message exactly as fetched, in a layout that's browsable, greppable, and safe to re-run (cron overlap, replays after state loss).

## Spec
In `internal/sink`:
- Path scheme: `<rule.dest>/<YYYY>/<MM>/<basename>.eml` where `YYYY/MM` from the message Date (UTC; fall back to InternalDate) and

  `basename = <unix-seconds>-<sha256(account+":"+msg.ID)[:8]>-<slug(subject, max 40)>`

  `slug`: lowercase, non-alphanumerics → `-`, collapse repeats, trim `-`, "no-subject" if empty. The hash fragment makes the name deterministic per message — that IS the idempotency key.
- Write behavior: create dirs 0755; if the target file already exists, skip silently (count as `skipped` in the run summary). Write temp file in the same dir + rename.
- `Sink` interface both sinks implement: `Store(msg *model.Message, rule *config.Rule) (path string, skipped bool, err error)` — the pipeline fans a matched message to each format listed in the rule.
- A sink error for one message logs and continues the run (counted as `errored`), it does not abort the cycle.

## Acceptance
- Unit tests: path construction (fixed date + subject fixtures), byte-faithful content (`Raw` in == file out), skip-on-exists, weird subjects (empty, emoji, 200 chars), temp+rename leaves no droppings on failure.
