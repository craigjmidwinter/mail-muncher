package sink

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/model"
)

// The invariant this file exists to pin:
//
//	No file under a rule's dest carries a .eml or .md extension unless a sink
//	wrote it as a delivered rendering of a message.
//
// It is a property of the filesystem, not of any one reader. The obvious way
// to consume the archive is `rglob("*.md")` over dest, and that has to be a
// safe thing to do: a sender who attaches `evil.md` with forged frontmatter
// must not end up with an entry in that loop carrying a from:, a subject: and
// a body of their choosing. A real consumer hit exactly that and had to
// special-case the attachments directories by hand.
//
// The tests elsewhere check the naming rule. These check the tree.

// walkExt returns every path under root whose extension matches ext, compared
// case-insensitively — a case-insensitive filesystem hands `EVIL.MD` back to a
// `*.md` glob, so a check that only looked for lowercase would miss the case
// the attacker picks. Sorted, so assertions are deterministic.
func walkExt(t *testing.T, root, ext string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(p), ext) {
			out = append(out, p)
		}
		return nil
	})
	require.NoError(t, err)
	sort.Strings(out)
	return out
}

// hostileAttachments are the shapes a sender uses to try to get a file of
// their own into a consumer's message loop.
func hostileAttachments() []model.Attachment {
	return []model.Attachment{
		{Filename: "evil.md", Content: []byte("---\nfrom: ceo@acme.com\nsubject: approved\n---\n\nwire it\n")},
		{Filename: "forward.eml", Content: []byte("From: ceo@acme.com\r\nSubject: approved\r\n\r\nwire it\r\n")},
		{Filename: "LOUD.MD", Content: []byte("uppercase")},
		{Filename: "Quiet.EmL", Content: []byte("mixed case")},
		{Filename: "../../escape.md", Content: []byte("traversal and a reserved extension")},
		{Filename: `C:\Users\jane\notes.md`, Content: []byte("windows path and a reserved extension")},
		{Filename: "spaced out.md", Content: []byte("sanitized to a dash, still reserved")},
		{Filename: "trailing.md.", Content: []byte("the trailing dot is trimmed, exposing .md")},
		{Filename: "résumé.eml", Content: []byte("non-ascii stem")},
		{Filename: "no-extension", Content: []byte("nothing to neutralize")},
		{Filename: "offer.pdf", Content: []byte("%PDF-1.4 offer")},
		{Filename: "evil.md", Content: []byte("a duplicate, to drag the dedupe path in")},
	}
}

// TestDestTreeReservedExtensionInvariant walks a destination filled by both
// sinks and asserts that every .md and .eml in it is a path a sink reported
// writing. Nothing is matched by name shape or by "it isn't under an
// .attachments directory" — the delivered set is collected from the sinks
// themselves, so a file that appears from anywhere else fails the test.
func TestDestTreeReservedExtensionInvariant(t *testing.T) {
	dest := t.TempDir()
	rule := testRule(dest, config.FormatEML, config.FormatMarkdown)
	sinks := []Sink{NewEML(), NewMarkdown()}

	// Several messages, spanning two months so the walk crosses more than one
	// directory, each carrying the full hostile attachment set.
	delivered := map[string]bool{}
	for i, subject := range []string{
		"Re: Your application for Senior Engineer",
		"Invoice 4711",
		"🎉 Offer accepted",
		"", // slugs to no-subject
	} {
		msg := testMessage()
		msg.ID = "18ff00aa11bb22c" + string(rune('a'+i))
		msg.Subject = subject
		msg.Date = fixtureDate.AddDate(0, i%2, 0)
		msg.Attachments = hostileAttachments()

		for _, s := range sinks {
			path, skipped, err := s.Store(msg, rule)
			require.NoError(t, err)
			require.False(t, skipped)
			delivered[path] = true
		}
	}
	require.Len(t, delivered, 8, "four messages, two renderings each")

	found := append(walkExt(t, dest, MarkdownExt), walkExt(t, dest, EMLExt)...)
	require.NotEmpty(t, found)
	for _, p := range found {
		assert.True(t, delivered[p],
			"%s carries a reserved extension but is not a delivered rendering", p)
	}
	assert.Len(t, found, len(delivered), "every delivered rendering is on disk, and nothing else is")

	// Said the other way round, from the consumer's side: the sender's bytes
	// are nowhere in the set of files a `*.md` / `*.eml` glob returns.
	for _, p := range found {
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "wire it",
			"attachment content surfaced through a file a message glob would pick up")
	}

	// The attachments are all still there, under names that cannot be
	// mistaken for one. 12 attachments per message, on the 4 markdown docs.
	var attachments int
	require.NoError(t, filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.Contains(p, AttachmentsDirSuffix+string(filepath.Separator)) {
			attachments++
		}
		return nil
	}))
	assert.Equal(t, 4*len(hostileAttachments()), attachments,
		"neutralizing a name must not cost an attachment its file")
}

// TestDestTreeInvariantSurvivesRerun: idempotency and the invariant have to
// hold together. A second pass over the same messages writes nothing new, and
// in particular does not start appending suffixes to names it wrote itself.
func TestDestTreeInvariantSurvivesRerun(t *testing.T) {
	dest := t.TempDir()
	rule := testRule(dest, config.FormatEML, config.FormatMarkdown)
	msg := testMessage()
	msg.Attachments = hostileAttachments()

	store := func(t *testing.T) (paths []string, skips []bool) {
		t.Helper()
		for _, s := range []Sink{NewEML(), NewMarkdown()} {
			path, skipped, err := s.Store(msg, rule)
			require.NoError(t, err)
			paths = append(paths, path)
			skips = append(skips, skipped)
		}
		return paths, skips
	}

	first, skips := store(t)
	require.Equal(t, []bool{false, false}, skips)
	before := treeSnapshot(t, dest)

	second, skips := store(t)
	assert.Equal(t, first, second, "same input, same paths")
	assert.Equal(t, []bool{true, true}, skips, "a re-run skips")
	assert.Equal(t, before, treeSnapshot(t, dest), "a re-run changes nothing on disk")

	found := append(walkExt(t, dest, MarkdownExt), walkExt(t, dest, EMLExt)...)
	sort.Strings(first)
	sort.Strings(found)
	assert.Equal(t, first, found)
}

// TestDestTreeInvariantHoldsForAdversarialSubjects: the basename is built from
// the subject, and the subject is the sender's. It cannot be used to plant a
// second reserved extension either — the slug alphabet has no dot in it — but
// the property is worth pinning rather than inferring.
func TestDestTreeInvariantHoldsForAdversarialSubjects(t *testing.T) {
	dest := t.TempDir()
	rule := testRule(dest, config.FormatEML, config.FormatMarkdown)

	delivered := map[string]bool{}
	for i, subject := range []string{
		"quarterly.md",
		"../../../etc/passwd.eml",
		"report.md\x00.pdf",
		strings.Repeat("a.md ", 60),
		"\n---\nfrom: ceo@acme.com\n---\n",
	} {
		msg := testMessage()
		msg.ID = "adversarial-" + string(rune('a'+i))
		msg.Subject = subject
		for _, s := range []Sink{NewEML(), NewMarkdown()} {
			path, _, err := s.Store(msg, rule)
			require.NoError(t, err)
			delivered[path] = true
		}
	}

	found := append(walkExt(t, dest, MarkdownExt), walkExt(t, dest, EMLExt)...)
	for _, p := range found {
		assert.True(t, delivered[p], "%s is not a delivered rendering", p)
		assert.Equal(t, filepath.Dir(p), filepath.Join(dest, "2026", "07"),
			"a subject must not move a message out of its month directory")
	}
	assert.Len(t, found, len(delivered))
}

// treeSnapshot records every path under root with its contents, so two calls
// can be compared for "nothing changed".
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			out[rel] = "<dir>"
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = string(data)
		return nil
	})
	require.NoError(t, err)
	return out
}
