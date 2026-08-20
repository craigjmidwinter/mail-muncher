---
title: 'init command: write a working config'
date: "2026-07-29"
time: "09:18:00"
tags:
    - ux
    - adoption
summary: 'mail-muncher init: pick a provider, write a valid config, say what to run next'
type: task
status: done
effort: M
epic: adoption-and-onboarding
---

## Context
Time-to-first-email is the metric. Today the answer to "how do I start" is "read two documentation pages, then hand-write YAML." That is a filter, and it filters out people who would have liked the tool.

## Spec
`mail-muncher init` — writes a valid config and tells the user exactly what to run next.

- Interactive by default; **fully non-interactive with flags** (`--provider`, `--account`, `--dest`, `--yes`) because an agent will run this, not just a person.
- Asks only what cannot be defaulted: provider (imap | gmail), account name, destination directory. Everything else takes the documented default.
- **Never overwrites an existing config.** If one exists, print its path and exit non-zero. `--force` may overwrite; nothing else may.
- Writes the file with a starter rule that is **useful but bounded** — the `newer_than: 72h` smoke-test rule is a good default because it guarantees the first run matches something, which is what proves the install. Comment it in the generated file as a starter to be narrowed.
- Generated config must pass `mail-muncher validate` — assert this in a test, for every provider branch.
- Ends by printing the next command, and it must be the *right* one for the chosen provider: `auth --account <name>` for gmail, a password-command check for imap.
- For the gmail path, tell the user up front that this needs ~10 minutes in Google Cloud Console and link `docs/gmail-setup.md`, **before** they start rather than after. Per [[no-bundled-oauth-client]] that cost is structural; surprising people with it is the avoidable part.

## Acceptance
- Non-interactive invocation produces a config that validates, for both providers.
- Refuses to clobber an existing config; `--force` overwrites.
- Golden-file the generated configs.
- A fresh-machine walkthrough: `init` → `auth` → `run --dry-run` → `run` with no other documentation open.
