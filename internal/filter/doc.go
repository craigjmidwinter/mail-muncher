// Package filter implements the rule engine: it compiles a rule's `match:`
// tree into a Matcher and decides, for each message, which rule (if any) claims
// it.
//
// # Match trees
//
// A match node is a YAML mapping with exactly one key — either a combinator or
// a predicate:
//
//	all: [node, ...]     every child must match (at least one child required)
//	any: [node, ...]     at least one child must match (at least one required)
//	not: node            inverts the child
//
//	from_domains: [example.com, ...]   a From domain equals or is a subdomain of one listed
//	from_domains_file: /path/to/list   same, but the list is read from a file every cycle
//	from_regex: pattern                RE2 against each From addr-spec
//	from_regex_file: /path/to/list     same, but the patterns are read from a file every cycle
//	to_regex: pattern                  RE2 against each To and Cc addr-spec
//	subject_regex: pattern             RE2 against the decoded Subject
//	header: {name: X-Foo, regex: ...}  RE2 against any value of the named header
//	has_attachment: true|false         message carries (or does not carry) an attachment
//	label: name                        provider label/folder membership, exact match
//	older_than: 720h                   message Date is further in the past than the duration
//	newer_than: 24h                    message Date is more recent than the duration
//
// Regexes and durations are compiled once, at Compile time; a malformed tree is
// an error there rather than a surprise at evaluation time. `mail-muncher
// validate` reports those errors against the rule that produced them by way of
// config.MatchValidator.
//
// # Cycles
//
// `from_domains_file` and `from_regex_file` name files another program owns
// (the job-search tracker), so their contents are re-read once per pipeline
// cycle rather than once per process or once per message. The pipeline calls
// Engine.BeginCycle at the top of every run/tick; the next evaluation that
// consults such a file re-reads it. A file that is missing or unreadable
// matches nothing and logs a single warning per cycle — it never fails
// compilation or evaluation, because the owning program may not have created it
// yet.
//
// It is, however, reported: Engine.Degraded lists every file the cycle could not
// read in full, and Engine.Preload reads them all up front so the list is
// complete before the first message is evaluated. "Matched nothing" and "could
// not be evaluated" look identical to a matcher and are opposite facts to a
// caller deciding whether it is safe to advance a sync cursor past the mail it
// just skipped — see the pipeline's on_degraded_filter policy.
//
// Files is generic over "a list of things parsed from a file": one per-cycle
// cache, one warning path and one degradation map serve every file-valued
// predicate, so a new one costs a parser and a case in compileNode. Pattern
// lists carry a hazard domain lists do not — an over-broad pattern matches
// every message rather than none — and the guards for that live in
// regexfile.go, which also explains why RE2 makes ReDoS the wrong thing to
// worry about here.
package filter
