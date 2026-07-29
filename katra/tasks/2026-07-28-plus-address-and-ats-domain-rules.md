---
title: Plus-address and ATS-domain rules
date: "2026-07-28"
time: "16:44:00"
tags:
    - jobsearch
    - filters
summary: Catch mail the domain list structurally cannot — plus aliases and shared ATS senders
type: task
status: todo
effort: S
epic: job-search-integration
---

## Context
`from_domains_file` has two structural blind spots. Neither is a gap in the list — both are cases the sender-domain predicate cannot express at all, so no amount of maintaining the list closes them.

**Plus aliases.** Mail sent to `you+something@gmail.com` is identifiable by *recipient*, not sender: the alias appears in the To/Cc headers and leaves no trace in the sending domain. A sender-domain rule structurally cannot catch it — an entire thread can pass through the filter untouched even when the domain list is perfectly up to date.

**Shared ATS domains.** Most recruiting mail does not come from the company's domain at all — it comes from `greenhouse.io`, `lever.co`, `ashbyhq.com`, `myworkdayjobs.com`, and friends. A generated per-company domain list will never contain these, and it should not: they are not company-specific. But they are job-search-relevant **by construction** — nobody receives Greenhouse mail incidentally.

## Spec
Both are configuration, not code — the predicates already exist. The deliverable is the right rules in `examples/job-search.yml` plus documentation of *why* each is there.

- **Plus-address rule**, using `to_regex` (which matches To **and** Cc):
  ```yaml
  - name: plus-alias
    match:
      to_regex: '\+[a-zA-Z0-9._-]+@gmail\.com$'
    dest: ~/Mail/job-search
    formats: [eml, markdown]
  ```
  Catches every `+tag` alias regardless of sender. Note Gmail ignores dots in the local part, so do not anchor on an exact spelling of the base address.

- **ATS domains** as a static `from_domains` list in the same rule tree as the generated file — these are permanent and belong in config, not in the generated list:
  ```yaml
  any:
    - from_domains_file: ~/.local/share/jobsearch/domains.txt
    - from_domains: [greenhouse.io, lever.co, ashbyhq.com, myworkdayjobs.com, jobvite.com, smartrecruiters.com, workable.com, breezy.hr, teamtailor.com, recruitee.com]
  ```
  Subdomain matching is already the semantics, so `mail.greenhouse.io` and `no-reply.eu.greenhouse.io` both hit.

- **Ordering matters** — rules are first-match-wins. Put these where their `dest` is what you want; if they share a `dest` with the domain-file rule, order is irrelevant and a single rule with `any:` is simpler.

## Acceptance
- `examples/job-search.yml` carries both, commented with the reasoning above (not just the syntax).
- `docs/filters.md` cookbook gains a "mail the sender-domain list cannot catch" section covering both cases.
- Verified against real archived mail: confirm the ATS list actually matches what recruiting mail in the account looks like, and correct the list from evidence rather than from this spec's guesses.
