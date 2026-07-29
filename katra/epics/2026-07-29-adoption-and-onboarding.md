---
title: Adoption and onboarding
date: "2026-07-29"
time: "09:12:00"
tags:
    - adoption
    - ux
summary: Cut time-to-first-email; make the unconfigured state self-explanatory
type: epic
status: planned
horizon: now
---

The tool works. Almost nobody will get far enough to find that out.

To try mail-muncher today a stranger must create a Google Cloud project, enable an API, configure a consent screen, create a Desktop client, download JSON, and register themselves as a test user — all before seeing a single message. Then the token expires in seven days. Most people leave at step two; the ones who don't churn a week later.

Per [[no-bundled-oauth-client]] that friction cannot be removed on the Gmail path — it is imposed by Google's restricted-scope policy, not by our design. So the strategy is to route around it and to explain it well:

- **Route around it**: IMAP with an app password needs no OAuth client, no consent screen, and no verification. It is the only genuinely fast path, and it simultaneously unlocks Fastmail, Proton Bridge, work accounts and self-hosted mail. That is a much larger audience than Gmail-API-only.
- **Explain it**: an unconfigured run must state plainly what is missing and what the options cost, because the tool cannot make first-run "just work" and pretending otherwise wastes the user's time.
- **Remove the incidental friction that *is* ours**: requiring a Go toolchain to install, and requiring two doc pages to be read before a config exists.

The measure of success is time-to-first-email for someone who has never seen the project, not feature count.
