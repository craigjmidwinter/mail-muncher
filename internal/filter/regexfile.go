package filter

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

// Pattern lists are the second kind of externally-owned file a rule can read,
// and they fail in the opposite direction from the first.
//
// # The hazard is breadth, not ReDoS
//
// Go's regexp package implements RE2: no backreferences, no lookaround, and —
// the part that matters here — **no backtracking**. Matching is linear in the
// length of the input, so a pathological or hostile pattern arriving from a
// file mail-muncher does not own cannot cause catastrophic backtracking. There
// is no ReDoS to harden against on this path; do not add a matching timeout, a
// pattern-length cap or a complexity budget under that heading.
//
// The real hazard is the mirror image of the domain-file one. A typo in a
// domain list matches *nothing*: the cost is silence, and you notice because
// mail you wanted did not arrive. A typo in a pattern list can match
// *everything*: `.*`, `^`, `x?`, `(?i)` and an empty pattern all match every
// address there is, so one bad line can hand an entire mailbox to a rule and
// archive the whole account to disk. Silence is recoverable; a full mailbox
// written into someone's dest is not.
//
// So the guards below are breadth guards:
//
//   - An empty pattern is refused. It matches everything.
//   - A pattern that matches the empty string is refused. Matching is
//     unanchored, so matching "" means matching every string — the same
//     catch-all by a longer route. A caller who genuinely wants a catch-all can
//     write one that requires at least one character.
//   - A pattern that does not compile is refused *by itself*, naming its line,
//     leaving the rest of the file in force. A generated file with one bad
//     entry should lose one subscription, not all of them.
//   - The number of patterns loaded is logged for every file on every cycle, so
//     a file that fell from twelve patterns to one is visible in the run output
//     rather than discovered by way of a full disk.
//
// A single `.` still matches every non-empty address and is not refused: the
// empty-string test is a precise rule, and widening it into a guess about which
// short patterns are "too broad" would start rejecting legitimate ones. The
// per-cycle count log is the backstop for that case.

// PatternFileStats is one read of a pattern list, accounted for: what ended up
// in force, and what the breadth guards above refused.
//
// It exists because "eleven of your twelve patterns were refused" is a fact
// only the parser knows, and a caller that is *reporting* on a file — `validate`,
// the MCP server's list_rules — needs the number, not just the survivors. The
// survivors alone cannot be diffed back into the count without a second copy of
// this file's line conventions living somewhere else, and a second copy is a
// second thing to keep in step.
//
// Every field describes the same single read of the same bytes, so the counts
// can never disagree with Patterns.
type PatternFileStats struct {
	// Patterns are the compiled patterns in force, in file order, de-duplicated.
	// Regexp.String returns the source text each was compiled from, so a caller
	// can report the line as the owning program wrote it.
	Patterns []*regexp.Regexp

	// Rejected counts the lines the breadth guards refused: they do not compile,
	// or they would match every message. A repeat of a bad line is counted every
	// time it appears, because every one of them was refused; a repeat of a good
	// line is not a rejection, because it was accepted and then collapsed.
	Rejected int

	// Total counts every line the parser considered — blank lines and full-line
	// comments excluded, duplicates included.
	//
	// It is deliberately not len(Patterns)+Rejected: duplicates of an accepted
	// pattern are read, accepted, and then collapsed into the one entry already
	// in Patterns. "%d of %d rejected" reads off Rejected and Total; how many
	// distinct patterns are in force reads off len(Patterns).
	Total int

	// Err is the degradation this read produced, and is nil only when every line
	// of the file is in force. It is the same error Files.Degraded reports for
	// this path: it names the offending lines, bounded by maxReportedBadLines,
	// and it is also non-nil when the scan was truncated (a line over the 1 MiB
	// cap), which is a case where Rejected is 0 but the tail of the file was
	// never seen.
	Err error
}

// ReadPatternFile reads and parses one `from_regex_file` pattern list, now.
//
// It is the exported form of the accounting: one os.ReadFile and one parse, so
// the returned patterns and the returned counts describe the same bytes and
// cannot drift apart. Deriving the count any other way — re-reading the file and
// diffing it against the patterns that loaded — is two copies of this file's
// line conventions in two packages, and only one of them has the tests.
//
// There is no receiver on purpose: this is the call-time read, outside the
// per-cycle cache that Files keeps. An agent asking "what am I subscribed to
// right now" must see the file as it is on disk this instant, not as it was when
// the current cycle started.
//
// The returned error is the *read* failing — the file does not exist yet, is
// unreadable, is a directory. That is not the same fact as the file parsing
// badly, and callers render it differently: a file the owning program has not
// written yet is an empty subscription with a note, while a file full of
// uncompilable lines is a subscription that is quietly smaller than intended.
// Parse degradation is therefore never returned here; it is PatternFileStats.Err,
// next to the counts that quantify it.
//
// There is deliberately no ReadDomainFile beside this. "Rejected" is not a
// concept a domain list has: no entry can fail to compile, and an entry that
// does not look like a domain is kept and logged rather than refused, so the
// only honest count a domain file could report is the one Files.Domains already
// returns the list for. Adding a symmetrical function would mean adding a
// Rejected field that is structurally always zero, which reads as a promise the
// format cannot break.
func ReadPatternFile(path string) (PatternFileStats, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PatternFileStats{}, err
	}
	// Logging off: a call-time query is not a cycle. The "pattern list loaded"
	// counter below is the pipeline's early warning that a generator broke, and
	// emitting one per list_rules call would dilute the one signal that means it.
	return patternFileStats(data, path, false), nil
}

// parsePatternList parses a newline-delimited list of RE2 patterns and compiles
// each one, once. source only appears in log lines and in the returned error.
func parsePatternList(data []byte, source string) ([]*regexp.Regexp, error) {
	return parsePatterns(data, source, true)
}

// parsePatterns is parsePatternList with the logging made optional, so
// `validate` can report on a file without also emitting the per-cycle load
// counters that belong to a running pipeline.
//
// Rejected lines are skipped and collected: the returned slice holds every
// pattern that compiled and passed the breadth guards, and the returned error —
// non-nil only when something was rejected — names the offending lines. Callers
// use the slice and report the error as degradation.
func parsePatterns(data []byte, source string, log bool) ([]*regexp.Regexp, error) {
	stats := patternFileStats(data, source, log)
	return stats.Patterns, stats.Err
}

// patternFileStats is the parser. Everything else in this file that reads a
// pattern list is a wrapper over it — parsePatterns keeps the two-value shape
// the cache and `validate` were written against, ReadPatternFile hands the whole
// accounting out — so there is exactly one place that knows what a line means.
func patternFileStats(data []byte, source string, log bool) PatternFileStats {
	var (
		out      []*regexp.Regexp
		seen     = make(map[string]bool)
		reasons  []string
		rejected int
		total    int
		line     int
	)

	sc := newListScanner(data)
	for line = 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())

		// A `#` introduces a comment only at the start of a line. Anywhere else
		// it is a literal character in the pattern: truncating a regex at a `#`
		// the way the domain parser truncates a domain would silently change
		// what the pattern matches, and silently changing what an
		// externally-owned pattern matches is precisely what this file format
		// cannot afford. Trailing comments are therefore not supported here.
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		total++

		re, err := compileListPattern(text)
		if err != nil {
			rejected++
			if len(reasons) < maxReportedBadLines {
				reasons = append(reasons, fmt.Sprintf("line %d: %v", line, err))
			}
			if log {
				slog.Warn("pattern list entry rejected; the rest of the file is still in force",
					"path", source, "line", line, "pattern", text, "error", err)
			}
			continue
		}
		if seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, re)
	}

	if log {
		// Per file, per cycle, always — including on a clean read. This line is
		// the only thing standing between "the generating program emitted one
		// catch-all instead of twelve patterns" and "the whole mailbox is on
		// disk".
		slog.Info("pattern list loaded",
			"path", source, "patterns", len(out), "rejected", rejected)
	}

	stats := PatternFileStats{Patterns: out, Rejected: rejected, Total: total}
	switch {
	case sc.Err() != nil:
		// Truncation wins: the rest of the file was never read, so a rejection
		// count for the part that was is the smaller of the two problems.
		stats.Err = fmt.Errorf("truncated at line %d: %w", line, sc.Err())
	case rejected > 0:
		if rejected > len(reasons) {
			reasons = append(reasons, fmt.Sprintf("and %d more", rejected-len(reasons)))
		}
		stats.Err = fmt.Errorf("%d of %d patterns rejected (%s)",
			rejected, total, strings.Join(reasons, "; "))
	}
	return stats
}

// compileListPattern compiles one line of a pattern list and applies the
// breadth guards described at the top of this file. The error it returns is
// reported against the line the pattern came from.
func compileListPattern(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, errors.New("pattern is empty, which would match every message")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regular expression: %w", err)
	}
	if re.MatchString("") {
		return nil, errors.New("pattern matches the empty string, so it would match every message; require at least one character")
	}
	return re, nil
}
