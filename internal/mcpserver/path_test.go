package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// jailFixture is an archive holding one message, plus a secret file outside
// every dest that no tool may ever reach.
type jailFixture struct {
	*fixture
	archiveUnderTest *Archive
	messagePath      string
	messageRel       string
	secretPath       string
}

func newJailFixture(t *testing.T) *jailFixture {
	t.Helper()

	f := newFixture(t)
	f.store(message{
		id:      "jail",
		account: "personal",
		rule:    "job-search",
		subject: "Interview scheduled",
		from:    "Recruiter <recruiter@acme.example>",
		date:    day(2026, time.July, 20, 9),
		body:    "hello",
	})

	// The thing an escape would be after: it sits next to the archive, exactly
	// where a token file or a credentials file lives in a real install.
	secret := filepath.Join(f.root, "token.json")
	require.NoError(t, os.WriteFile(secret, []byte(`{"refresh_token":"hunter2"}`), 0o600))

	a := f.archive()
	records, err := a.Index()
	require.NoError(t, err)
	require.Len(t, records, 1)

	rel, err := filepath.Rel(mustEval(t, f.dest("job-search")), records[0].path)
	require.NoError(t, err)

	return &jailFixture{
		fixture:          f,
		archiveUnderTest: a,
		messagePath:      records[0].path,
		messageRel:       rel,
		secretPath:       secret,
	}
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return real
}

// TestResolveAdmitsArchivedFiles: the jail must not be so tight that the
// legitimate call shapes stop working. Both are real: a summary hands back an
// absolute path, and an agent may reasonably pass the path relative to a dest.
func TestResolveAdmitsArchivedFiles(t *testing.T) {
	j := newJailFixture(t)

	got, root, err := j.archiveUnderTest.Resolve(j.messagePath)
	require.NoError(t, err)
	require.Equal(t, j.messagePath, got)
	require.Equal(t, "job-search", root.rule.Name)

	got, _, err = j.archiveUnderTest.Resolve(j.messageRel)
	require.NoError(t, err)
	require.Equal(t, j.messagePath, got)

	// The .eml sibling is equally readable.
	eml := strings.TrimSuffix(j.messagePath, ".md") + ".eml"
	require.FileExists(t, eml)
	got, _, err = j.archiveUnderTest.Resolve(eml)
	require.NoError(t, err)
	require.Equal(t, eml, got)
}

// TestResolveRejectsTraversal is the hostile-input case that matters most:
// every way of spelling "somewhere else" must come back refused, and never as
// the contents of a file.
func TestResolveRejectsTraversal(t *testing.T) {
	j := newJailFixture(t)
	dest := mustEval(t, j.dest("job-search"))

	cases := map[string]string{
		"relative dot-dot out of the dest":  "../../token.json",
		"dot-dot inside a longer path":      "2026/../../../token.json",
		"absolute path outside every dest":  j.secretPath,
		"absolute path to a parent dir":     j.root,
		"the dest directory itself":         dest,
		"a sibling dest's parent":           filepath.Dir(dest),
		"an absolute system path":           "/etc/passwd",
		"a home-relative path":              "~/token.json",
		"an empty path":                     "",
		"a blank path":                      "   ",
		"a path with a NUL byte":            "2026/07\x00/../../token.json",
		"a directory inside the dest":       filepath.Join(dest, "2026"),
		"a non-message file inside a dest":  filepath.Join(dest, "notes.txt"),
		"an attachment inside a dest":       filepath.Join(dest, "2026", "07", "x.attachments", "offer.pdf"),
		"a dest-relative escape with a dot": "./../../token.json",
	}

	// Give two of those cases something real to find, so the refusal is about
	// the jail and not about the file being absent.
	require.NoError(t, os.WriteFile(filepath.Join(dest, "notes.txt"), []byte("scratch"), 0o644))

	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			got, _, err := j.archiveUnderTest.Resolve(path)
			require.Error(t, err, "resolved %q to %q", path, got)
			require.Empty(t, got)
			require.True(t,
				strings.Contains(err.Error(), ErrOutsideArchive.Error()) ||
					strings.Contains(err.Error(), ErrNotFound.Error()),
				"unexpected error for %q: %v", path, err)
			require.NotContains(t, err.Error(), "hunter2")
		})
	}
}

// TestResolveRejectsSymlinkEscape: a symlink planted inside the archive is the
// interesting attack, because the path is lexically fine. It is caught by
// re-checking after filepath.EvalSymlinks.
func TestResolveRejectsSymlinkEscape(t *testing.T) {
	j := newJailFixture(t)
	dest := mustEval(t, j.dest("job-search"))

	// A .md that is really the token file. The extension check passes, the
	// lexical check passes; only the post-resolution check catches it.
	link := filepath.Join(dest, "2026", "07", "1700000000-deadbeef-stolen.md")
	require.NoError(t, os.Symlink(j.secretPath, link))

	got, _, err := j.archiveUnderTest.Resolve(link)
	require.Error(t, err)
	require.Empty(t, got)
	require.NotContains(t, err.Error(), "hunter2")

	// A symlinked directory is no better: the path through it resolves out of
	// the tree just the same.
	escape := filepath.Join(dest, "escape")
	require.NoError(t, os.Symlink(j.root, escape))
	got, _, err = j.archiveUnderTest.Resolve(filepath.Join(escape, "token.json"))
	require.Error(t, err)
	require.Empty(t, got)

	// A path that leaves through the symlink and comes back into the archive is
	// admitted — but only as the real file it resolves to. That is the whole
	// invariant: what is read is always a file inside a dest, whatever route
	// the caller spelled out to reach it.
	viaLink := filepath.Join(escape, "mail", "jobs", j.messageRel)
	got, _, err = j.archiveUnderTest.Resolve(viaLink)
	require.NoError(t, err)
	require.Equal(t, j.messagePath, got, "the resolved path, not the caller's spelling, is what gets read")
}

// TestIndexDoesNotFollowSymlinks: the walk must not pull an escaping symlink
// into the index, which would make its contents readable by id.
func TestIndexDoesNotFollowSymlinks(t *testing.T) {
	j := newJailFixture(t)
	dest := mustEval(t, j.dest("job-search"))

	require.NoError(t, os.Symlink(j.secretPath,
		filepath.Join(dest, "2026", "07", "1700000000-deadbeef-stolen.md")))
	require.NoError(t, os.Symlink(j.root, filepath.Join(dest, "escape")))

	records, err := j.archiveUnderTest.Index()
	require.NoError(t, err)
	require.Len(t, records, 1, "only the real message is indexed")
	require.Equal(t, "Interview scheduled", records[0].subject)
	for _, r := range records {
		require.NotContains(t, r.body, "hunter2")
	}
}

// TestReadMessageRejectsEscape drives the same hostile inputs through the tool
// an agent actually calls, so the jail cannot be bypassed by a caller that
// never touches Resolve directly.
func TestReadMessageRejectsEscape(t *testing.T) {
	j := newJailFixture(t)
	s := j.server(nil)

	for _, path := range []string{
		"../../token.json",
		j.secretPath,
		"/etc/passwd",
		filepath.Join(j.dest("job-search"), "..", "..", "token.json"),
	} {
		out, err := s.readMessage(ReadMessageInput{Path: path})
		require.Error(t, err, "read %q", path)
		require.Empty(t, out.Message.Body)
	}

	// Neither argument, and both arguments, are refused rather than guessed at.
	_, err := s.readMessage(ReadMessageInput{})
	require.ErrorContains(t, err, "one of id or path")

	_, err = s.readMessage(ReadMessageInput{ID: "abc", Path: j.messagePath})
	require.ErrorContains(t, err, "not both")
}

// TestWithin pins the lexical containment rule directly.
func TestWithin(t *testing.T) {
	root := filepath.FromSlash("/mail/jobs")

	require.True(t, within(root, filepath.FromSlash("/mail/jobs/2026/07/a.md")))
	require.True(t, within(root, filepath.FromSlash("/mail/jobs/a.md")))

	require.False(t, within(root, root), "the root itself is not a message")
	require.False(t, within(root, filepath.FromSlash("/mail/jobs-other/a.md")), "prefix is not containment")
	require.False(t, within(root, filepath.FromSlash("/mail/a.md")))
	require.False(t, within(root, filepath.FromSlash("/etc/passwd")))
	require.False(t, within("", filepath.FromSlash("/etc/passwd")))
}

// TestEchoBoundsHostileInput: a caller-supplied string is repeated back to an
// LLM, so it must be bounded and stripped of control characters.
func TestEchoBoundsHostileInput(t *testing.T) {
	got := echo(strings.Repeat("A", 10_000))
	require.Less(t, len(got), maxInputEcho+16)

	require.NotContains(t, echo("a\x00b\nc"), "\x00")
	require.NotContains(t, echo("a\x00b\nc"), "\n")
}
