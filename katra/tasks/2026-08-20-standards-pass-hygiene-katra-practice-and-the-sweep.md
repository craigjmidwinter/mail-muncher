---
title: 'Standards pass: hygiene, katra practice, and the sweep'
date: "2026-08-20"
time: "16:39:49"
tags:
    - standards
    - hygiene
    - security
summary: Gap check against the PROJECT-STANDARDS sections this repo predates
type: task
status: doing
effort: M
---

Gap check against the fleet PROJECT-STANDARDS sections written after mail-muncher was set as an exemplar: PROJECT HYGIENE (tests, lint, deps, release discipline), KATRA practice, and the timeboxed PERFORMANCE & SECURITY SWEEP.

The docs and brand already meet the bar and are out of scope for this pass.

## Scope
- Hygiene: committed linter config, lint in CI, CHANGELOG, release-claim accuracy.
- Katra: task/epic status truthfulness, entry practice.
- Sweep: secrets (tree + history spot-check), govulncheck, unsafe-pattern read, minimal scopes, hot-path sanity.

## Constraint
The working tree carried ~890 lines of uncommitted from_domains_file/from_regex_file guard work from another session throughout this pass. It is not part of this task and must not be committed by it.
