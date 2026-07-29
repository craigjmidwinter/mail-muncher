---
title: Gmail OAuth flow and token storage
date: "2026-07-28"
time: "15:34:11"
tags:
    - gmail
    - oauth
summary: 'mail-muncher auth: loopback OAuth flow, token refresh, gmail.readonly scope'
type: task
status: todo
effort: M
epic: gmail-provider
---

## Context
Per decision [[gmail-api-over-imap-for-the-first-provider]], Gmail access is via the Gmail API with a user-supplied OAuth client (desktop-app type, created once in Google Cloud Console; test-mode consent screen is fine for a personal tool).

## Spec
- Packages: `golang.org/x/oauth2`, `golang.org/x/oauth2/google`, `google.golang.org/api/gmail/v1`.
- Scope: `gmail.readonly` only.
- `mail-muncher auth --account <name>`:
  1. Read the account's `gmail.credentials_file` (the downloaded OAuth client JSON; parse with `google.ConfigFromJSON`).
  2. Loopback flow: listen on `127.0.0.1:0`, set redirect to that port, print the auth URL (and try to open the browser via `open` on darwin — best-effort), wait for the code on the loopback server, exchange it.
  3. Write the token JSON to `gmail.token_file` with mode 0600, atomic write.
- Runtime token source: `oauth2.Config.TokenSource` wrapping the stored token auto-refreshes; persist the refreshed token back to disk when it changes (wrap the TokenSource to detect changes).
- Clear errors: missing credentials file → point at the GCP setup doc (docs task in job-search-integration epic); expired/revoked refresh token during `run` → instruct to re-run `auth`.

## Acceptance
- `auth` completes against a real Google account (manual verification; document the steps taken in the PR/entry).
- Unit tests for token persistence + refresh-detection wrapper (fake TokenSource, no network).
