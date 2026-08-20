---
title: 'from_regex_file: externally-owned patterns'
date: "2026-07-29"
time: "16:20:00"
tags:
    - filters
    - agents
summary: A second program-owned predicate, for senders whose host cannot be predicted
type: task
status: doing
effort: M
epic: filter-engine
---

## Context
Raised by a real consumer building against mail-muncher's output.

The design bet is that **the filter config does not own the data it filters on**. Exactly one predicate honours that today: `from_domains_file`. Everything else — `from_regex`, `to_regex`, `subject_regex`, `header` — lives inline in the config, so a program that wants to subscribe to a *pattern* rather than a *domain* has to get a human to edit YAML. That is the friction the whole project exists to remove.

The motivating case: a company mails from a host that cannot be predicted in advance. Recruiting mail for one employer might arrive from `wagepoint.teamtailor.com`, `mail.wagepoint.com`, or `notifications@wagepoint-hr.example`. A domain list cannot express "any host containing wagepoint" — it can only enumerate hosts already known. The generating program knows the company name and could emit the pattern; it just has nowhere to put it.

## Spec
Add a `from_regex_file: path` predicate to `internal/filter`, mirroring `from_domains_file` in every respect that already works.

- **File format**: one RE2 pattern per line. Trim whitespace; ignore blank lines and `#` comments. Do not lowercase — a regex is case-sensitive by construction and `(?i)` is how a caller asks otherwise.
- **Matching semantics identical to `from_regex`**: each pattern is tested against each bare From addr-spec (`model.Message.FromAddresses()`), and the predicate matches if any pattern matches any address.
- **Re-read at the start of every cycle**, through the same per-cycle cache as the domain-file loader. Two rules naming the same file read it once.
- **Missing or unreadable file**: matches nothing, and reports degradation through the existing mechanism so `on_degraded_filter` governs whether the cursor advances. Do not invent a second policy.
- **A single invalid pattern must not discard the whole file.** Skip that line, report it as degradation naming the line number and the compile error, and keep the patterns that did compile. A generated file with one bad entry should lose one subscription, not all of them.
- Compile each pattern once per read, not per message.

### The failure mode that differs from domain files — handle it deliberately
A typo in a domain file matches **nothing**; the cost is silence. A typo in a regex file can match **everything** — `.` or `.*` or an empty pattern will claim every message in the mailbox, and with a broad rule that means archiving the entire account.

That asymmetry deserves a guard, not just a sentence in the docs:

- Reject an empty pattern outright.
- **A pattern that matches the empty string matches every address.** Detect that (compile it and test against `""`) and refuse it by default, reporting it like an invalid pattern. If a caller genuinely wants a catch-all they can write one that requires at least one character.
- Log the count of patterns loaded per file per cycle, so a file that suddenly went from 12 patterns to 1 catch-all is visible in the run summary rather than discovered by way of a full mailbox on disk.

Note in a code comment that Go's RE2 has no backtracking, so a pathological pattern from an external file cannot cause catastrophic backtracking. Over-broad matching is the real hazard here, not ReDoS — say so, or someone will later "harden" the wrong thing.

### Keep the door open, but do not walk through it
Refactor the domain-file loader so the per-cycle caching, the missing-file reporting and the degradation plumbing are generic over "a list of things parsed from a file". `to_regex_file` and `subject_regex_file` then become small additions if anyone asks.

**Do not** build a general "include an arbitrary match subtree from a file" mechanism. That hands an external file the power to define `not:` trees and nested combinators, which is far more authority than the use case needs and much harder to reason about when the file is machine-generated.

## Acceptance
- Table-driven tests: parsing (comments, blanks, whitespace, case preserved), matching against multiple From addresses, multi-pattern files, reload picking up a file changed between cycles, missing file matching nothing without erroring.
- One invalid pattern is skipped while the rest still match, and the degradation names the line.
- An empty pattern and an empty-string-matching pattern are both refused.
- `validate` reports a file whose patterns cannot compile, and a missing file stays a warning — it may not exist yet.
- `docs/filters.md` documents it beside `from_domains_file`, including the over-broad hazard and why it is treated differently from a domain typo.
