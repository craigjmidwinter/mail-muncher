package filter

import (
	"bufio"
	"bytes"
	"fmt"
	"log/slog"
	"strings"
)

// maxReportedBadLines bounds how many per-line warnings one file produces per
// read — "that does not look like a domain", "that pattern was rejected" — so a
// file of prose cannot flood the log or the degradation message.
const maxReportedBadLines = 5

// parseDomainList parses a newline-delimited domain list, liberally: `#`
// introduces a comment, blank lines are skipped, entries are lowercased, a
// leading `@` is dropped, and duplicates collapse. Entries that do not look
// like a domain (no dot) are kept but logged, because the file belongs to
// another program and guessing wrong should not silently drop an entry. source
// only appears in log lines and in the returned error.
//
// A scan that cannot continue — one line longer than the scanner's 1 MiB
// buffer, say — returns the entries parsed so far *and* an error. Ignoring that
// error is how a single malformed line silently deletes the rest of somebody's
// subscription list.
func parseDomainList(data []byte, source string) ([]string, error) {
	var (
		out      []string
		seen     = make(map[string]bool)
		reported int
		skipped  int
		line     int
	)

	sc := newListScanner(data)
	for line = 1; sc.Scan(); line++ {
		text := sc.Text()
		if i := strings.IndexByte(text, '#'); i >= 0 {
			text = text[:i]
		}
		domain := NormalizeDomain(text)
		if domain == "" {
			continue
		}
		if !strings.Contains(domain, ".") {
			skipped++
			if reported < maxReportedBadLines {
				reported++
				slog.Warn("domain list entry does not look like a domain; using it anyway",
					"path", source, "line", line, "entry", domain)
			}
		}
		if seen[domain] {
			continue
		}
		seen[domain] = true
		out = append(out, domain)
	}
	if skipped > reported {
		slog.Warn("more domain list entries do not look like domains",
			"path", source, "suppressed", skipped-reported)
	}

	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("truncated at line %d: %w", line, err)
	}
	return out, nil
}

// newListScanner returns the line scanner every externally-owned list file is
// read with. The 1 MiB cap is deliberate: a line longer than that stops the
// scan, and the resulting error is reported as degradation rather than swallowed.
func newListScanner(data []byte) *bufio.Scanner {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return sc
}
