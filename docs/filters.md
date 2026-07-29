---
title: Filters
layout: default
nav_order: 4
description: >-
  The complete mail-muncher match-tree language — combinators, predicates and
  what each one can see — plus a cookbook of filter rules that do real work.
---

# Filters

The complete match-tree language, and a cookbook of rules that do real work.

- [How rules are evaluated](#how-rules-are-evaluated)
- [Match nodes](#match-nodes)
- [Combinators](#combinators)
- [Predicates](#predicates)
- [What a predicate can see](#what-a-predicate-can-see)
- [Cookbook](#cookbook)
- [Debugging a rule](#debugging-a-rule)

## How rules are evaluated

For each fetched message, mail-muncher walks `rules` in config order and stops
at the first rule that both applies to the message's account and whose match
tree accepts it. That rule's `dest` and `formats` decide where the message
lands. No later rule sees it.

```yaml
rules:
  - name: offers          # narrow, so it goes first
    match:
      all:
        - from_domains_file: ~/.local/share/jobsearch/domains.txt
        - subject_regex: "(?i)\\boffer\\b"
    dest: ~/Mail/offers

  - name: job-search      # everything else from those companies
    match:
      from_domains_file: ~/.local/share/jobsearch/domains.txt
    dest: ~/Mail/job-search
```

Swap those two and `offers` becomes unreachable — `job-search` claims
everything first. **Order narrow to broad.**

A message no rule claims is not stored, and the sync cursor advances past it
anyway: it is evaluated exactly once, ever. Changing your rules does not
retroactively archive old mail. To re-evaluate a window of history, delete the
account's state file, which re-arms `initial_lookback` and re-scans. Everything
already on disk is skipped, so this is cheap in disk and safe to repeat.

## Match nodes

A match node is a **YAML mapping with exactly one key**. The key is either a
combinator or a predicate; the value's shape depends on the key.

```yaml
match:
  from_domains: [acme.com]        # one predicate
```

```yaml
match:
  all:                            # a combinator over more nodes
    - from_domains: [acme.com]
    - has_attachment: true
```

Two keys in one mapping is a compile error, because YAML gives no ordering
guarantee and the intent is ambiguous:

```yaml
# WRONG
match:
  from_domains: [acme.com]
  has_attachment: true
```

```
error: rules[0].match: a match node must have exactly one key, got 2 (from_domains, has_attachment); combine them with all: or any:
```

Everything in the tree — every regex, every duration — is compiled when the
config loads. A malformed pattern fails `validate`, not the 3am cron run.
Errors name their position inside the tree:

```
error: rules[0].match: any[1].header.regex: invalid regular expression "([a": error parsing regexp: missing closing ]: `[a`
```

YAML anchors and aliases are resolved before compilation, so shared subtrees
work:

```yaml
rules:
  - name: offers
    match: &from-companies
      from_domains_file: ~/.local/share/jobsearch/domains.txt
    dest: ~/Mail/offers
  - name: everything-else
    match: *from-companies
    dest: ~/Mail/job-search
```

## Combinators

| Key | Value | Matches when |
| --- | --- | --- |
| `all` | list of match nodes | every child matches |
| `any` | list of match nodes | at least one child matches |
| `not` | a single match node | the child does not match |

`all` and `any` require **at least one** child:

```
error: rules[0].match: all: must contain at least one match node
error: rules[0].match: any: expected a list of match nodes, got a mapping
```

`not` takes a node, not a list — note the indentation:

```yaml
match:
  not:
    from_domains: [noreply.example.com]
```

Nest them freely:

```yaml
match:
  all:
    - any:
        - from_domains_file: ~/.local/share/jobsearch/domains.txt
        - from_domains: [greenhouse.io, lever.co]
    - not:
        subject_regex: "(?i)^(unsubscribe|newsletter)"
    - newer_than: 4320h
```

`all` short-circuits on the first child that fails and `any` on the first that
succeeds, so cheap predicates first is a real (if minor) optimization. Put
`from_domains` ahead of a heavy regex.

## Predicates

### `from_domains`

```yaml
- from_domains: [acme.com, globex.io]
```

Matches when any `From` address's domain equals a listed domain **or is a
subdomain of one**. `acme.com` matches `jane@acme.com` and
`careers@mail.acme.com`, but not `jane@notacme.com`.

Entries are normalized on load: lowercased, trimmed, a leading `@` stripped, a
trailing `.` stripped. `@Example.COM.` and `example.com` are the same entry.
The message side is normalized the same way, so matching is case-insensitive
throughout.

The list must be non-empty and no entry may be empty:

```
error: rules[0].match: from_domains: must list at least one domain
error: rules[0].match: from_domains[1]: domain must not be empty
```

If a message has no `From` header, mail-muncher falls back to `Sender` — some
automated senders only set that — and if neither yields a domain, the predicate
does not match.

### `from_domains_file`

```yaml
- from_domains_file: ~/.local/share/jobsearch/domains.txt
```

Identical matching semantics to `from_domains`; the difference is who owns the
list. This is the feature the tool exists for: **the file belongs to another
program**, and mail-muncher re-reads it at the start of every cycle.

**File format** — deliberately liberal, because you are not the one writing it:

```
# comments start with #
acme.com
globex.io       # inline comments work too

@initech.com    # a leading @ is stripped
MAIL.Umbrella.COM
  spaced.example.com
acme.com        # duplicates collapse
```

One entry per line. Blank lines are skipped. Surrounding whitespace is trimmed.
Everything is lowercased. An entry containing no dot is **kept** and logged as
suspicious rather than dropped — the file belongs to someone else, and guessing
wrong should not silently discard an entry. At most five such warnings are
logged per read, then a count of the rest.

**Reload semantics:**

- Read **once per cycle, on first use**. Not once per process, not once per
  message. Every `run` re-reads it; every daemon tick re-reads it.
- A file referenced by several rules is read once per cycle and shared.
- A **missing or unreadable file matches nothing** and logs exactly one warning
  for that file for that cycle. It is never fatal — the owning program may not
  have created it yet:

  ```
  level=WARN msg="domain list unavailable; predicate matches nothing this cycle" path=/Users/you/.local/share/jobsearch/domains.txt error="open ...: no such file or directory"
  ```

- At validation time a missing file is a warning, not an error, for the same
  reason.

Because it is re-read per cycle rather than watched, a change takes effect on
the **next** cycle. With `daemon --interval 5m`, that is at most five minutes.
For an immediate pickup, run `mail-muncher run` after writing the file.

The path is `~`/`$VAR`-expanded like every other path in the config, including
inside a nested match tree.

```
error: rules[0].match: any[0].from_domains_file: path must not be empty
```

### `from_regex`

```yaml
- from_regex: "(?i)^no-?reply@acme\\.com$"
```

RE2 pattern tested against each `From` **addr-spec** — the bare address, with
no display name and no angle brackets (`jane@acme.com`). Matches if any `From`
address matches.

To test the raw header including the display name, use
`header: {name: From, regex: ...}` instead.

### `to_regex`

```yaml
- to_regex: "(?i)^me\\+vendors@example\\.com$"
```

Same as `from_regex`, but tested against every `To` **and** `Cc` addr-spec.
Matches if any of them matches. `Bcc` is not visible in received mail and is
not consulted.

### `subject_regex`

```yaml
- subject_regex: "(?i)(your application|application received)"
```

RE2 pattern tested against the decoded `Subject`. Decoding means RFC 2047
encoded-words (`=?UTF-8?B?...?=`) are already turned into UTF-8, so you write
the pattern against what a human reads, not against the wire format.

A message with no `Subject` is tested against the empty string, so
`subject_regex: ""` matches everything — including subject-less mail.

### `header`

```yaml
- header: {name: List-Id, regex: "golang-nuts"}
```

or

```yaml
- header:
    name: X-Spam-Status
    regex: "^Yes"
```

Matches when **any value** of the named header matches the pattern. Both keys
are required and no others are allowed:

```
error: rules[0].match: header: missing key regex
error: rules[0].match: header: unknown key "pattern" (want name and regex)
error: rules[0].match: header.name: must not be empty
```

Header lookup is case-insensitive (`list-id` finds `List-Id`). Values are
RFC 2047-decoded like the subject. Repeated headers keep all their values in
order, and each is tested — which is what makes `Received` searchable:

```yaml
- header: {name: Received, regex: "mx\\.acme\\.com"}
```

Only top-level message headers are visible. Headers on individual MIME parts
are not.

### `has_attachment`

```yaml
- has_attachment: true
```

True when the message carries at least one part marked
`Content-Disposition: attachment`. Inline images referenced by `cid:` are
**not** attachments and do not count — which is what makes
`has_attachment: true` a decent proxy for "someone sent me a document" rather
than "this email has a logo in it".

`has_attachment: false` matches messages with no attachments. It is a
predicate about the message, not a negation of the tree, so it composes
normally.

Use `true` / `false`. YAML 1.2 treats `yes`/`no` as strings and they are
rejected:

```
error: rules[0].match: has_attachment: expected true or false, got "yes"
```

### `label`

```yaml
- label: INBOX
```

Matches when the message carries that provider label, compared **exactly and
case-sensitively**. For Gmail:

- System labels are upper case: `INBOX`, `SENT`, `UNREAD`, `STARRED`,
  `IMPORTANT`, `SPAM`, `TRASH`, `DRAFT`, and the category labels
  `CATEGORY_PERSONAL`, `CATEGORY_SOCIAL`, `CATEGORY_PROMOTIONS`,
  `CATEGORY_UPDATES`, `CATEGORY_FORUMS`.
- User labels use the name shown in the Gmail UI. A nested label is
  `Parent/Child`.

Label **ids** are resolved to names once per cycle, so you write the name you
see. A label created after the cycle started falls back to its raw id, which
looks like `Label_17` — a cosmetic edge case, not a correctness one.

`label: inbox` does not match `INBOX`.

```
error: rules[0].match: label: must not be empty
```

### `older_than` / `newer_than`

```yaml
- older_than: 2160h    # Date is more than 90 days ago
- newer_than: 24h      # Date is less than a day ago
```

Go duration strings, compared against the message `Date` header — falling back
to the provider's internal receipt date when the header is missing or
unparseable. Both must be positive.

A message with no usable date at all matches **neither** predicate. That is
intentional: an undated message should not be swept up by an age filter in
either direction.

Go durations have no day unit. Use hours: `24h`, `168h` (a week), `720h` (30
days), `2160h` (90 days).

```
error: rules[0].match: older_than: invalid duration "30d" (want a Go duration such as "720h")
error: rules[0].match: newer_than: duration must be positive, got "0s"
```

`newer_than` is evaluated at message-evaluation time, so under a long-running
daemon it means "newer than N before *now*", not "before the cycle started".

## What a predicate can see

Predicates run against the parsed message, after MIME decoding. What is
available:

| Message field | Read by |
| --- | --- |
| `From` (or `Sender` when `From` is absent) | `from_domains`, `from_domains_file`, `from_regex` |
| `To`, `Cc` | `to_regex` |
| `Subject`, RFC 2047-decoded | `subject_regex` |
| All top-level headers, decoded, repeats preserved | `header` |
| Attachment parts | `has_attachment` |
| Provider labels, resolved to names | `label` |
| `Date`, falling back to provider internal date | `older_than`, `newer_than` |

Not available to any predicate: the message **body**, attachment contents,
message size, and thread membership. Body matching is not implemented. If you
need it, filter on headers to get the message onto disk and grep the markdown
rendering afterwards.

### Regex syntax

Patterns are Go [RE2](https://github.com/google/re2/wiki/Syntax). They are
**unanchored** — the pattern matches anywhere in the value unless you anchor it
with `^` and `$`. No backreferences and no lookaround; RE2 trades those for
guaranteed linear-time matching, so a hostile subject line cannot hang a cycle.

Useful flags: `(?i)` case-insensitive, `(?s)` dot matches newline, `(?m)`
multi-line anchors.

In YAML, prefer double quotes and escape backslashes:

```yaml
- subject_regex: "(?i)\\[urgent\\]"          # double-quoted: \\ for one backslash
- subject_regex: '(?i)\[urgent\]'            # single-quoted: no escaping
```

Single-quoted YAML scalars pass backslashes through unchanged, which is usually
what you want for regexes. Unquoted scalars work too, until the pattern
contains a `:` or `#`.

## Cookbook

### Everything from the companies an external program is tracking

The core pattern. The domains file is written by something else; this rule
never changes.

```yaml
- name: job-search
  match:
    from_domains_file: ~/.local/share/jobsearch/domains.txt
  dest: ~/Mail/job-search
  formats: [eml, markdown]
```

### Two consumers, two mailboxes

Each program gets its own declaration file and its own directory. Neither can
see the other's mail.

```yaml
- name: research-agent
  match:
    from_domains_file: ~/.local/share/agents/research/domains.txt
  dest: ~/mail/agents/research
  formats: [markdown]

- name: support-agent
  match:
    from_domains_file: ~/.local/share/agents/support/domains.txt
  dest: ~/mail/agents/support
  formats: [markdown]
```

If a message matches both files, the first rule wins and the second consumer
never sees it. When overlap matters, make the rules disjoint with `not`, or
give both consumers the same `dest`.

### Company mail, but not their marketing

```yaml
- name: job-search
  match:
    all:
      - from_domains_file: ~/.local/share/jobsearch/domains.txt
      - not:
          any:
            - subject_regex: "(?i)(newsletter|webinar|unsubscribe)"
            - header: {name: List-Unsubscribe, regex: ".+"}
  dest: ~/Mail/job-search
```

`header: {name: List-Unsubscribe, regex: ".+"}` is the idiom for "this header
exists and is non-empty", which is a better bulk-mail signal than any subject
heuristic.

### Applicant tracking systems as well as companies

ATS platforms send from their own domains, not the employer's.

```yaml
- name: job-search
  match:
    any:
      - from_domains_file: ~/.local/share/jobsearch/domains.txt
      - from_domains: [greenhouse.io, lever.co, ashbyhq.com, myworkday.com, smartrecruiters.com]
      - subject_regex: "(?i)(your application|application received|interview)"
  dest: ~/Mail/job-search
```

### Invoices and receipts with a document attached

```yaml
- name: receipts
  match:
    all:
      - any:
          - from_domains: [stripe.com, squareup.com, intuit.com]
          - subject_regex: "(?i)\\b(invoice|receipt|payment)\\b"
      - has_attachment: true
  dest: ~/Mail/receipts
  formats: [eml, markdown]
```

The markdown sink extracts each attachment to
`<basename>.attachments/`, so the PDFs end up as ordinary files next to a
document that links them.

### One mailing list

```yaml
- name: golang-nuts
  match:
    header: {name: List-Id, regex: "golang-nuts\\.googlegroups\\.com"}
  dest: ~/Mail/lists/golang-nuts
  formats: [markdown]
```

`List-Id` is far more reliable than the `From` address, which list software
rewrites.

### Mail to a plus-alias you hand out

```yaml
- name: vendor-alias
  match:
    to_regex: "(?i)^you\\+vendors@example\\.com$"
  dest: ~/Mail/vendors
```

### Only recent, only from a Gmail label

```yaml
- name: recent-flagged
  match:
    all:
      - label: Follow-up/Waiting
      - newer_than: 336h
  dest: ~/Mail/waiting
```

### Catch-all, last

A final rule with a permissive match makes an "everything else" archive. It
must be last, since it claims anything the rules above did not.

```yaml
- name: archive-all
  match:
    not:
      label: SPAM
  dest: ~/Mail/archive
  formats: [eml]
```

Be deliberate: this archives your entire mailbox on the schedule you run. Check
what it will do with `run --dry-run` first.

### Route one account differently

```yaml
- name: work-only
  account: work
  match:
    label: INBOX
  dest: ~/Mail/work
```

A rule with `account:` set is skipped entirely for other accounts. A rule
without one applies to all of them.

## Debugging a rule

**Compile it.** `validate` compiles every match tree and reports the exact
position of the problem:

```bash
mail-muncher validate --config examples/job-search.yml
```

**See the decisions.** Debug logging prints the rule decision for every
message — the winning rule name, or no match:

```bash
mail-muncher run --dry-run --log-level debug
```

**Change nothing while you experiment.** `--dry-run` fetches and evaluates
exactly as a real run does, reports the path each match would take, writes no
files, and does not save sync state. Run it as many times as you like.

**Check the domain file is being found.** A missing file matches nothing and
logs one warning per cycle. `validate` reports it too:

```
warning: rules[0].match.any[0].from_domains_file: file does not exist yet: /Users/you/.local/share/jobsearch/domains.txt (it is maintained by another program; the rule matches nothing until it appears)
```

If the path looks wrong in that warning, the problem is `~`/`$VAR` expansion —
`validate` always prints the expanded path.

### Common surprises

| Symptom | Cause |
| --- | --- |
| A rule never fires | An earlier rule claims the message first. Order narrow to broad. |
| A rule stopped firing after a config change | Rules are evaluated once per message, ever. Old mail is not re-evaluated; delete the state file to re-scan. |
| `label: inbox` matches nothing | Labels are case-sensitive. Use `INBOX`. |
| `has_attachment: true` misses a message with a visible image | Inline `cid:` images are not attachments. |
| A regex matches more than expected | RE2 patterns are unanchored. Add `^` and `$`. |
| `older_than: 30d` fails to compile | Go durations have no day unit. Use `720h`. |
| A message excluded by `gmail.query` was archived anyway | `query` applies to full scans only; rules are the only authority on what is stored. |
| Everything matches | An empty `subject_regex: ""` matches every message, including subject-less ones. |
