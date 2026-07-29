---
title: Gmail setup
layout: default
nav_order: 2
description: >-
  Create your own Google Cloud OAuth client for mail-muncher and authorize it
  with the single gmail.readonly scope. A one-time setup of about ten minutes.
---

# Gmail setup

mail-muncher ships no OAuth client of its own. You create one in your own
Google Cloud project, download its JSON, and it stays yours — Google's quota,
Google's audit log, your credentials on your disk. This is a one-time setup of
about ten minutes, mostly clicking through console screens.

The only scope requested is `gmail.readonly`. The resulting token cannot send,
delete, label, or modify anything in the mailbox.

**Read [Step 3](#3-configure-the-oauth-consent-screen) before you start.** A
consent screen left in *Testing* mode expires its refresh token after seven
days, which will silently stop unattended runs a week after everything works.

---

## 1. Create a Google Cloud project

1. Go to <https://console.cloud.google.com/>.
2. Open the project picker in the top bar and choose **New project**.
3. Name it something you will recognize in a year — `mail-muncher` is fine —
   and click **Create**.
4. Make sure the new project is selected in the top bar before continuing.
   Every step below applies to the *currently selected* project, and setting
   things up in the wrong one is the single most common way to get stuck.

A personal Gmail account can create projects without billing. No card, no
charges: the Gmail API's free quota is far beyond what a personal archiver
uses.

## 2. Enable the Gmail API

1. Navigate to **APIs & Services → Library**.
2. Search for **Gmail API** and open it.
3. Click **Enable**.

Skipping this step produces an error at first fetch, not at `auth` time — the
OAuth flow succeeds and the API call is refused:

```
Gmail API has not been used in project 123456789 before or it is disabled.
```

If you see that, come back here, enable the API, and wait a minute or two for
it to propagate. No re-auth is needed.

## 3. Configure the OAuth consent screen

1. Go to **APIs & Services → OAuth consent screen**.
2. Choose **External** as the user type, and click **Create**.
   - *Internal* is only available to Google Workspace organizations. If you
     have one and choose Internal, the seven-day expiry below does not apply to
     you.
3. Fill in the required fields: an app name (`mail-muncher`), your own address
   as the user support email, and your own address as the developer contact.
   Nothing else is required.
4. On the **Scopes** screen, click **Save and continue**. You do not need to
   add scopes here — mail-muncher requests `gmail.readonly` at authorization
   time.
5. On the **Test users** screen, click **Add users** and add **the Gmail
   address you want to archive**. This is mandatory: an app in Testing mode
   will refuse to authorize any account not on this list.
6. Save.

### The seven-day refresh-token expiry

An External consent screen in **Testing** publishing status issues refresh
tokens that **expire after seven days**. Everything works, and then a week
later every unattended run fails at once with:

```
gmail: stored token was rejected for account "personal" (revoked, expired, or the OAuth client changed): re-run `mail-muncher auth --account personal`
```

Two ways to deal with it. Pick one now rather than being surprised later.

**Option A — publish the app (recommended).** On the OAuth consent screen, set
the publishing status to **In production**. Refresh tokens then last until they
are revoked, and unattended runs keep working indefinitely.

Google shows a verification prompt when you do this. You do not need to
complete verification: an unverified app can still be used by its own owner and
by accounts you have granted access. You will see an "Google hasn't verified
this app" interstitial during `auth` — click **Advanced → Go to mail-muncher
(unsafe)** to continue. Verification is only required to offer the app to other
people's accounts, which is not what this is.

**Option B — re-authorize weekly.** Leave the app in Testing and run
`mail-muncher auth --account <name>` again whenever a run reports a rejected
token. Fine for a machine you sit in front of; poor for a cron job.

## 4. Create a Desktop app OAuth client

1. Go to **APIs & Services → Credentials**.
2. Click **Create credentials → OAuth client ID**.
3. Set **Application type** to **Desktop app**. This matters:
   - Desktop clients are allowed to redirect to `http://127.0.0.1:<any port>`,
     which is exactly what the `auth` loopback flow does.
   - A **Web application** client requires you to register every redirect URI in
     advance, and `auth` binds an OS-assigned port, so the redirect will be
     rejected.
   - A **TVs and Limited Input devices** client cannot request Gmail scopes.
4. Name it (`mail-muncher cli`) and click **Create**.
5. In the confirmation dialog, click **Download JSON**.

Save the file where your config points and lock it down:

```bash
mkdir -p ~/.config/mail-muncher
mv ~/Downloads/client_secret_*.json ~/.config/mail-muncher/credentials.json
chmod 600 ~/.config/mail-muncher/credentials.json
```

A correct Desktop-app credentials file starts with an `"installed"` key:

```bash
head -c 60 ~/.config/mail-muncher/credentials.json
# {"installed":{"client_id":"1234567890-abc.apps.googleu
```

If it starts with `{"web":` you created a Web application client. Delete it and
create a Desktop app client instead.

Never commit this file. The repo's `.gitignore` already excludes
`/credentials.json` and `/token.json` at the repository root as a safety net,
but the real answer is to keep them under `~/.config`.

## 5. Point the config at it

```yaml
accounts:
  - name: personal
    provider: gmail
    gmail:
      credentials_file: ~/.config/mail-muncher/credentials.json
      token_file: ~/.config/mail-muncher/token.json
      initial_lookback: 2160h
```

Check it before authorizing:

```bash
mail-muncher validate
```

The token file does not exist yet, so expect exactly this warning — it is not
an error and `validate` exits 0:

```
warning: accounts[0].gmail.token_file: file does not exist yet: /Users/you/.config/mail-muncher/token.json (run `mail-muncher auth`)
```

## 6. Authorize

```bash
mail-muncher auth --account personal
```

`--account` may be omitted when the config defines exactly one account.

The command prints a consent URL, tries to open your browser on it, and starts
a temporary HTTP server on a loopback port to catch the redirect:

```
Authorizing Gmail account "personal".
Open this URL in a browser and grant read-only access:

https://accounts.google.com/o/oauth2/auth?access_type=offline&client_id=...

Waiting for the browser to redirect back...
```

On a headless machine or over SSH, no browser opens — copy the URL into a
browser on any machine that can reach `127.0.0.1` on the printed port. In
practice that means the same machine; for a remote host, forward the port with
`ssh -L`.

In the browser: pick the Google account you added as a test user, click through
the unverified-app interstitial if it appears (**Advanced → Go to mail-muncher
(unsafe)**), and grant the single read-only permission. The browser lands on a
page that says **Authorized**, and the terminal prints:

```
Authorized "personal"; token written to /Users/you/.config/mail-muncher/token.json
```

The token file is written with mode 0600 via an atomic temp-file-and-rename, so
an interrupted write never leaves you with a truncated token. There is a
five-minute timeout on the whole flow.

## 7. First pull

```bash
mail-muncher run --dry-run     # fetch and evaluate, write nothing
mail-muncher run               # do it for real
```

The first run is a full scan bounded by `initial_lookback`, so it is the
slowest one. Every run after it uses Gmail's history API and is cheap.

Mail in Spam and Trash is not fetched: `gmail.include_spam_trash` defaults to
`false`, so those messages never reach your rules and never reach an AI agent's
context window. If something you wanted is missing from the first pull, check
Gmail's Spam folder before anything else — see
[configuration.md](configuration.md#include_spam_trash) for the opt-in.

---

## Troubleshooting

The error strings below are the exact text mail-muncher emits. Match on them.

### `gmail: OAuth credentials file not found: <path>`

```
gmail: OAuth credentials file not found: /Users/you/.config/mail-muncher/credentials.json
Create a Desktop app OAuth client in the Google Cloud Console, download its JSON, and save it there (or point `gmail.credentials_file` at it). See docs/gmail-setup.md.
```

The path in `gmail.credentials_file` does not exist. You have not done
[step 4](#4-create-a-desktop-app-oauth-client), or the file is somewhere else,
or `~`/`$VAR` in the path expanded to somewhere you did not expect. Check what
the path resolved to:

```bash
mail-muncher validate      # prints expanded paths in its warnings
```

### `gmail: parse credentials <path>: ...`

The file is not an OAuth client JSON. Usual causes: you downloaded a *service
account* key instead of an OAuth client, the download was truncated, or you
saved the wrong file. Confirm it begins with `{"installed":` and re-download
from **Credentials → your client → Download JSON** if not.

Service accounts do not work here at all: they cannot access a consumer Gmail
mailbox without domain-wide delegation, which requires Google Workspace.

### `gmail: no cached token: <path>: run \`mail-muncher auth --account personal\` first`

`auth` has never completed for this account. Run it. If you *did* run it, check
that the `token_file` path in your config is the one `auth` reported writing.

### `gmail: stored token was rejected for account "personal" ...`

```
gmail: stored token was rejected for account "personal" (revoked, expired, or the OAuth client changed): re-run `mail-muncher auth --account personal`
```

The refresh token is no longer accepted. In order of likelihood:

1. **The seven-day Testing-mode expiry.** See
   [above](#the-seven-day-refresh-token-expiry). If this failed almost exactly a
   week after it started working, this is why.
2. **You created a new OAuth client** or deleted the old one. A token is bound
   to the client that issued it. Re-run `auth`.
3. **Access was revoked** at <https://myaccount.google.com/permissions>, or the
   account password changed.
4. **The token file was replaced** with one from a different account or client.

The fix is always the same: `mail-muncher auth --account <name>`. It requests
offline access and forces the consent prompt, so it always comes back with a
fresh refresh token rather than an access-token-only grant.

### `gmail: refresh token for account "personal": ...`

Distinct from the message above, and it means the opposite: the refresh
*failed* but Google did not reject the token itself — a network timeout, DNS
failure, or a 5xx. This is transient. Do not re-run `auth`; run again later.

### `gmail: authorization denied by Google: access_denied`

You clicked **Cancel**, or the account you signed in with is not on the **Test
users** list of a Testing-mode consent screen. Add it in
[step 3](#3-configure-the-oauth-consent-screen) and retry.

### `gmail: timed out after 5m0s waiting for the browser redirect; re-run \`mail-muncher auth --account personal\``

Nothing hit the loopback callback within five minutes. Either the browser was
never opened on the printed URL, or it opened somewhere that cannot reach
`127.0.0.1` on the listening port — a browser on a different machine, or a
container without host networking.

### `gmail: authorization redirect had an unexpected state value; re-run \`mail-muncher auth\` and use only the URL it prints`

The redirect did not carry the anti-CSRF `state` value this invocation
generated. Almost always a stale browser tab from a previous `auth` attempt.
Close old tabs, re-run `auth`, and use only the URL it just printed.

### `gmail: listen on 127.0.0.1:0 for the OAuth redirect: ...`

The loopback listener could not bind. Rare — check for a sandbox or firewall
policy that blocks binding to localhost.

### Warning: `google returned no refresh token`

```
google returned no refresh token; unattended runs will stop working when the access token expires
```

Logged at the end of a successful `auth`. The grant carries only an access
token, which dies in an hour. This almost always means the OAuth client is not
a **Desktop app** client. Create one that is
([step 4](#4-create-a-desktop-app-oauth-client)) and re-run `auth`.

### `Gmail API has not been used in project ... before or it is disabled`

[Step 2](#2-enable-the-gmail-api) was skipped, or it was done in a different
project than the one that owns the OAuth client. Enable the Gmail API in the
project shown in the error text.

### Rate limits and quota

mail-muncher retries 429s, 5xx, and rate-limited 403s with exponential backoff
and jitter — five attempts, 500ms base, capped at 30 seconds. A *daily quota
exhausted* 403 is deliberately not retried, because retrying inside one run
cannot help; wait for the quota window to reset.

If you are hitting limits routinely, narrow `gmail.query` so the first scan asks
for less, and make sure the state file is not being deleted between runs (which
forces a full scan every time).

One quota note specific to Spam and Trash: at the default
`gmail.include_spam_trash: false`, full scans never list them, but incremental
cycles still spend one `messages.get` per Spam message to find out that it is
Spam and discard it. The Gmail history API has no way to filter them server-side,
so that call is the price of both sync paths agreeing on which mail exists. It is
a handful of reads a day on a normal mailbox.

## Rotating or revoking access

- **Revoke mail-muncher's access:** <https://myaccount.google.com/permissions>,
  find the app, remove access. Then delete the token file. Existing archived
  mail on disk is untouched.
- **Rotate the client secret:** create a new Desktop app client, replace
  `credentials.json`, delete the token file, and re-run `auth`.
- **Move to another machine:** copy `credentials.json` and `token.json` (both
  0600), or copy only `credentials.json` and run `auth` again on the new
  machine. Copying the state directory as well preserves the sync cursor; not
  copying it just means one full scan.
