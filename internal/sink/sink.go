package sink

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/model"
)

// File permissions for everything a sink creates. Archived mail is private
// correspondence and decoded attachments, so it gets the same treatment as the
// sync cursors in internal/state and the OAuth token file: owner-only, 0700
// directories and 0600 files. Nothing here should be readable by other local
// users, and a tool run by the owner is unaffected.
const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

const (
	// maxSlugLen bounds the subject slug inside a basename.
	maxSlugLen = 40
	// noSubjectSlug stands in for a subject that slugs to nothing (empty,
	// punctuation-only, emoji-only, or written in a non-ASCII script).
	noSubjectSlug = "no-subject"
)

// HashLen is how many hex characters of the identity digest go into a
// basename. 16 hex characters is 64 bits: enough that two messages colliding on
// the fragment is not reachable at any volume a mailbox produces, while still
// leaving the name short enough to read. It is exported because the fragment is
// a public part of the on-disk layout — readers of the archive parse it back
// out as a message id.
const HashLen = 16

// ErrUnknownFormat is returned by New and ForRule for a format no sink
// implements.
var ErrUnknownFormat = errors.New("sink: unknown format")

// ErrNoDest is returned by Store when the rule has no `dest:` directory.
var ErrNoDest = errors.New("sink: rule has no dest")

// ErrSymlink is returned when a path a sink would create, check, or write
// through is a symbolic link.
//
// Nothing a sink writes is ever legitimately a symlink: the layout is built
// entirely out of directories this package creates and files it creates. A link
// in that tree therefore means something else is placing them there, and the
// safe reading is "refuse and say so", not "follow it and find out". The rule's
// own `dest:` is exempt — the operator chose that path, and pointing it at
// another volume is ordinary.
var ErrSymlink = errors.New("sink: refusing to follow symlink")

// Sink writes one rendering of a matched message to disk.
//
// Store is the contract the pipeline uses for a real run: it reports the path
// it wrote (or would have written), whether the message was skipped because
// that path already existed, and any error. A Store error concerns exactly one
// message and one format — the caller logs it, counts it as errored, and
// carries on with the rest of the cycle.
//
// Path and Plan exist for `--dry-run`: both are side-effect free (Path does no
// I/O at all, Plan does a single Lstat and creates nothing), so a dry run can
// report "would store <path>" without touching the destination tree.
//
// Every implementation is safe to reuse across messages and rules; sinks hold
// no per-message state.
type Sink interface {
	// Format is the config format this sink implements.
	Format() config.Format

	// Path returns the absolute-or-rule-relative destination path for the
	// message. It is pure: same message and rule, same answer, no I/O.
	Path(msg *model.Message, rule *config.Rule) string

	// Plan returns what Store would do without doing it: the destination
	// path, and whether Store would skip because the file already exists.
	Plan(msg *model.Message, rule *config.Rule) (path string, skipped bool, err error)

	// Store writes the rendering, creating parent directories as needed.
	// When the destination already exists it writes nothing and returns
	// skipped=true.
	Store(msg *model.Message, rule *config.Rule) (path string, skipped bool, err error)
}

// New returns the sink implementing a format.
func New(f config.Format) (Sink, error) {
	switch f {
	case config.FormatEML:
		return NewEML(), nil
	case config.FormatMarkdown:
		return NewMarkdown(), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownFormat, string(f))
	}
}

// ForRule returns one sink per format listed on the rule, in the rule's order,
// so the pipeline can fan a matched message out to each rendering:
//
//	sinks, err := sink.ForRule(rule)
//	for _, s := range sinks {
//	    path, skipped, err := s.Store(msg, rule)
//	}
//
// Duplicate formats are collapsed. An unrecognized format is an error
// (config.Validate rejects those first, so this is belt and braces).
func ForRule(r *config.Rule) ([]Sink, error) {
	if r == nil {
		return nil, nil
	}
	sinks := make([]Sink, 0, len(r.Formats))
	seen := make(map[config.Format]bool, len(r.Formats))
	for _, f := range r.Formats {
		if seen[f] {
			continue
		}
		seen[f] = true
		s, err := New(f)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", r.Name, err)
		}
		sinks = append(sinks, s)
	}
	return sinks, nil
}

// Basename is the shared, extension-less file name both sinks build their
// output from:
//
//	<unix-seconds>-<sha256(account+":"+id)[:16]>-<slug(subject)>
//
// The digest fragment is the idempotency key: it depends only on the account
// and the provider message id, so the same message always lands on the same
// path and a re-run can skip it by existence check alone. The timestamp sorts
// a directory chronologically and the slug makes it browsable; neither is
// load-bearing for identity.
//
// Timestamps before the Unix epoch clamp to 0 so a basename never starts with
// a '-' (which reads as a flag to most command-line tools).
func Basename(msg *model.Message) string {
	if msg == nil {
		return fmt.Sprintf("0-%s-%s", identity("", ""), noSubjectSlug)
	}
	secs := msg.Date.UTC().Unix()
	if secs < 0 {
		secs = 0
	}
	return fmt.Sprintf("%d-%s-%s", secs, identity(msg.Account, msg.ID), Slug(msg.Subject))
}

// identity is the stable per-message digest fragment.
func identity(account, id string) string {
	sum := sha256.Sum256([]byte(account + ":" + id))
	return hex.EncodeToString(sum[:])[:HashLen]
}

// Slug turns a subject into a filename fragment: lowercased, every character
// outside [a-z0-9] collapsed to a single '-', trimmed of leading and trailing
// '-', truncated to maxSlugLen, and replaced by "no-subject" when nothing
// survives.
//
// The alphabet is deliberately ASCII-only. Non-ASCII names would otherwise be
// subject to filesystem Unicode normalization (HFS+ stores NFD), which can
// make the written name differ from the name the next run stats for — and that
// existence check is the whole idempotency story. A subject that is entirely
// emoji or entirely non-Latin therefore slugs to "no-subject"; the digest
// fragment still keeps such messages apart.
func Slug(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			// Collapse runs of separators, and never lead with one.
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > maxSlugLen {
		slug = strings.TrimRight(slug[:maxSlugLen], "-")
	}
	if slug == "" {
		return noSubjectSlug
	}
	return slug
}

// destParts splits a message's destination into the rule's own `dest:` and the
// <YYYY>/<MM> elements below it. The two are kept apart because they are
// trusted differently: dest is the operator's configured path and may be
// whatever they point it at, while everything beneath is this package's own
// layout and must be a plain directory.
//
// The date is read in UTC so the layout does not shift with the machine's
// timezone; model.Parse has already fallen back to the provider's internal date
// when the message carried no usable Date header.
func destParts(msg *model.Message, rule *config.Rule) (string, []string) {
	var dest string
	if rule != nil {
		dest = rule.Dest
	}
	var t time.Time
	if msg != nil {
		t = msg.Date.UTC()
	}
	return dest, []string{t.Format("2006"), t.Format("01")}
}

// dirFor is the <dest>/<YYYY>/<MM> directory a message files into. Pure: it
// touches the filesystem not at all, so Path and Plan stay side-effect free.
func dirFor(msg *model.Message, rule *config.Rule) string {
	dest, parts := destParts(msg, rule)
	return filepath.Join(append([]string{dest}, parts...)...)
}

// ensureDir creates the message's <dest>/<YYYY>/<MM> directory and returns it.
//
// The rule's dest is created with its parents, as before. The year and month
// below it are created one component at a time so that an existing component
// can be inspected before it is descended into: a symlink there is refused
// rather than followed, which is what keeps a planted `2026 -> /elsewhere` from
// redirecting the archive out of the destination tree.
func ensureDir(msg *model.Message, rule *config.Rule) (string, error) {
	dest, parts := destParts(msg, rule)
	if err := os.MkdirAll(dest, dirPerm); err != nil {
		return "", fmt.Errorf("sink: create %s: %w", dest, err)
	}
	dir := dest
	for _, elem := range parts {
		dir = filepath.Join(dir, elem)
		if err := mkdirNoFollow(dir); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// verifyDir is the read-only half of ensureDir: it checks the components a
// message would be filed under without creating any of them, so a dry run can
// still refuse a symlinked year or month. A component that does not exist yet
// is fine — a real run would create it, and everything below it too.
func verifyDir(msg *model.Message, rule *config.Rule) error {
	dest, parts := destParts(msg, rule)
	dir := dest
	for _, elem := range parts {
		dir = filepath.Join(dir, elem)
		info, err := os.Lstat(dir)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return nil
		case err != nil:
			return fmt.Errorf("sink: stat %s: %w", dir, err)
		}
		if err := checkRealDir(dir, info); err != nil {
			return err
		}
	}
	return nil
}

// mkdirNoFollow creates one directory whose parent already exists, and accepts
// an existing one only if it is a real directory. A symlink — even one that
// resolves to a perfectly good directory — is an error.
func mkdirNoFollow(dir string) error {
	err := os.Mkdir(dir, dirPerm)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, os.ErrExist):
		info, statErr := os.Lstat(dir)
		if statErr != nil {
			return fmt.Errorf("sink: stat %s: %w", dir, statErr)
		}
		return checkRealDir(dir, info)
	default:
		return fmt.Errorf("sink: create %s: %w", dir, err)
	}
}

// checkRealDir accepts a path only if it is a directory in its own right,
// rather than a link standing in for one.
func checkRealDir(dir string, info os.FileInfo) error {
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%w: %s", ErrSymlink, dir)
	case !info.IsDir():
		return fmt.Errorf("sink: %s exists and is not a directory", dir)
	default:
		return nil
	}
}

// pathFor is the full destination path for a message under one extension.
func pathFor(msg *model.Message, rule *config.Rule, ext string) string {
	return filepath.Join(dirFor(msg, rule), Basename(msg)+ext)
}

// plan reports whether a path already exists, which is what both sinks treat
// as "already stored, skip".
//
// It uses Lstat, not Stat. Stat follows symlinks, which means a link planted at
// a message's final path would answer "yes, already stored" and the message
// would be silently dropped — counted as skipped, never written, pointing at a
// file the sink did not produce. Lstat sees the link itself, and a link is an
// error: the message is then counted and logged rather than swallowed.
func plan(path string) (bool, error) {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("%w: %s", ErrSymlink, path)
		}
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("sink: stat %s: %w", path, err)
	}
}

// checkDest rejects a rule that has nowhere to write.
func checkDest(rule *config.Rule) error {
	if rule == nil || strings.TrimSpace(rule.Dest) == "" {
		return ErrNoDest
	}
	return nil
}

// writeAndSync is a package-level indirection so tests can simulate a write
// failing partway through and assert that no temp file survives it.
var writeAndSync = func(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

// writeTemp writes data to a fresh temp file in dir, fsyncs it, and returns its
// name with the file closed. Anything that fails along the way cleans the temp
// file up and leaves nothing behind. The caller owns the returned name and is
// responsible for removing it if it does not end up publishing it.
func writeTemp(dir, base string, data []byte) (string, error) {
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("sink: create temp file in %s: %w", dir, err)
	}
	name := tmp.Name()
	fail := func(format string, err error) (string, error) {
		_ = tmp.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf(format, name, err)
	}

	if err := writeAndSync(tmp, data); err != nil {
		return fail("sink: write %s: %w", err)
	}
	if err := tmp.Chmod(filePerm); err != nil {
		return fail("sink: chmod %s: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("sink: close %s: %w", name, err)
	}
	return name, nil
}

// createFileExclusive publishes data at path and reports whether it was this
// call that created it. A false with a nil error means the name was already
// taken — which for a message file is exactly "already archived, skip".
//
// This is the idempotency check, not a step after one. The kernel decides
// whether the name is free, at the instant the name is claimed, so there is no
// window between deciding and acting in which another writer can slip a file in
// and have it silently replaced.
//
// The mechanism is link(2) from a fully written temp file: it fails with EEXIST
// rather than clobbering, and unlike rename(2) it cannot replace something that
// is already there. Because the temp file is complete and fsynced first, the
// published file is never partial either — the two guarantees the old
// stat-then-rename could only offer one of at a time.
func createFileExclusive(path string, data []byte) (bool, error) {
	dir := filepath.Dir(path)
	tmpName, err := writeTemp(dir, filepath.Base(path), data)
	if err != nil {
		return false, err
	}
	// A no-op once the link below has succeeded and the name has two entries.
	defer func() { _ = os.Remove(tmpName) }()

	err = os.Link(tmpName, path)
	switch {
	case err == nil:
		syncDir(dir)
		return true, nil
	case errors.Is(err, os.ErrExist):
		return false, existingPathTaken(path)
	}

	// Hard links are how "claim this name or fail" is said portably, but not
	// every filesystem has them (FAT, some network mounts). Fall back to an
	// exclusive create written in place: still atomically no-clobber, and still
	// symlink-proof, at the cost of the no-partial-file guarantee — and only on
	// filesystems that left us no way to keep it.
	return createInPlaceExclusive(path, data)
}

// createInPlaceExclusive is the fallback for filesystems without hard links.
// O_CREATE|O_EXCL is specified to fail on a symlink rather than follow it, so
// this path is no more permissive than the linking one.
func createInPlaceExclusive(path string, data []byte) (bool, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	switch {
	case errors.Is(err, os.ErrExist):
		return false, existingPathTaken(path)
	case err != nil:
		return false, fmt.Errorf("sink: create %s: %w", path, err)
	}
	if err := writeAndSync(f, data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return false, fmt.Errorf("sink: write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return false, fmt.Errorf("sink: close %s: %w", path, err)
	}
	syncDir(filepath.Dir(path))
	return true, nil
}

// existingPathTaken classifies a lost race for a name. A plain file there is
// the ordinary case — an earlier run archived this message — and reports no
// error, leaving the caller to count a skip. A symlink is not ordinary: it was
// planted between the caller's check and this claim, and it gets the same
// refusal it would have got had the check seen it.
func existingPathTaken(path string) error {
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrSymlink, path)
	}
	return nil
}

// writeFileAtomic writes data to a temp file in the destination's own
// directory, fsyncs it, and renames it into place. Anything that fails before
// the rename leaves no partial file and no temp file behind.
//
// Unlike createFileExclusive this will replace an existing file, so it is only
// for content whose name is not the idempotency marker — attachments, which the
// .md above them decides the fate of. Replacing is still symlink-safe:
// rename(2) swaps the final component itself and never writes through a link
// sitting at that name.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmpName, err := writeTemp(dir, filepath.Base(path), data)
	if err != nil {
		return err
	}
	// A no-op once the rename below has succeeded.
	defer func() { _ = os.Remove(tmpName) }()

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("sink: rename %s -> %s: %w", tmpName, path, err)
	}
	syncDir(dir)
	return nil
}

// syncDir flushes a directory entry so a completed rename survives power loss.
// Best effort: not every platform lets you open a directory.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer func() { _ = d.Close() }()
	_ = d.Sync()
}
