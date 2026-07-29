# Filters: the match-tree language

Load this when writing or debugging a rule's `match:`. Full reference and
cookbook: <https://craigjmidwinter.github.io/mail-muncher/filters>.

## Shape

Rules are evaluated in config order against every fetched message and the
**first match wins** — a message is written by exactly one rule, or by none.
Put narrow rules above broad ones.

A `match:` value is a mapping with **exactly one key**: a combinator or a
predicate. Two keys in one mapping is a compile error telling you to combine
them with `all:` or `any:`. Regexes and durations compile when the config
loads, so a bad pattern is a `validate` failure, not a 3am surprise.

## Combinators

| Key | Value | Matches when |
| --- | --- | --- |
| `all` | list of nodes | every child matches (at least one child required) |
| `any` | list of nodes | at least one child matches (at least one child required) |
| `not` | a single node | the child does not match |

```yaml
match:
  all:
    - any:
        - from_domains: [acme.com]
        - from_domains_file: ~/.local/share/agent/domains.txt
    - not:
        subject_regex: "(?i)^\\[newsletter\\]"
```

## Predicates

| Key | Value | Matches when |
| --- | --- | --- |
| `from_domains` | list of domains | any `From` address's domain equals or is a subdomain of a listed domain |
| `from_domains_file` | path | same, with the list read from an externally-owned file each cycle |
| `from_regex` | RE2 pattern | the pattern matches any `From` addr-spec (no display name) |
| `to_regex` | RE2 pattern | the pattern matches any `To` or `Cc` addr-spec |
| `subject_regex` | RE2 pattern | the pattern matches the decoded `Subject` |
| `header` | `{name: X-Foo, regex: ...}` | the pattern matches any value of that header |
| `has_attachment` | `true` / `false` | the message does (or does not) carry a real attachment |
| `label` | label name | the message carries that provider label, compared exactly |
| `older_than` | Go duration | the message `Date` is further in the past than the duration |
| `newer_than` | Go duration | the message `Date` is more recent than the duration |

One worked example each:

```yaml
# Mail from a company or any of its subdomains.
- from_domains: [acme.com, globex.io]

# The same list, owned and updated by another program — re-read every cycle.
- from_domains_file: ~/.local/share/jobsearch/domains.txt

# A specific sender, however they capitalize it.
- from_regex: "(?i)^no-?reply@acme\\.com$"

# Anything addressed to a plus-alias handed out to vendors.
- to_regex: "(?i)^me\\+vendors@example\\.com$"

# Application acknowledgements, case-insensitively.
- subject_regex: "(?i)(your application|application received)"

# Everything a mailing list tags.
- header: {name: List-Id, regex: "golang-nuts"}

# Only messages that actually carry a file.
- has_attachment: true

# Gmail labels, exactly as shown in the UI. Nested labels use "Parent/Child";
# system labels are upper case (INBOX, SENT, UNREAD, STARRED).
- label: INBOX

# Message Date older than 90 days / newer than a day.
- older_than: 2160h
- newer_than: 24h
```

## Details that trip people up

- `from_regex` and `to_regex` test the **bare address** (`jane@acme.com`), never
  the display name. Use `header: {name: From, regex: ...}` to test the raw
  header including the display name.
- `has_attachment` counts parts marked `Content-Disposition: attachment`.
  Inline images referenced by `cid:` are not attachments.
- `label` is case-sensitive and exact — `label: inbox` does not match `INBOX`.
- `older_than` / `newer_than` compare against the message `Date` header, falling
  back to the provider's internal date when the header is missing or
  unparseable. A message with no usable date matches neither.
- Patterns are Go [RE2](https://github.com/google/re2/wiki/Syntax): no
  backreferences, no lookaround. Prefix with `(?i)` for case-insensitivity. In
  YAML prefer double quotes and escape backslashes (`"\\."`), or use single
  quotes where no escaping is needed.
- Use `true` / `false` for `has_attachment`. YAML 1.2 treats `yes` and `no` as
  strings and mail-muncher rejects them.
- `gmail.query` is **not** a filter on what gets kept. It only bounds what a
  full scan asks Gmail for. The rules are the only authority.

## Domain-list file format

`from_domains_file` names a file mail-muncher does not own, does not create,
and never writes.

```
# ~/.local/share/jobsearch/domains.txt
# written by the job-search tracker

acme.com
globex.io          # inline comments are fine
@initech.com       # a leading @ is stripped
MAIL.Umbrella.COM  # case is irrelevant
```

- **Read once per cycle, on first use** — not per process, not per message. A
  file referenced by several rules is read once and shared.
- **Missing or unreadable is never fatal.** The predicate matches nothing and
  one warning is logged for that file for that cycle. See `on_degraded_filter`
  for what the cycle does about the cursor.
- **Liberal parsing.** One entry per line; `#` starts a comment; blank lines
  skipped; whitespace trimmed; a leading `@` and a trailing `.` stripped;
  lowercased; duplicates collapsed. An entry with no dot is **kept** and logged
  as suspicious rather than dropped — the file belongs to someone else and
  guessing wrong should not silently discard an entry.
- **Equality or subdomain.** `acme.com` matches `acme.com` and
  `careers.acme.com`, not `notacme.com`.

The same matching rules apply to the inline `from_domains:` predicate; the only
difference is who owns the list.

## Debugging a rule that will not fire

```bash
mail-muncher run --dry-run --log-level debug
```

This logs the winning rule name (or "no match") for every message, fetches and
evaluates exactly as a real run, and writes nothing.

Check in this order:

1. Is an earlier rule claiming the message? First match wins.
2. Is the rule scoped to the wrong `account`?
3. Is a `from_domains_file` missing? `mail-muncher validate` reports it as a
   warning naming the exact path.
4. Is the regex anchored or case-sensitive when it should not be?
5. Was the message even fetched? A narrow `gmail.query` can starve a full scan.
