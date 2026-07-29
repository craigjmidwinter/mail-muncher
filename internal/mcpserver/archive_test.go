package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/model"
	"github.com/craigjmidwinter/mail-muncher/internal/sink"
)

// TestIndexReadsMarkdownFrontmatter: the `.md` rendering is the preferred
// source, and everything a summary needs comes out of its frontmatter.
func TestIndexReadsMarkdownFrontmatter(t *testing.T) {
	f := newFixture(t)
	stored := f.store(message{
		id:       "m1",
		account:  "personal",
		rule:     "job-search",
		subject:  "Interview scheduled",
		from:     "Recruiter <recruiter@acme.example>",
		to:       "Craig <craig@example.test>",
		date:     day(2026, time.July, 20, 9),
		threadID: "thread-hiring",
		labels:   []string{"INBOX", "IMPORTANT"},
		body:     "We would like to book a call on Thursday.",
	})

	records, err := f.archive().Index()
	require.NoError(t, err)
	require.Len(t, records, 1)

	got := records[0]
	require.Equal(t, config.FormatMarkdown, got.format, "the .md is preferred over the .eml")
	require.Equal(t, sink.MarkdownExt, filepath.Ext(got.path))
	require.Len(t, got.files, 2, "both renderings are on disk and both are reported")

	require.Equal(t, "Interview scheduled", got.subject)
	require.Equal(t, "Recruiter <recruiter@acme.example>", got.from)
	require.Equal(t, []string{"Craig <craig@example.test>"}, got.to)
	require.Equal(t, "personal", got.account)
	require.Equal(t, "job-search", got.rule)
	require.Equal(t, "thread-hiring", got.threadID)
	require.Equal(t, string(model.ThreadIDSourceProvider), got.threadIDSource)
	require.Equal(t, []string{"INBOX", "IMPORTANT"}, got.labels)
	require.Equal(t, stored.Date.UTC(), got.date)
	require.Contains(t, got.body, "book a call on Thursday")

	// The id is the identity digest sink.Basename builds, not the provider id.
	base := sink.Basename(stored)
	wantID, ok := messageIDFromPath(base + sink.MarkdownExt)
	require.True(t, ok)
	require.Equal(t, wantID, got.id)
	require.Len(t, got.id, sink.HashLen)
}

// TestIndexFallsBackToEML covers a rule configured `formats: [eml]` only:
// there is no frontmatter, so the archive parses the RFC822 source instead.
func TestIndexFallsBackToEML(t *testing.T) {
	f := newFixture(t)
	f.store(message{
		id:      "n1",
		account: "work",
		rule:    "newsletters",
		subject: "This week in Go",
		from:    "Digest <digest@news.example>",
		date:    day(2026, time.July, 21, 8),
		body:    "Generics are still generics.",
	})

	records, err := f.archive().Index()
	require.NoError(t, err)
	require.Len(t, records, 1)

	got := records[0]
	require.Equal(t, config.FormatEML, got.format)
	require.Equal(t, []string{"eml"}, formatsOf(got))
	require.Equal(t, "This week in Go", got.subject)
	require.Equal(t, "Digest <digest@news.example>", got.from)
	require.Contains(t, got.body, "Generics are still generics")

	// The account cannot be read from the bytes, so it comes from the rule.
	require.Equal(t, "work", got.account)
	require.Equal(t, "newsletters", got.rule)

	// The provider's thread id is not in the file, so the id is reconstructed
	// and says so — which is exactly what ThreadIDSource is for.
	require.NotEmpty(t, got.threadID)
	require.Equal(t, string(model.ThreadIDSourceSelf), got.threadIDSource)
}

// TestIndexEMLHTMLOnlyBody: an HTML-only message still yields readable text,
// rendered the same way the markdown sink would have rendered it.
func TestIndexEMLHTMLOnlyBody(t *testing.T) {
	f := newFixture(t)
	f.store(message{
		id:      "n2",
		account: "work",
		rule:    "newsletters",
		subject: "HTML only",
		from:    "Digest <digest@news.example>",
		date:    day(2026, time.July, 22, 8),
		html:    "<html><body><h1>Headline</h1><p>Body <b>text</b>.</p></body></html>",
	})

	records, err := f.archive().Index()
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Contains(t, records[0].body, "Headline")
	require.Contains(t, records[0].body, "Body **text**")
	require.Equal(t, "markdown", records[0].bodyFormat)
}

// TestIndexReportsAttachments: names and sizes reach the record from the
// sibling attachments directory (markdown) and from the parsed parts (eml).
func TestIndexReportsAttachments(t *testing.T) {
	f := newFixture(t)
	f.store(message{
		id:      "m2",
		account: "personal",
		rule:    "job-search",
		subject: "Offer",
		from:    "HR <hr@acme.example>",
		date:    day(2026, time.July, 23, 10),
		body:    "Attached.",
		attach:  "offer.pdf",
	})
	f.store(message{
		id:      "n3",
		account: "work",
		rule:    "newsletters",
		subject: "With a file",
		from:    "Digest <digest@news.example>",
		date:    day(2026, time.July, 23, 11),
		body:    "Attached.",
		attach:  "report.pdf",
	})

	records, err := f.archive().Index()
	require.NoError(t, err)
	require.Len(t, records, 2)

	byRule := map[string]*record{}
	for _, r := range records {
		byRule[r.rule] = r
	}

	md := byRule["job-search"]
	require.Len(t, md.attachments, 1)
	require.Equal(t, "offer.pdf", md.attachments[0].Filename)
	require.Positive(t, md.attachments[0].Bytes, "the size is stat'd from the attachments directory")

	eml := byRule["newsletters"]
	require.Len(t, eml.attachments, 1)
	require.Equal(t, "report.pdf", eml.attachments[0].Filename)
	require.Equal(t, "application/pdf", eml.attachments[0].ContentType)
	require.Positive(t, eml.attachments[0].Bytes)
}

// TestIndexOrdersNewestFirst pins the documented ordering.
func TestIndexOrdersNewestFirst(t *testing.T) {
	f := newFixture(t)
	for i, d := range []time.Time{
		day(2026, time.July, 10, 9),
		day(2026, time.July, 25, 9),
		day(2026, time.July, 18, 9),
	} {
		f.store(message{
			id:      string(rune('a' + i)),
			account: "personal",
			rule:    "job-search",
			subject: d.Format("2006-01-02"),
			from:    "Recruiter <recruiter@acme.example>",
			date:    d,
			body:    "hi",
		})
	}

	records, err := f.archive().Index()
	require.NoError(t, err)
	require.Len(t, records, 3)
	require.Equal(t, "2026-07-25", records[0].subject)
	require.Equal(t, "2026-07-18", records[1].subject)
	require.Equal(t, "2026-07-10", records[2].subject)
}

// TestIndexCachesByModTime: an unchanged file is served from the cache, and a
// rewritten one is re-parsed. This is the whole point of the mtime key.
func TestIndexCachesByModTime(t *testing.T) {
	f := newFixture(t)
	f.store(message{
		id:      "m3",
		account: "personal",
		rule:    "job-search",
		subject: "First",
		from:    "Recruiter <recruiter@acme.example>",
		date:    day(2026, time.July, 24, 9),
		body:    "one",
	})

	a := f.archive()
	first, err := a.Index()
	require.NoError(t, err)
	require.Len(t, first, 1)

	second, err := a.Index()
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Same(t, first[0].fileRecord, second[0].fileRecord,
		"an unchanged file must not be re-parsed")

	// Rewrite the .md with a different subject and a later modification time.
	path := first[0].path
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(string(data)+"\nrewritten\n"), 0o644))
	later := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(path, later, later))

	third, err := a.Index()
	require.NoError(t, err)
	require.Len(t, third, 1)
	require.NotSame(t, first[0].fileRecord, third[0].fileRecord, "a changed file must be re-parsed")
	require.Contains(t, third[0].body, "rewritten")
}

// TestIndexSkipsNonMessageFiles: an archive is a directory a human also pokes
// at, so stray files must be ignored rather than crash the walk.
func TestIndexSkipsNonMessageFiles(t *testing.T) {
	f := newFixture(t)
	f.store(message{
		id:      "m4",
		account: "personal",
		rule:    "job-search",
		subject: "Real",
		from:    "Recruiter <recruiter@acme.example>",
		date:    day(2026, time.July, 26, 9),
		body:    "hi",
	})

	dest := f.dest("job-search")
	require.NoError(t, os.WriteFile(filepath.Join(dest, "notes.txt"), []byte("scratch"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dest, ".hidden.md"), []byte("---\nsubject: x\n---\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dest, "no-digest-here.md"), []byte("---\nsubject: x\n---\n"), 0o644))

	records, err := f.archive().Index()
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "Real", records[0].subject)
}

// TestIndexIgnoresAttachmentDirectories: sender-controlled files live under
// `<basename>.attachments` and must never be indexed as messages, whatever
// they are named.
func TestIndexIgnoresAttachmentDirectories(t *testing.T) {
	f := newFixture(t)
	f.store(message{
		id:      "m5",
		account: "personal",
		rule:    "job-search",
		subject: "Offer",
		from:    "HR <hr@acme.example>",
		date:    day(2026, time.July, 27, 9),
		body:    "Attached.",
		attach:  "offer.pdf",
	})

	records, err := f.archive().Index()
	require.NoError(t, err)
	require.Len(t, records, 1)

	// Plant a decoy that would otherwise parse as a message.
	dir := filepath.Join(filepath.Dir(records[0].path),
		strings.TrimSuffix(filepath.Base(records[0].path), sink.MarkdownExt)+sink.AttachmentsDirSuffix)
	require.DirExists(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "0-deadbeef-decoy.md"),
		[]byte("---\nsubject: decoy\n---\n\nbody\n"), 0o644))

	records, err = f.archive().Index()
	require.NoError(t, err)
	require.Len(t, records, 1, "an attachments directory is never walked for messages")
	require.Equal(t, "Offer", records[0].subject)
}

// TestNewArchiveRejectsConfigWithoutDests: a server with nothing it may read
// fails at construction rather than answering every call with an empty list.
func TestNewArchiveRejectsConfigWithoutDests(t *testing.T) {
	_, err := NewArchive(&config.Config{Rules: []config.Rule{{Name: "nowhere"}}}, discardLogger())
	require.ErrorIs(t, err, ErrNoDests)

	_, err = NewArchive(nil, discardLogger())
	require.Error(t, err)
}

// TestNewArchiveToleratesMissingDest: a destination the first cycle has not
// created yet is not an error, it is simply empty.
func TestNewArchiveToleratesMissingDest(t *testing.T) {
	f := newFixture(t)
	require.NoDirExists(t, f.dest("job-search"))

	records, err := f.archive().Index()
	require.NoError(t, err)
	require.Empty(t, records)
}

// TestSplitFrontmatter covers the malformed shapes a hand-edited archive can
// contain.
func TestSplitFrontmatter(t *testing.T) {
	front, body, err := splitFrontmatter("---\nsubject: hi\n---\n\nbody text\n")
	require.NoError(t, err)
	require.Equal(t, "subject: hi", front)
	require.Equal(t, "\nbody text\n", body)

	_, _, err = splitFrontmatter("no fence at all\n")
	require.Error(t, err)

	_, _, err = splitFrontmatter("---\nsubject: hi\nnever closed\n")
	require.Error(t, err)
}

// TestMessageIDFromPath pins the basename contract the id depends on. The
// digest length is sink.HashLen and not a number of this package's own: the
// archive parses back exactly what internal/sink writes.
func TestMessageIDFromPath(t *testing.T) {
	digest := strings.Repeat("a1b2", sink.HashLen/4)
	require.Len(t, digest, sink.HashLen)

	id, ok := messageIDFromPath("/mail/2026/07/1753000000-" + digest + "-interview-scheduled.md")
	require.True(t, ok)
	require.Equal(t, digest, id)

	// No slug at all is still a valid name, since a subject can slug to nothing.
	id, ok = messageIDFromPath("/mail/1753000000-" + digest + ".eml")
	require.True(t, ok)
	require.Equal(t, digest, id)

	for _, bad := range []string{
		"/mail/notes.md",                                        // no digest segment
		"/mail/1753000000-xyz.md",                               // too short
		"/mail/1753000000-" + digest + "a-x.md",                 // too long
		"/mail/1753000000-" + digest[:len(digest)-1] + "z-x.md", // not hex
	} {
		_, ok := messageIDFromPath(bad)
		require.False(t, ok, "%q must not yield an id", bad)
	}
}
