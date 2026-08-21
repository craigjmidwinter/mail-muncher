---
title: init's one-liner silently upgrades an existing install
date: "2026-08-21"
time: "08:24:32"
tags:
    - ergonomics
    - install
summary: brew install on an already-installed cask upgrades with no confirmation; found the hard way
type: task
status: todo
effort: S
epic: adoption-and-onboarding
---

Found during the 2026-08-21 ergonomics pass, by doing it to a real machine.

## What happened
An agent following README's documented Homebrew one-liner verbatim —

    brew install craigjmidwinter/tap/mail-muncher

— on a machine that already had the cask at 0.3.0 **silently upgraded it to 0.4.0**. No prompt, no "already installed", no confirmation. This is stock `brew install`-on-outdated-package behaviour, not something the formula does wrong, and the upgrade was reverted afterwards.

## Why it is worth a line in the docs
For a stranger there is nothing to protect and the behaviour is fine. For an existing user — or an agent acting on their behalf, which is this project's whole premise — "run the install command" and "change the version you are running" are different intentions, and the docs currently present only one command for both.

The new **Upgrade** section already documents `brew upgrade mail-muncher` as the deliberate way to move versions. What is missing is the note next to the install one-liner that it is *also* an upgrade if you already have it, so nobody discovers that by having it happen.

## Shape of the fix
One sentence under the Homebrew block. Something like: run on a machine that already has it, this upgrades to the latest release without asking; `brew list --versions mail-muncher` first if you care which version you are on.

Not urgent, and deliberately not bundled into the ergonomics commit — that commit was already large and this is a docs nicety rather than a broken path.
