---
title: A vanished message is not a fetch failure
date: "2026-07-29"
time: "18:05:00"
tags:
    - gmail
    - sync
    - reliability
summary: 404 on messages.get is terminal for that message and benign for the cycle
type: decision
status: accepted
---

## Context
Reported from production by a consumer, reproduced three runs running.

A message was deleted from the mailbox between `users.history.list` returning its id and `users.messages.get` downloading it. The 404 propagated out of `Fetch`, the cycle aborted, and the pipeline — correctly, by its own rules — refused to save state after a fetch error. So `HistoryID` never advanced, the next cycle requested the same window, re-listed the same dead id, and 404ed again.

**The integration was not slow. It was deadlocked**, and it could not self-heal: every future cycle was guaranteed to fail in exactly the same place. The only escape available to the user was deleting their sync cursor and re-scanning the mailbox, which is a heavy, lossy-feeling operation to demand of someone whose actual problem was one deleted message.

## Decision
A **404 on `users.messages.get`** skips that message and lets the cycle continue. Every other error — 401, 403, 429, 5xx, transport failures, context cancellation — still aborts the cycle and still refuses to advance the cursor.

## Why the distinction is the whole point
The abort-on-fetch-error rule is deliberate and correct: advancing a cursor past mail that was never delivered loses mail silently, which is the worst thing this tool can do. That rule is not being weakened.

What was missing is that **404 is not a failure to fetch. It is authoritative evidence that there is nothing to fetch.** Retrying cannot help. Waiting cannot help. And refusing to advance does not protect anything, because the message it is "protecting" no longer exists — it only guarantees the wedge.

So the rule is sharpened rather than relaxed:

- **Might the message still be there?** Abort. Do not advance. (Everything except 404.)
- **Is the message definitively gone?** Skip it, count it, log it, keep going.

## Consequences
- Cycles survive ordinary mailbox churn. Deleting mail while a daemon polls is normal behaviour, not an outage.
- **Skips must be visible.** A vanished message is counted and logged with its id, so "listed then gone" is legible in the run output and never silent. A silent skip here would be indistinguishable from the mail loss the abort rule exists to prevent.
- The negative controls matter as much as the fix. Tests assert 403, 500 and transport errors still abort and still pin the cursor. If those ever start passing under a broadened "skip on error", this decision has been undone and mail loss is back.
- **Do not generalize this to 4xx.** 403 in particular can mean a revoked token or a scope problem — conditions where the mail is still there and skipping would silently drop it.
- The general shape to watch for: *any per-message terminal condition that aborts the whole cycle wedges the cursor forever.* 404 was one instance. A new one is a bug of the same class, not a new category.
