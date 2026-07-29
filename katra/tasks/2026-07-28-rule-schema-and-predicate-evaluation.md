---
title: Rule schema and predicate evaluation
date: "2026-07-28"
time: "15:34:49"
tags:
    - filters
summary: Composable match nodes (all/any/not + predicates), ordered rules, first match wins
type: task
status: todo
effort: M
epic: filter-engine
---

## Context
The heart of the flexibility requirement. A rule's `match:` is a tree of combinators and predicates evaluated against the canonical `model.Message`.

## Spec
In `internal/filter`:

A **match node** is a YAML map with exactly one key — either a combinator or a predicate:

- Combinators: `all: [node...]`, `any: [node...]`, `not: node`
- Predicates:
  - `from_domains: [example.com, ...]` — any From address's domain equals (case-insensitive) or is a subdomain of a listed domain (`mail.example.com` matches `example.com`)
  - `from_domains_file: path` — same semantics, list loaded from file (next task)
  - `from_regex: pattern` — RE2 against each full From address (`Name <addr>` rendered as `addr`)
  - `to_regex: pattern` — against To+Cc addresses
  - `subject_regex: pattern`
  - `header: {name: X-Foo, regex: pattern}` — against raw header value(s)
  - `has_attachment: true|false`
  - `label: name` — provider label/folder membership, exact match
  - `older_than: 720h` / `newer_than: 24h` — Go duration vs message Date

`Compile(node yaml.Node) (Matcher, error)` where `Matcher` is `func(*model.Message) bool` (or an interface with `Match`). Compile-time errors for: unknown key, multiple keys in one node, invalid regex/duration, empty combinator list. Regexes compile once.

`Engine.Evaluate(msg) *Rule`: walk rules in config order, skip rules bound to a different account, return the **first** whose matcher passes; nil if none. Unmatched messages are not stored (state still advances — a message evaluated once is never re-evaluated).

Wire into `internal/config`: replace the opaque `match:` yaml.Node hook — `validate` now compiles every rule and reports compile errors with rule name + key path.

## Acceptance
- Table-driven unit tests per predicate + nested combinator trees (e.g. `all[any[...], not[...]]`), unknown-key error, first-match-wins ordering, account scoping.
