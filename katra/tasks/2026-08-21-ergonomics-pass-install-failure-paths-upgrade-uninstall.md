---
title: 'Ergonomics pass: install, failure paths, upgrade, uninstall'
date: "2026-08-21"
time: "07:54:52"
tags:
    - standards
    - ergonomics
    - docs
summary: New PROJECT-STANDARDS section; Leg G performed as a stranger on darwin, linux and windows
type: task
status: done
effort: M
epic: adoption-and-onboarding
entry: ergonomics-the-install-path-a-stranger-actually-walks
---

PROJECT-STANDARDS gained an ERGONOMICS section (between PROJECT HYGIENE and KATRA) and a Leg G in PROCESS, after the 2026-08-20 pass had already run. This task is the catch-up.

## What the section demands
- Primary install is one copy-paste command per platform; no manual config before first success; the no-flags invocation does the common thing.
- The install's own failure paths teach: wrong platform, missing dependency, permission error each print the fix in one screen.
- Platform coverage is actually tested, by a person or an agent, not inferred from a CI matrix. Unsupported platforms are named as unsupported.
- Upgrade and uninstall are documented AND tested; uninstall removes everything install created — state, caches, launch agents included.
- Every documented command pastes and runs verbatim.

## Method
Four legs, each acting as a stranger following only the published docs:
- G1 darwin, local, isolated HOME: fresh install, timed first run, upgrade, uninstall, and an empirical inventory of what install actually creates.
- G2 linux, on the fabric runner `fabricvm`: binary-download install, cosign verification, and the three failure paths triggered deliberately rather than read about.
- G3 windows, on the fabric runner `windesk`: what a Windows user actually experiences, with the sharp question being whether `go install` fails cleanly or succeeds misleadingly.
- G4 static: every fenced command in README, docs/, CONTRIBUTING, contrib/ and examples/ classified for copy-paste-ability, plus a naming audit.

## Leads already confirmed before the legs reported
- `contrib/launchd/com.craigmidwinter.mail-muncher.plist` is the filename on disk, but the install instructions embedded inside it say `cp contrib/launchd/com.craigjmidwinter.mail-muncher.plist` — an extra `j` — and its own `Label` is `com.craigjmidwinter.mail-muncher`. The documented copy fails, and the `launchctl load`/`unload` lines name a file that will not exist.
- There is no uninstall documentation anywhere. A grep across README and docs/ finds exactly one mention of moving versions (`brew upgrade mail-muncher`) and nothing at all about removal — while the tool creates config, state, an archive tree, an OAuth token file, and optionally a launch agent.
- Windows appears nowhere in the README, so a Windows reader learns the platform is unsupported by failing rather than by being told.
