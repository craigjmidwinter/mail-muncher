---
title: IMAP app password as the primary onboarding path
date: "2026-07-29"
time: "09:20:00"
tags:
    - imap
    - adoption
summary: Promote IMAP from 'later' to the fast path, and document the credential trade honestly
type: task
status: todo
effort: S
epic: adoption-and-onboarding
---

## Context
This is a positioning task, not an implementation one — the provider itself is specced in [[imap-provider-behind-the-same-interface]], whose epic is promoted from `later` to `now` by [[adoption-and-onboarding]].

Per [[no-bundled-oauth-client]], the Gmail-API path costs ~10 minutes of Google Cloud Console and expires every 7 days, and neither is fixable by us. An IMAP app password involves **no OAuth client, no consent screen, no verification, and no expiry**. It is not the fallback; it is the only fast path that exists, and it simultaneously unlocks Fastmail, Proton Bridge, work accounts and self-hosted mail.

## Spec
Once the IMAP provider lands, make it the path a newcomer meets first:

- **README quickstart leads with IMAP.** Gmail API moves to a clearly-signposted section for people who specifically want Google-enforced read-only, with its costs stated up front rather than discovered.
- **`docs/imap-setup.md`**, written to the same standard as `docs/gmail-setup.md`: enabling 2FA, generating an app password, storing it so `password_cmd` can retrieve it (Keychain on macOS, `pass`/`secret-tool` elsewhere), and a troubleshooting section keyed to real IMAP errors.
- **State the credential trade honestly, per provider, wherever the read-only claim appears.** `gmail.readonly` is enforced by Google: the token *cannot* send or delete. An app password is a full mail credential; mail-muncher only ever issues `BODY.PEEK` and never sets `\Seen`, but that restraint lives in our code rather than in the provider's enforcement. The README's security section currently makes a blanket read-only claim that is only true of the Gmail path — fix it rather than soften it.
- Provider comparison table near the top of the README: setup time, credential scope, who enforces read-only, expiry, which mailboxes it works with.

## Acceptance
- A newcomer reading only the README quickstart reaches first mail via IMAP without opening the Gmail docs.
- No remaining claim in README or docs asserts Google-enforced read-only for the IMAP path.
- `docs/imap-setup.md` is accurate enough that a fresh machine can follow it.
