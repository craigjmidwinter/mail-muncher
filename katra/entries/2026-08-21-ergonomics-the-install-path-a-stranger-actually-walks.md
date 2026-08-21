---
title: 'Ergonomics: the install path a stranger actually walks'
date: "2026-08-21"
time: "08:20:46"
tags:
    - ergonomics
    - standards
    - docs
    - install
hash: 4ab4cbd
stat:
    f: 14
    a: 723
    d: 92
summary: Leg G found a manual-edit step on the primary path, a cp that never worked, and no uninstall at all
closes:
    - ergonomics-pass-install-failure-paths-upgrade-uninstall
---

PROJECT-STANDARDS grew an ERGONOMICS section after this project had already
been made an exemplar behind it, which is a good way to find out what your docs
only *look* like they do. The rule that did the damage: **no manual config file
editing before first success.**

## The primary path told you to open an editor

Quickstart step 3 read, in full: "**Fill in three values.** Open the file
`init` just wrote and set `imap.host`, `imap.username` and
`imap.password_cmd`." That is on the IMAP path — the one the README leads with,
the one labelled ~2 min, the one the bundled skill drives for an agent. `init`
wrote `imap.example.com` and `you@fastmail.com` placeholders and then told you
to go fix them by hand.

The odd part is that `init` was already interactive. It asked for account name
and destination. Host, username and password command are exactly as answerable
as those two; nobody had gone back and asked why they were not.

Now they are. `init` prompts for all three, offers the platform's own secret
store as the password-command default (Keychain, `secret-tool`, `pass`), and
takes `--host`, `--username` and `--password-cmd` for scripting. Under `--yes`
it refuses rather than writing a placeholder:

```
error: --host and --username required with --yes --provider imap; host and
username have no honest default to take. Run `mail-muncher init --provider
imap` without --yes to be prompted instead
```

That refusal is the interesting design call. The alternative was to keep
writing placeholders under `--yes` and keep the edit step for that one case.
Refusing is better because it makes a promise the tool can keep: **every config
`init` writes validates on the first try.** There is no longer a state where
`init` succeeds and the config is not usable.

The edit instruction is now gone from README, `docs/index.md`, and both files
of the bundled skill. That last one mattered most and was nearly missed — the
skill is how an *agent* installs this, so a stale "then edit three keys" line
there is a hand-edit instruction given to something that cannot open an editor.

## A cp that has never worked

`contrib/launchd/com.craigmidwinter.mail-muncher.plist` — no `j`. Its own
embedded install instructions say `cp contrib/launchd/com.craigjmidwinter.mail-muncher.plist`
— with the `j`. So has its `<key>Label</key>`. Commit `cd0d8e9` fixed the
module path and URLs when the account name turned out to be `craigjmidwinter`,
and fixed the *contents* of this file, and missed the filename.

Copy-pasted from a fresh checkout, that command has always failed with "No such
file or directory", and the `launchctl load` and `unload` lines that follow
name a file that was never created. Renamed the file; every other spelling in
the repo already agreed with the new name. Verified by actually pasting the
block into a shell against a throwaway `HOME` and running `plutil -lint` on the
result, rather than by reading it again.

## Nothing said how to remove it

There was no uninstall documentation of any kind. One passing mention of `brew
upgrade` was the entire lifecycle story. Meanwhile a full install leaves:

```
~/.config/mail-muncher/          config.yml, credentials.json, token.json
~/.local/state/mail-muncher/     sync cursors, two lockfiles, quarantine/
~/Library/LaunchAgents/…plist    if you followed the scheduling section
~/Library/Logs/mail-muncher.*    which that plist creates
a macOS Keychain entry           which the quickstart itself tells you to make
```

The Homebrew cask carries `# No zap stanza required`, so `brew uninstall`
removes the binary and none of the above. The Keychain entry is the one that
stings: the quickstart walks you through creating a credential and nothing ever
mentions deleting it.

The new section removes all of it, in order of sensitivity — credential first,
binary last. It was written against an inventory built by installing into a
sandbox `HOME` and enumerating what appeared, then machine-checked for
coverage, not written from memory of what the tool probably creates.

Archived mail is deliberately excluded, and says so. Files this tool has
already written are the user's, and an uninstaller is a bad place to discover
that a project disagrees.

## Windows: not a clean failure

Windows appeared nowhere in the README. The standard asks for unsupported
platforms to be *named*, and the reason why is visible in what actually
happens: this is not a clean failure.

`go install` cross-compiles fine — no cgo, no platform build tags outside a
test file. The binary starts. `init` succeeds, and `defaultPasswordCmd` is even
careful enough to seed `pass` rather than a macOS or Linux secret tool. Then
the first `run` dies, because `internal/provider/imap/password.go:60` hands
every `password_cmd` to a hardcoded `/bin/sh -c` that a stock Windows box does
not have. Late failure, blaming the password manager.

Gmail has no such dependency; its OAuth flow already branches to `rundll32`.
So the honest statement is not "Windows is unsupported" but the more specific
"IMAP cannot work, Gmail might, the container and WSL2 do" — which is what the
new section says.

## What could not be verified live, and is labelled as such

Both fabric runners were down for the whole pass — `fabricvm` and `windesk`,
last heartbeat about 15.6 hours before, jobs queued and never claimed. Two legs
posted jobs, watched them sit, and reported the failure instead of inventing a
result; one of them caught its own unverified claim of a successful post after
being challenged on it. Docker Desktop was started as a fallback and its engine
never came up either.

So the Linux and Windows findings are static analysis — read from
`.goreleaser.yml`, the Dockerfile, and the Go source — and every one of them is
marked that way rather than dressed up as a test run. The specific claims were
re-verified by hand before being written into the docs: `CGO_ENABLED=0` (so the
binaries are static and the Alpine-for-`/bin/sh` framing is exactly right), the
archive `name_template` matching the README's `curl` URL character for
character, and the `/bin/sh` line in `password.go`.

The failure-path work that *did* get exercised was done on darwin: the no-root
install, and the permission error. That second one corrected a draft of the
docs — the message names a scratch file, `install: /usr/local/bin/INS@LPh1Hz:
Permission denied`, not `mail-muncher`, which is confusing enough that quoting
it wrongly would have been worse than not quoting it at all.
