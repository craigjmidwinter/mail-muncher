package filter

import (
	"log/slog"
	"os"
	"regexp"
	"sort"
	"sync"
)

// DegradedFile records one externally-owned file that could not be read in
// full during the current cycle: it does not exist yet, it is unreadable, or
// parsing rejected some of its lines.
//
// Evaluation semantics are unchanged by degradation — an unreadable file still
// matches nothing, and a partly-parsed one still matches what parsed. What
// degradation adds is *reporting*: the pipeline asks for it after every cycle
// and applies the configured `on_degraded_filter` policy, because "this
// predicate matched nothing" and "this predicate could not be evaluated" are
// not the same fact and only one of them makes it safe to advance the cursor.
type DegradedFile struct {
	// Path is the file that could not be read.
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

// FileKind names which parser reads an externally-owned file, and so which of
// the per-cycle caches holds it. It doubles as the noun used in log lines.
//
// Adding a third kind is the expensive way to extend this; adding a predicate
// that reuses one of these two is the cheap way. `to_regex_file` and
// `subject_regex_file` would both be FileKindPatterns — same file format, same
// parser, same cache entry — differing only in which strings of the message
// the compiled matcher tests.
type FileKind string

const (
	// FileKindDomains is a `from_domains_file` domain list.
	FileKindDomains FileKind = "domain list"
	// FileKindPatterns is a `from_regex_file` pattern list.
	FileKindPatterns FileKind = "pattern list"
)

// FileRef is one externally-owned file a compiled match tree reads, and the
// parser that reads it. Engine.FileRefs returns them so a caller can preload
// every file before the first message is evaluated.
type FileRef struct {
	// Kind selects the parser.
	Kind FileKind
	// Path is the file, as configured (already `~`/`$VAR`-expanded by
	// config.Load).
	Path string
}

// Files caches the externally-owned files referenced by the file-valued
// predicates — `from_domains_file`, `from_regex_file` — for the length of one
// pipeline cycle.
//
// The point of those predicates is that another program (the job-search
// tracker) owns the file: mail-muncher must pick up edits without a restart or
// a config change, but must not re-read the file once per message. So a file is
// read at most once per cycle, on first use, and BeginCycle drops what was
// cached.
//
// A missing or unreadable file yields an empty list — the predicate matches
// nothing — plus one warning per file per cycle. Compiling and evaluating never
// fail on it: the owning program may not have created the file yet. The failure
// is instead recorded and reported by Degraded, so the caller can decide
// whether advancing past that cycle's mail is safe. The same is true of a file
// that parsed only partly: what parsed is used, and the rest is degradation.
//
// Files is safe for concurrent use.
type Files struct {
	mu       sync.Mutex
	domains  map[string][]string
	patterns map[string][]*regexp.Regexp
	// degraded is keyed by path across every kind, because the caller acting on
	// it cares about "which file", not "which parser".
	degraded map[string]error
}

// DomainFiles is the former name of Files, from when a domain list was the only
// externally-owned file a rule could reference.
//
// Deprecated: use Files.
type DomainFiles = Files

// NewFiles returns an empty cache. The first lookup of each path in a cycle
// reads the file.
func NewFiles() *Files {
	return &Files{
		domains:  make(map[string][]string),
		patterns: make(map[string][]*regexp.Regexp),
		degraded: make(map[string]error),
	}
}

// NewDomainFiles returns an empty cache.
//
// Deprecated: use NewFiles.
func NewDomainFiles() *Files { return NewFiles() }

// BeginCycle discards everything read during the previous cycle, so the next
// lookup of each path re-reads the file (and re-warns about it if it is still
// missing). It also clears the degradation recorded for the previous cycle: a
// file that has since appeared must not keep a cycle held forever.
func (f *Files) BeginCycle() {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.domains = make(map[string][]string, len(f.domains))
	f.patterns = make(map[string][]*regexp.Regexp, len(f.patterns))
	f.degraded = make(map[string]error, len(f.degraded))
}

// Preload reads every referenced file now, rather than lazily on first use, and
// records any that could not be read.
//
// The pipeline calls it right after BeginCycle so that a policy which must act
// *before* anything is stored (`on_degraded_filter: fail`) has the full picture
// up front. It costs nothing extra: the reads are the same ones evaluation
// would have done, and their results land in the same per-cycle cache.
func (f *Files) Preload(refs []FileRef) {
	if f == nil {
		return
	}
	for _, ref := range refs {
		switch ref.Kind {
		case FileKindPatterns:
			f.Patterns(ref.Path)
		default:
			f.Domains(ref.Path)
		}
	}
}

// Degraded returns every externally-owned file that could not be read in full
// during the current cycle, ordered by path. It is empty when every referenced
// file was read cleanly — which is the only state in which advancing a cursor
// past unmatched mail is safe.
func (f *Files) Degraded() []DegradedFile {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.degraded) == 0 {
		return nil
	}
	out := make([]DegradedFile, 0, len(f.degraded))
	for path, err := range f.degraded {
		out = append(out, DegradedFile{Path: path, Err: err})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Domains returns the normalized domains listed in path, reading the file if
// this is its first use in the current cycle. A missing or unreadable file
// yields nil and is recorded for Degraded.
func (f *Files) Domains(path string) []string {
	if f == nil {
		domains, _ := readList(path, parseDomainList)
		return domains
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.domains == nil {
		f.domains = make(map[string][]string)
	}
	return cachedList(f, f.domains, path, FileKindDomains, parseDomainList)
}

// Patterns returns the compiled patterns listed in path, reading and compiling
// the file if this is its first use in the current cycle. A missing or
// unreadable file yields nil and is recorded for Degraded, as does a file some
// of whose lines were rejected — the ones that survived are still returned.
//
// Compilation happens here, once per file per cycle, never per message.
func (f *Files) Patterns(path string) []*regexp.Regexp {
	if f == nil {
		patterns, _ := readList(path, parsePatternList)
		return patterns
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.patterns == nil {
		f.patterns = make(map[string][]*regexp.Regexp)
	}
	return cachedList(f, f.patterns, path, FileKindPatterns, parsePatternList)
}

// parseList turns one externally-owned file's bytes into the list a predicate
// matches against. source names the file, and appears only in log lines and in
// the returned error.
//
// A parser returns whatever it recovered *alongside* any error, never instead
// of it: a file with one bad line should cost one entry, not the whole file.
type parseList[T any] func(data []byte, source string) ([]T, error)

// readList reads and parses one externally-owned file. A read or parse failure
// is returned alongside whatever was recovered; callers treat the list as
// "matches nothing extra", never as fatal, and report the error as degradation.
func readList[T any](path string, parse parseList[T]) ([]T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parse(data, path)
}

// cachedList is the generic half of the per-cycle cache: read on first use,
// reuse for the rest of the cycle, warn once, record degradation once. Every
// file-valued predicate goes through it, so there is exactly one place where
// "the file could not be read" turns into something `on_degraded_filter` can
// act on.
//
// f.mu must be held by the caller.
func cachedList[T any](f *Files, cache map[string][]T, path string, kind FileKind, parse parseList[T]) []T {
	if list, ok := cache[path]; ok {
		return list
	}
	if f.degraded == nil {
		f.degraded = make(map[string]error)
	}

	list, err := readList(path, parse)
	if err != nil {
		// Warned once per cycle: the cache entry below suppresses repeats
		// until BeginCycle clears it.
		if len(list) == 0 {
			slog.Warn(string(kind)+" unavailable; predicate matches nothing this cycle",
				"path", path, "error", err)
		} else {
			slog.Warn(string(kind)+" could not be read in full; predicate uses only the entries that parsed",
				"path", path, "entries", len(list), "error", err)
		}
		f.degraded[path] = err
	}
	cache[path] = list
	return list
}
