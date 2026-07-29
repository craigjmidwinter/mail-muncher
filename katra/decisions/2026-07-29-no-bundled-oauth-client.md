---
title: No bundled OAuth client
date: "2026-07-29"
time: "09:10:00"
tags:
    - oauth
    - adoption
    - security
summary: Users register their own Google OAuth client; we ship none, and cannot
type: decision
status: accepted
---

## Decision
mail-muncher ships **no** OAuth client ID or secret. Every user registers their own Google Cloud project and Desktop-app client. `docs/gmail-setup.md` is a permanent, load-bearing part of the product, not temporary scaffolding.

## Why
The obvious convenience move — bake a client ID into the binary so `auth` works out of the box — is not available to us, and would be a bad idea even if it were.

**It is closed by Google.** `gmail.readonly` is a **restricted scope**. Distributing an app that strangers authorize against means publishing to Production, and Production with a restricted scope requires OAuth verification *plus* a third-party CASA security assessment — paid, renewed annually, priced for companies. Remaining unverified caps the app at 100 test users and blocks everyone else. There is no hobby-project tier here.

**Quota is per-project.** A shared client puts every user on one quota pool, so one person backfilling eighteen months of mail degrades everyone else's cycles. This is why rclone, which does ship defaults, tells users to register their own anyway.

**Single point of failure.** One suspension, one quota exhaustion, one policy change against our project and every installation breaks simultaneously, with no recourse available to the user.

**The secret would not be secret.** In an installed application the client secret is extractable by definition — that is precisely why the flow uses PKCE. Shipping one lets anyone stand up a consent screen reading "mail-muncher wants access to your Gmail." That is a phishing shape, and our name would be on it.

**User-owned is the better trust story anyway.** For a tool that reads mail, "the credentials never touch the maintainer, and you can revoke from your own console without asking anyone" is stronger than any convenience a shared client would buy.

## Consequences
- **Onboarding friction on the Gmail path is permanent and structural.** It cannot be engineered away; it can only be explained well and routed around.
- **This raises IMAP from a nice-to-have to the primary low-friction path.** An app password involves no client, no consent screen, and no verification — it is the only fast path that exists. See [[imap-app-password-as-the-primary-onboarding-path]].
- **Unconfigured output has to carry real weight.** Since first-run cannot "just work", the tool must explain on first contact what is missing, what the two provider options cost, and what to run next. See [[unconfigured-first-run-guidance]].
- The 7-day refresh-token expiry on Testing-mode consent screens follows directly from this and is likewise structural. Document it loudly; do not pretend a workaround exists.
- If Google ever offers a verification path proportionate to an open-source tool, revisit — but not before, and not by shipping an unverified client and hoping.
