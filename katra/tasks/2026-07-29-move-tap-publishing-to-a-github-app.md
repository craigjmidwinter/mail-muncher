---
title: Move tap publishing to a GitHub App
date: "2026-07-29"
time: "11:05:00"
tags:
    - release
    - security
    - ops
summary: Replace the non-expiring PAT with an App that mints a short-lived token per run
type: task
status: todo
effort: S
epic: adoption-and-onboarding
---

## Context
The release workflow updates the Homebrew cask in `craigjmidwinter/homebrew-tap`, a different repository. A workflow's default `GITHUB_TOKEN` cannot push cross-repo, so the current mechanism is a **fine-grained PAT with no expiration**, stored as the `HOMEBREW_TAP_GITHUB_TOKEN` secret and scoped to Contents:write on that one repo.

That was the right call at the time — the alternative was a credential that silently breaks the release pipeline every 90 days, which is a worse failure than the one it prevents. But it leaves a standing credential that never rotates: if it ever leaks it stays valid until somebody notices and revokes it by hand.

## Spec
Replace the PAT with a **GitHub App**, which is the mechanism GitHub actually intends for cross-repo automation.

- Create an App owned by the same account, with **Contents: Read and write** on repository access, installed **only** on `homebrew-tap`.
- Store `APP_ID` and the App's private key as secrets on the **mail-muncher** repo.
- In `.github/workflows/release.yml`, mint a token per run — `actions/create-github-app-token` is the maintained action for this — and pass it where `HOMEBREW_TAP_GITHUB_TOKEN` is passed today.
- Delete the PAT from the account and the secret from the repo once a real release has published through the new path. **Verify before deleting**, not after.

## Why it is better
- **The token is short-lived** (about an hour) and minted fresh per run, so a leaked one expires on its own. The App's private key is the long-lived secret, and it never appears in a workflow log or a build artifact.
- **Nothing to renew, ever.** That is the property the PAT was chosen for and the App gets it without the standing-credential cost.
- **Auditable** — App actions are attributable to the App rather than to a human account, so the tap's commit history stops looking like the owner pushed it by hand.

## Acceptance
- A tagged release updates the cask in `homebrew-tap` with no PAT present anywhere.
- The workflow still fails **loudly and explicitly** when App credentials are missing — the current preflight writes a job-summary block and a `::warning`, and skips the tap while still publishing binaries. Preserve that behaviour; a silent skip is how a broken tap goes unnoticed for months.
- `brew install craigjmidwinter/tap/mail-muncher` installs the new version afterwards.
- The PAT is revoked and the old secret deleted.

## Note
Not urgent. The PAT works, is scoped to one repository and one permission, and the blast radius is a public tap containing a generated cask file. This is a hardening task, not a fix — schedule it when the release path is otherwise stable, and do it in one sitting so the repo is never half-migrated.
