---
title: 'No delivery notifications: files are the contract'
date: "2026-07-28"
time: "16:52:00"
tags:
    - architecture
    - agents
summary: Rejected webhooks/notify-on-delivery; consumers poll the directory on their own cadence
type: decision
status: accepted
---

## Decision
mail-muncher will **not** notify anyone when it delivers mail. No webhooks, no exec-on-delivery hook, no callback, no socket, no queue. A cycle writes files and exits. Consumers check the directory on whatever cadence suits them.

## Why
The files-on-disk contract is the clean part of this design, and it is clean precisely *because* it is one-directional. mail-muncher never calls the consumer and the consumer never calls mail-muncher; they share two paths and nothing else. That property is what makes the tool safe to point at an unattended process, trivial to test, and impossible to get into a broken state that needs draining.

Every notification mechanism trades that away for latency nobody has asked for:

- A webhook makes mail-muncher an HTTP client with retries, timeouts, backoff, dead-letter semantics, and a new failure mode where mail is on disk but the consumer never heard about it — so the consumer needs a reconciling directory scan anyway. The scan is the reliable mechanism; the webhook is a cache in front of it.
- An exec-on-delivery hook makes mail-muncher a process supervisor and hands the delivered message's content to a subprocess, which is a meaningful escalation for a tool holding a mailbox.
- Either one makes the tool stateful about *who is listening*, which is exactly the coupling the externally-managed filter file was designed to avoid.

The consumer side already polls. The job-search integration runs on its own schedule; a mail-triage step is one more directory listing at the top of a run it was going to do anyway. Delivery latency is bounded by the daemon's poll interval regardless, so a notification would at best shave part of one interval.

## Consequences
- Consumers must be able to answer "what is new?" from the directory. The deterministic, sortable filenames and the `--json` manifest make that cheap; `thread_id` in frontmatter makes it groupable.
- If a genuine low-latency need appears later, the honest answer is a shorter `--interval`, not a callback.
- Revisit only with a concrete consumer that provably cannot poll — not with a hypothetical one.
