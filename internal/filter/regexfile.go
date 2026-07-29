package filter

import (
	"errors"
	"fmt"
	"log/slog"
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

	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("truncated at line %d: %w", line, err)
	}
	if rejected > 0 {
		if rejected > len(reasons) {
			reasons = append(reasons, fmt.Sprintf("and %d more", rejected-len(reasons)))
		}
		return out, fmt.Errorf("%d of %d patterns rejected (%s)",
			rejected, total, strings.Join(reasons, "; "))
	}
	return out, nil
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
