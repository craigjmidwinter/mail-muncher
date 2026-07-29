---
title: Agent interface
date: "2026-07-28"
time: "16:10:00"
tags:
    - agents
    - mcp
summary: Machine-readable run output plus an MCP server, so agents consume mail directly
type: epic
status: planned
horizon: next
---

The reframing: mail-muncher is an email client **for agents**. An agent declares what mail it wants (the externally-managed domain file), and mail-muncher delivers exactly that — read-only, idempotently, as files the agent can parse.

Everything through the `now` epics already serves this. Two gaps remain:

- **"What just landed?"** A cycle prints a human summary and writes files. An agent has to diff a directory to learn what changed. A JSON manifest closes that loop in one stream.
- **Files are a one-way interface.** An agent that wants to *ask* — "any mail from Acme this week?" — has to implement directory walking and frontmatter parsing itself, per agent. An MCP server makes mail a first-class tool call.

The invariant that must survive both: mail-muncher holds `gmail.readonly`. It cannot send, delete, or modify mail, and nothing in this epic may weaken that. The MCP server exposes *stored* mail from configured destinations only — never arbitrary filesystem paths, never the live mailbox.
