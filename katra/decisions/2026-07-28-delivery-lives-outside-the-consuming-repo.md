---
title: Delivery lives outside the consuming repo
date: "2026-07-28"
time: "17:04:00"
tags:
    - security
    - agents
summary: dest must not sit inside a repo that unattended agents can write and that gets pushed
type: decision
status: accepted
---

## Decision
A rule's `dest` lives **outside** the repository of the program that consumes it — e.g. `~/Mail/<topic>` rather than a directory inside the consumer's checkout. Any file the consumer maintains as a subscription (a `from_domains_file`, say) lives outside it too. Neither delivered mail nor the subscription file is ever committed.

## Why
Three independent reasons, any one of which is sufficient.

**1. Unattended agents may have write access to that repo.** A repository worked by a coding agent running non-interactively, in an edits-are-auto-approved mode, has that agent's write access across the whole tree. Email is attacker-controlled text. A well-built consumer already guards this boundary from the inside — deterministic classification with no model in the loop, metadata plus a truncated snippet rather than full bodies, output that a human reads before anything acts on it. Delivering raw `.eml` and full-body `.md` into that repo would put attacker-controlled content directly in the working set of an unattended agent that can edit files, defeating the boundary from the outside no matter how careful the consumer is. The general form: **never deliver untrusted input into a directory an unattended agent has write access to**, however well the consumer of that input is sandboxed.

**2. It would be committed and pushed.** Repositories get built, deployed, and served. Personal mail — full bodies, headers, attachments — would land in git history and in a deployed image. That is unrecoverable in the way git is unrecoverable.

**3. Ownership.** A subscription file is a contract *between* two programs. Putting it inside one of them makes it look like that program's private state and invites someone to "clean it up" into the repo's config.

## Consequences
- `.gitignore` is **not** the mitigation. The path simply is not inside the repo; there is nothing to ignore and nothing to get wrong later.
- A consumer reads from an absolute path outside its own checkout, and must keep truncating to a snippet — mail-muncher writing full bodies to disk does not license passing full bodies onward to anything that classifies or acts.
- mail-muncher itself needs no changes: `dest` is already an arbitrary path, and the tool holds `gmail.readonly` and cannot modify the mailbox.
- If a future consumer wants mail inside a repo, that is a decision to revisit explicitly, with the agent-write-access question answered first.
