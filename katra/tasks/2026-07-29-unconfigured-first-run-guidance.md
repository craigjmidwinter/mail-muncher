---
title: Unconfigured first-run guidance
date: "2026-07-29"
time: "09:16:00"
tags:
    - ux
    - adoption
    - agents
summary: Every command, run with nothing set up, explains what is missing and what the options cost
type: task
status: todo
effort: M
epic: adoption-and-onboarding
---

## Context
Per [[no-bundled-oauth-client]] first-run cannot "just work" — we ship no OAuth client and never will. So the unconfigured state is not an error path to be tolerated, it is the **first thing most users and every installing agent will see**. It has to do real work.

Today `mail-muncher run` with no config prints a config-load failure. That tells someone what broke, not what to do, and nothing at all about the fact that there are two provider paths with very different costs.

## Spec
- **Every command that needs config** (`run`, `daemon`, `auth`, `validate`, `mcp`) detects "no config file at the resolved path" as a distinct state from "config is broken", and prints guidance instead of a parse error. Exit code stays 1 (config error) — this is about the message, not the status.
- The message must state, in this order: **what is missing** (the exact path it looked at), **what to do next** (one command), and **the two provider options with their honest costs**:
  - **IMAP + app password** — a couple of minutes, works with Gmail, Fastmail, Proton Bridge, work accounts, self-hosted. Credential is broader than a read-only OAuth token; mail-muncher only ever issues `BODY.PEEK`.
  - **Gmail API + OAuth** — roughly ten minutes in Google Cloud Console, read-only scope enforced by Google, and a refresh token that expires every 7 days on a Testing-mode consent screen.
  Do not editorialize one as "the good one" — state the trade and let the reader choose.
- Point at `mail-muncher init` (see [[init-command]]) and at the docs URL. Keep it under ~15 lines: a wall of text is as useless as no text.
- **Partially-configured states get their own messages**, each naming the next command:
  - config exists, account has never been authorized → point at `auth`
  - token exists but is expired/revoked → point at re-running `auth`, and say the 7-day Testing-mode expiry is the usual cause
  - config exists but zero rules → explain nothing will ever be stored and point at the filters doc
- **Agent-legible, not just human-legible.** An agent installing the plugin will run a command and read stdout/stderr. The guidance must be unambiguous enough to act on without fetching docs: name exact file paths, exact commands, exact config keys. Avoid ASCII art, avoid colour as the sole carrier of meaning.
- `mcp` is the sharpest case: an MCP client starts it and sees only a dead server. It must fail with a message the calling agent can relay verbatim, and — if the transport allows — surface the same guidance as a tool error rather than only on stderr.

## Acceptance
- Table-driven tests over the states: no config, unreadable config, no accounts, no rules, no token, expired token. Each asserts the *actionable* content — the path, the next command — not just that some error occurred.
- Golden-file the no-config output for `run` and for `mcp`; these strings are a user interface and should not drift silently.
- Verified by hand from a clean state: `HOME=$(mktemp -d) mail-muncher run` reads as helpful, not as a crash.
