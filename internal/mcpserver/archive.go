package mcpserver

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/craigmidwinter/mail-muncher/internal/config"
	"github.com/craigmidwinter/mail-muncher/internal/sink"
)

// Errors a tool turns into a clean, non-fatal tool error. They are compared
// with errors.Is, never string-matched.
var (
	// ErrOutsideArchive is the path jail refusing a path. It is deliberately
	// indistinguishable from "no such file": a caller must not be able to use
	// the difference to probe the filesystem outside the archive.
	ErrOutsideArchive = errors.New("path is not inside a configured destination directory")
	// ErrNotFound is a message id or path that names nothing stored.
	ErrNotFound = errors.New("no such stored message")
	// ErrNoDests is a configuration with no usable rule `dest` at all, which
	// leaves the archive with nothing it is allowed to read.
	ErrNoDests = errors.New("no rule has a dest directory to read")
)

// maxInputEcho bounds how much of a caller-supplied string is repeated back in
// an error message. The result goes to an LLM, and a megabyte of hostile input
// echoed verbatim helps nobody.
const maxInputEcho = 200

// destRoot is one rule's destination directory, pinned at construction.
//
// Two paths are kept because they can differ: dir is the directory as
// configured (which is what sink writes to, and therefore what a manifest
// Entry.Path names), while real is that directory with every symlink resolved.
// Admission is checked against either; the post-resolution escape check is
// always against real, because that is the only one a resolved path can be
// compared to meaningfully.
type destRoot struct {
	rule *config.Rule
	dir  string
	real string
	// exists reports whether dir resolved to something at construction time. A
	// dest that has never received mail is not an error — it simply holds no
	// messages until the first cycle creates it.
	exists bool
}

// Archive is the read-only view of the stored mail under the configured rule
// destinations. It owns the path jail and the lazily-built, mtime-keyed index.
//
// Archive is safe for concurrent use.
type Archive struct {
	cfg   *config.Config
	log   *slog.Logger
	roots []destRoot

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// cacheEntry is one parsed file, keyed by the path it was read from. A file
// whose size and modification time are unchanged is never re-parsed.
type cacheEntry struct {
	modTime time.Time
	size    int64
	rec     *fileRecord
}

// NewArchive pins the destination directories of every rule that has one.
//
// Resolving the roots once, here, is what makes the jail cheap and stable: a
// symlink swapped in later cannot widen it, because admission is always
// re-checked against these roots and every resolved path is compared to real.
func NewArchive(cfg *config.Config, log *slog.Logger) (*Archive, error) {
	if cfg == nil {
		return nil, errors.New("mcpserver: no config")
	}
	if log == nil {
		log = slog.Default()
	}

	a := &Archive{cfg: cfg, log: log, cache: make(map[string]cacheEntry)}
	for i := range cfg.Rules {
		rule := &cfg.Rules[i]
		if strings.TrimSpace(rule.Dest) == "" {
			continue
		}
		dir, err := filepath.Abs(rule.Dest)
		if err != nil {
			return nil, fmt.Errorf("mcpserver: rule %q: resolve dest: %w", rule.Name, err)
		}
		root := destRoot{rule: rule, dir: filepath.Clean(dir), real: filepath.Clean(dir)}
		if real, err := filepath.EvalSymlinks(root.dir); err == nil {
			root.real, root.exists = filepath.Clean(real), true
		} else if !errors.Is(err, fs.ErrNotExist) {
			// A dest we cannot resolve is not fatal — the rule simply has
			// nothing readable — but it is worth saying out loud.
			log.Warn("cannot resolve rule dest; it will hold no readable messages",
				"rule", rule.Name, "error", err)
		}
		a.roots = append(a.roots, root)
	}
	if len(a.roots) == 0 {
		return nil, ErrNoDests
	}
	return a, nil
}

// Resolve admits a caller-supplied path into the archive, or refuses it.
//
// The check runs twice on purpose:
//
//  1. Lexically, before any I/O: the candidate must sit under one of the
//     configured dests. filepath.Join has already collapsed `..`, so a
//     traversal attempt fails here, and an absolute path outside the tree
//     never gets as far as a syscall.
//  2. After filepath.EvalSymlinks: the fully resolved path must still sit
//     under the resolved dest. This is what a symlink planted inside the
//     archive and pointing at /etc/passwd fails.
//
// Only `.eml` and `.md` files are admitted, and the target must be a regular
// file. Every rejection returns ErrOutsideArchive or ErrNotFound and never
// reveals whether the path exists outside the archive.
func (a *Archive) Resolve(p string) (string, *destRoot, error) {
	raw := strings.TrimSpace(p)
	switch {
	case raw == "":
		return "", nil, fmt.Errorf("%w: empty path", ErrOutsideArchive)
	case strings.ContainsRune(raw, 0):
		return "", nil, fmt.Errorf("%w: %s", ErrOutsideArchive, echo(p))
	case !readableExt(raw):
		return "", nil, fmt.Errorf("%w: %s (only %s and %s files are readable)",
			ErrOutsideArchive, echo(p), sink.EMLExt, sink.MarkdownExt)
	}

	for i := range a.roots {
		root := &a.roots[i]

		// A relative path is interpreted against each dest in turn; an absolute
		// one is taken as given. Join cleans, so "../../etc/passwd" becomes a
		// path outside the root and is rejected by the check below rather than
		// silently normalised into it.
		candidate := raw
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(root.real, candidate)
		}
		candidate = filepath.Clean(candidate)

		if !within(root.real, candidate) && !within(root.dir, candidate) {
			continue
		}

		real, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			// Missing, or a dangling symlink. Either way: nothing to read.
			continue
		}
		real = filepath.Clean(real)

		// The escape check. `real` has no symlinks left in it, so if it is
		// still under the resolved root it genuinely is a file the archive
		// owns.
		if !within(root.real, real) {
			return "", nil, fmt.Errorf("%w: %s", ErrOutsideArchive, echo(p))
		}
		if !readableExt(real) {
			return "", nil, fmt.Errorf("%w: %s", ErrOutsideArchive, echo(p))
		}
		info, err := os.Stat(real)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		return real, root, nil
	}

	return "", nil, fmt.Errorf("%w: %s", ErrNotFound, echo(p))
}

// readableExt reports whether a path names one of the two renderings the
// archive is allowed to open.
func readableExt(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case sink.EMLExt, sink.MarkdownExt:
		return true
	default:
		return false
	}
}

// within reports whether p sits strictly underneath root, lexically. Both are
// expected to be cleaned absolute paths.
//
// A path equal to root is not "within" it: a directory is never a readable
// message, and admitting it would only widen what callers can ask for.
func within(root, p string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// echo renders caller-supplied text for an error message, bounded.
func echo(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '.'
		}
		return r
	}, s)
	if len(s) > maxInputEcho {
		s = s[:maxInputEcho] + "…"
	}
	return fmt.Sprintf("%q", s)
}

// Index walks every destination and returns one record per stored message,
// newest first.
//
// Files whose size and modification time are unchanged since the last call are
// served from the cache rather than re-parsed; a cold walk still stats every
// file, which is the intended cost at personal-mailbox scale.
func (a *Archive) Index() ([]*record, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	fresh := make(map[string]cacheEntry, len(a.cache))
	byMessage := make(map[messageKey][]*fileRecord)

	for i := range a.roots {
		root := &a.roots[i]
		if !root.exists {
			continue
		}
		err := filepath.WalkDir(root.real, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// An unreadable subdirectory costs us that subtree, not the
				// whole call.
				a.log.Debug("skipping unreadable archive entry", "path", path, "error", err)
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}

			name := d.Name()
			if d.IsDir() {
				// Attachment directories hold sender-controlled files that are
				// not renderings of a message; they are reached through
				// read_message, not by walking.
				if path != root.real && (strings.HasPrefix(name, ".") || strings.HasSuffix(name, sink.AttachmentsDirSuffix)) {
					return fs.SkipDir
				}
				return nil
			}
			// WalkDir uses Lstat, so a symlink arrives here as a non-regular
			// entry and is dropped: nothing outside the tree can be pulled in
			// by the walk.
			if !d.Type().IsRegular() || strings.HasPrefix(name, ".") || !readableExt(name) {
				return nil
			}

			info, err := d.Info()
			if err != nil {
				a.log.Debug("skipping unstattable archive file", "path", path, "error", err)
				return nil
			}

			if prev, ok := a.cache[path]; ok && prev.size == info.Size() && prev.modTime.Equal(info.ModTime()) {
				fresh[path] = prev
				byMessage[prev.rec.key()] = append(byMessage[prev.rec.key()], prev.rec)
				return nil
			}

			rec, err := parseFile(path, root.rule, info)
			if err != nil {
				a.log.Warn("skipping unreadable stored message", "path", path, "error", err)
				return nil
			}
			fresh[path] = cacheEntry{modTime: info.ModTime(), size: info.Size(), rec: rec}
			byMessage[rec.key()] = append(byMessage[rec.key()], rec)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("mcpserver: walk %s: %w", root.rule.Name, err)
		}
	}

	a.cache = fresh

	records := make([]*record, 0, len(byMessage))
	for _, files := range byMessage {
		records = append(records, newRecord(files))
	}
	sortRecords(records)
	return records, nil
}

// messageKey identifies one stored message across its renderings: the rule
// that claimed it plus the identity digest in its basename.
type messageKey struct {
	rule string
	id   string
}

// record is one stored message, with every rendering of it that is on disk.
// The primary rendering is the `.md` when there is one, because its
// frontmatter is already structured; a rule configured `formats: [eml]` falls
// back to the parsed `.eml`.
type record struct {
	*fileRecord
	files []*fileRecord
}

// newRecord merges the renderings of one message, preferring markdown.
func newRecord(files []*fileRecord) *record {
	sort.SliceStable(files, func(i, j int) bool {
		return formatRank(files[i].format) < formatRank(files[j].format)
	})
	return &record{fileRecord: files[0], files: files}
}

// formatRank orders renderings by how much structure they carry, best first.
func formatRank(f config.Format) int {
	if f == config.FormatMarkdown {
		return 0
	}
	return 1
}

// sortRecords puts the newest message first, breaking ties on id so the order
// is total and stable across calls.
func sortRecords(records []*record) {
	sort.Slice(records, func(i, j int) bool {
		a, b := records[i], records[j]
		if !a.date.Equal(b.date) {
			return a.date.After(b.date)
		}
		if a.id != b.id {
			return a.id < b.id
		}
		return a.path < b.path
	})
}
