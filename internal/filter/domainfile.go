package filter

import (
	"bufio"
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
)

// maxReportedBadLines bounds how many "that does not look like a domain"
// warnings one file produces per read, so a file of prose cannot flood the log.
const maxReportedBadLines = 5

// DegradedFile records one externally-owned domain list that could not be read
// in full during the current cycle: it does not exist yet, it is unreadable, or
// parsing stopped early on an over-long line.
//
// Evaluation semantics are unchanged by degradation — an unreadable list still
// matches nothing, and a truncated one still matches what parsed. What
// degradation adds is *reporting*: the pipeline asks for it after every cycle
// and applies the configured `on_degraded_filter` policy, because "this
// predicate matched nothing" and "this predicate could not be evaluated" are
// not the same fact and only one of them makes it safe to advance the cursor.
type DegradedFile struct {
	// Path is the domain list that could not be read.
	Path string
	// Err is why. It is the raw os or parse error, unwrapped.
	Err error
}

func (d DegradedFile) String() string {
	if d.Err == nil {
		return d.Path
	}
	return d.Path + ": " + d.Err.Error()
}

// DomainFiles caches the externally-owned domain lists referenced by
// `from_domains_file` predicates for the length of one pipeline cycle.
//
// The point of the predicate is that another program (the job-search tracker)
// owns the file: mail-muncher must pick up edits without a restart or a config
// change, but must not re-read the file once per message. So a file is read at
// most once per cycle, on first use, and BeginCycle drops what was cached.
//
// A missing or unreadable file yields an empty list — the predicate matches
// nothing — plus one warning per file per cycle. Compiling and evaluating never
// fail on it: the owning program may not have created the file yet. The failure
// is instead recorded and reported by Degraded, so the caller can decide
// whether advancing past that cycle's mail is safe.
//
// DomainFiles is safe for concurrent use.
type DomainFiles struct {
	mu       sync.Mutex
	cached   map[string][]string
	degraded map[string]error
}

// NewDomainFiles returns an empty cache. The first lookup of each path in a
// cycle reads the file.
func NewDomainFiles() *DomainFiles {
	return &DomainFiles{
		cached:   make(map[string][]string),
		degraded: make(map[string]error),
	}
}

// BeginCycle discards everything read during the previous cycle, so the next
// lookup of each path re-reads the file (and re-warns about it if it is still
// missing). It also clears the degradation recorded for the previous cycle: a
// file that has since appeared must not keep a cycle held forever.
func (d *DomainFiles) BeginCycle() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cached = make(map[string][]string, len(d.cached))
	d.degraded = make(map[string]error, len(d.degraded))
}

// Preload reads every path now, rather than lazily on first use, and records
// any that could not be read.
//
// The pipeline calls it right after BeginCycle so that a policy which must act
// *before* anything is stored (`on_degraded_filter: fail`) has the full picture
// up front. It costs nothing extra: the reads are the same ones evaluation
// would have done, and their results land in the same per-cycle cache.
func (d *DomainFiles) Preload(paths []string) {
	if d == nil {
		return
	}
	for _, path := range paths {
		d.Domains(path)
	}
}

// Degraded returns every domain list that could not be read in full during the
// current cycle, ordered by path. It is empty when every referenced list was
// read cleanly — which is the only state in which advancing a cursor past
// unmatched mail is safe.
func (d *DomainFiles) Degraded() []DegradedFile {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.degraded) == 0 {
		return nil
	}
	out := make([]DegradedFile, 0, len(d.degraded))
	for path, err := range d.degraded {
		out = append(out, DegradedFile{Path: path, Err: err})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Domains returns the normalized domains listed in path, reading the file if
// this is its first use in the current cycle. A missing or unreadable file
// yields nil and is recorded for Degraded.
func (d *DomainFiles) Domains(path string) []string {
	if d == nil {
		domains, _ := readDomainFile(path)
		return domains
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cached == nil {
		d.cached = make(map[string][]string)
	}
	if d.degraded == nil {
		d.degraded = make(map[string]error)
	}
	if domains, ok := d.cached[path]; ok {
		return domains
	}

	domains, err := readDomainFile(path)
	if err != nil {
		// Warned once per cycle: the cache entry below suppresses repeats
		// until BeginCycle clears it.
		if len(domains) == 0 {
			slog.Warn("domain list unavailable; predicate matches nothing this cycle",
				"path", path, "error", err)
		} else {
			slog.Warn("domain list could not be read in full; predicate uses only the entries that parsed",
				"path", path, "entries", len(domains), "error", err)
		}
		d.degraded[path] = err
	}
	d.cached[path] = domains
	return domains
}

// readDomainFile reads and parses a domain list. A read or parse failure is
// returned alongside whatever was recovered; callers treat the list as "matches
// nothing extra", never as fatal, and report the error as degradation.
func readDomainFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseDomainList(data, path)
}

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

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
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
