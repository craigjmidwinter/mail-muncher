package sink

import (
	"errors"
	"flag"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/model"
)

// -update rewrites the golden files instead of comparing against them:
//
//	go test ./internal/sink/... -update
var update = flag.Bool("update", false, "rewrite testdata golden files")

// storeMarkdown stores msg under a fresh destination and returns the written
// path and its contents.
func storeMarkdown(t *testing.T, msg *model.Message) (string, string) {
	t.Helper()
	dest := t.TempDir()
	rule := testRule(dest, config.FormatMarkdown)

	path, skipped, err := NewMarkdown().Store(msg, rule)
	require.NoError(t, err)
	require.False(t, skipped)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return path, string(data)
}

// assertGolden compares got against testdata/golden/<name>.md.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	golden := filepath.Join("testdata", "golden", name+".md")

	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(golden), 0o755))
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
		return
	}

	want, err := os.ReadFile(golden)
	require.NoError(t, err, "missing golden file; regenerate with: go test ./internal/sink/... -update")
	assert.Equal(t, string(want), got)
}

func TestMarkdownPath(t *testing.T) {
	msg := testMessage()
	rule := testRule("/archive/job-search", config.FormatMarkdown)
	s := NewMarkdown()

	assert.Equal(t, "/archive/job-search/2026/07/"+fixtureBase+".md", s.Path(msg, rule))
	assert.Equal(t, "/archive/job-search/2026/07/"+fixtureBase+".attachments", s.AttachmentsDir(msg, rule))

	// The two sinks agree on the basename; only the extension differs.
	assert.Equal(t,
		strings.TrimSuffix(NewEML().Path(msg, rule), EMLExt),
		strings.TrimSuffix(s.Path(msg, rule), MarkdownExt))
}

func TestMarkdownTextOnlyBody(t *testing.T) {
	msg := testMessage()
	msg.TextBody = "Hi there,\r\n\r\nThanks for applying. We'd like to schedule a call.   \r\n\r\n-- \r\nJane\r\n\r\n\r\n"
	msg.HTMLBody = "<p>ignored when a text part exists</p>"

	_, got := storeMarkdown(t, msg)
	assertGolden(t, "text-only", got)

	assert.NotContains(t, got, "ignored when a text part exists", "TextBody wins over HTMLBody")
	assert.NotContains(t, got, "\r", "line endings must be normalized to LF")
	for _, line := range strings.Split(got, "\n") {
		assert.Equal(t, strings.TrimRight(line, " \t"), line, "trailing whitespace must be stripped")
	}
}

func TestMarkdownHTMLOnlyBody(t *testing.T) {
	msg := testMessage()
	msg.TextBody = ""
	msg.HTMLBody = `<html><body>
		<h1>Interview scheduled</h1>
		<p>Hi there,</p>
		<p>Thanks for applying &mdash; we'd like to <strong>schedule a call</strong>.
		Pick a slot on <a href="https://acme.example/booking">our calendar</a>.</p>
		<ul><li>30 minutes</li><li>Video</li></ul>
		<blockquote>Please reply by Friday.</blockquote>
		<img src="cid:logo-9f2a@acme.example" alt="Acme">
		<pre><code>ssh interview@acme.example</code></pre>
	</body></html>`

	_, got := storeMarkdown(t, msg)
	assertGolden(t, "html-only", got)

	assert.Contains(t, got, "# Interview scheduled")
	assert.Contains(t, got, "**schedule a call**")
	assert.Contains(t, got, "[our calendar](https://acme.example/booking)")
	assert.Contains(t, got, "cid:logo-9f2a@acme.example", "cid: references are left unresolved")
	assert.NotContains(t, got, "<strong>", "the HTML must actually be converted")
}

func TestMarkdownBlankHTMLFallsBackToPlaceholder(t *testing.T) {
	msg := testMessage()
	msg.TextBody = ""
	// Markup that converts to nothing at all.
	msg.HTMLBody = "<html><head><title>t</title></head><body><div>  </div></body></html>"

	_, got := storeMarkdown(t, msg)
	assert.Contains(t, got, noBodyPlaceholder)
}

func TestMarkdownEmptyBody(t *testing.T) {
	msg := testMessage()
	msg.TextBody = ""
	msg.HTMLBody = ""

	_, got := storeMarkdown(t, msg)
	assertGolden(t, "no-body", got)
	assert.Contains(t, got, noBodyPlaceholder)
}

func TestMarkdownWhitespaceOnlyBodyIsEmpty(t *testing.T) {
	msg := testMessage()
	msg.TextBody = "  \r\n\t\r\n   "
	msg.HTMLBody = ""

	_, got := storeMarkdown(t, msg)
	assert.Contains(t, got, noBodyPlaceholder)
}

func TestMarkdownFrontmatterEscaping(t *testing.T) {
	msg := testMessage()
	// Every character that breaks hand-formatted YAML: a colon-space that
	// would start a mapping, quotes of both kinds, a leading indicator
	// character, and a comma inside a flow sequence.
	msg.Subject = `Re: "Senior Engineer": don't #1, {yes} [ok]`
	msg.From = []mail.Address{{Name: `Jane "JD" Doe`, Address: "jane@acme.com"}}
	msg.Labels = []string{"INBOX", "Job: Search", "yes, really"}

	_, got := storeMarkdown(t, msg)
	assertGolden(t, "escaping", got)

	// The real assertion: the frontmatter parses back to exactly what went in.
	assert.Equal(t, msg.Subject, parseFrontmatter(t, got)["subject"])
	assert.Equal(t,
		[]any{"INBOX", "Job: Search", "yes, really"},
		parseFrontmatter(t, got)["labels"])
}

// TestMarkdownFrontmatterAstralPlaneSubject pins the shape go-yaml gives a
// subject containing a character outside the Basic Multilingual Plane: the
// scalar is double-quoted and the character is written as a `\U0001F389`
// escape rather than as the emoji itself. Non-astral non-ASCII (`é`) is left
// alone, so the two sit side by side here.
//
// This is not a corner case. Emoji in subjects are ordinary marketing and
// notification mail — 8 of 186 real delivered messages in one archive — and a
// consumer that reaches into the frontmatter with a regex instead of a YAML
// parser gets the escape sequence back as literal text. The golden is here so
// that fact is discoverable from the testdata rather than from production.
func TestMarkdownFrontmatterAstralPlaneSubject(t *testing.T) {
	msg := testMessage()
	msg.Subject = "🎉 Félicitations — offer accepted 🎉"

	_, got := storeMarkdown(t, msg)
	assertGolden(t, "subject-astral", got)

	assert.Contains(t, got, `\U0001F389`, "an astral-plane character is emitted as a \\U escape")
	assert.NotContains(t, strings.SplitN(got, "\n---\n", 2)[0], "🎉",
		"the emoji itself does not appear in the frontmatter")

	// The escape is a YAML escape, not a mangling: a parser gets the subject
	// back exactly. Only a reader that pattern-matches the raw bytes sees it.
	assert.Equal(t, msg.Subject, parseFrontmatter(t, got)["subject"])
}

// TestMarkdownFrontmatterNewlineSubject pins the other shape a subject can
// force: a header folded across lines, or one injected with a newline, comes
// out as a block scalar (`subject: |-`) with the value indented beneath the
// key, not as a quoted one-liner. A consumer reading frontmatter line by line
// sees a key with no value on it.
func TestMarkdownFrontmatterNewlineSubject(t *testing.T) {
	msg := testMessage()
	msg.Subject = "Re: Your application\nfrom: attacker@example.com"

	_, got := storeMarkdown(t, msg)
	assertGolden(t, "subject-newline", got)

	assert.Contains(t, got, "subject: |-", "a multi-line subject becomes a block scalar")
	assert.Equal(t, msg.Subject, parseFrontmatter(t, got)["subject"])

	// The injected line is indented into the block scalar, so it is part of
	// the subject and not a second `from:` key.
	assert.Equal(t, "Jane Doe <jane@acme.com>", parseFrontmatter(t, got)["from"])
}

// parseFrontmatter extracts and decodes the YAML block at the top of a
// rendered document.
func parseFrontmatter(t *testing.T, doc string) map[string]any {
	t.Helper()
	require.True(t, strings.HasPrefix(doc, "---\n"), "document must open with a frontmatter fence")
	rest := doc[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	require.GreaterOrEqual(t, end, 0, "frontmatter must be closed")

	var out map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(rest[:end+1]), &out))
	return out
}

func TestMarkdownFrontmatterFields(t *testing.T) {
	msg := testMessage()
	msg.Cc = []mail.Address{{Name: "Recruiting Team", Address: "recruiting@acme.com"}}

	_, got := storeMarkdown(t, msg)
	fm := parseFrontmatter(t, got)

	assert.Equal(t, "Jane Doe <jane@acme.com>", fm["from"])
	assert.Equal(t, []any{"me@example.com"}, fm["to"])
	assert.Equal(t, []any{"Recruiting Team <recruiting@acme.com>"}, fm["cc"])
	assert.Equal(t, "<abc123@acme.com>", fm["message_id"], "angle brackets are kept")
	assert.Equal(t, "18fe9c0d1a2b3c4d", fm["thread_id"], "the join key a consumer groups on")
	assert.Equal(t, "provider", fm["thread_id_source"])
	assert.Equal(t, "<application-000@example.com>", fm["in_reply_to"])
	assert.Equal(t, "personal", fm["account"])
	assert.Equal(t, "job-search", fm["rule"])
	assert.Equal(t, []any{"INBOX"}, fm["labels"])
	assert.Contains(t, got, "date: 2026-07-28T09:15:00Z")

	// The unbounded chain stays in the .eml; the frontmatter carries the key.
	assert.NotContains(t, got, "references:")
}

// TestMarkdownFrontmatterSynthesizedThread: a message whose thread was
// reconstructed from headers says so, so a reader knows the grouping is
// best-effort rather than a provider guarantee.
func TestMarkdownFrontmatterSynthesizedThread(t *testing.T) {
	msg := testMessage()
	msg.ThreadID = "<application-000@example.com>"
	msg.ThreadIDSource = model.ThreadIDSourceReferences

	_, got := storeMarkdown(t, msg)
	fm := parseFrontmatter(t, got)

	assert.Equal(t, "<application-000@example.com>", fm["thread_id"])
	assert.Equal(t, "references", fm["thread_id_source"])
}

// TestMarkdownFrontmatterThreadOfOne: a message that starts a thread is keyed
// by itself and names no parent, so a consumer still groups it unconditionally.
func TestMarkdownFrontmatterThreadOfOne(t *testing.T) {
	msg := testMessage()
	msg.ThreadID = msg.MessageID
	msg.ThreadIDSource = model.ThreadIDSourceSelf
	msg.InReplyTo = ""
	msg.References = nil

	_, got := storeMarkdown(t, msg)
	fm := parseFrontmatter(t, got)

	assert.Equal(t, "<abc123@acme.com>", fm["thread_id"])
	assert.Equal(t, "self", fm["thread_id_source"])
	assert.NotContains(t, fm, "in_reply_to")
}

func TestMarkdownFrontmatterOmitsEmptyOptionalFields(t *testing.T) {
	msg := testMessage()
	msg.Cc = nil
	msg.Labels = nil
	msg.Attachments = nil
	msg.InReplyTo = ""

	_, got := storeMarkdown(t, msg)
	front := got[:strings.Index(got, "\n---\n")]

	assert.NotContains(t, front, "cc:")
	assert.NotContains(t, front, "labels:")
	assert.NotContains(t, front, "attachments:")
	assert.NotContains(t, front, "in_reply_to:", "a thread-opening message names no parent")
	assert.Contains(t, front, "to: [", "to is always present, as a flow sequence")
	assert.Contains(t, front, "thread_id:", "thread_id is always present — it is the join key")
	assert.Contains(t, front, "thread_id_source:")
}

func TestMarkdownWritesAttachments(t *testing.T) {
	dest := t.TempDir()
	rule := testRule(dest, config.FormatMarkdown)
	msg := testMessage()
	msg.Attachments = []model.Attachment{
		{Filename: "offer.pdf", ContentType: "application/pdf", Content: []byte("%PDF-1.4 offer")},
		{Filename: "Résumé 2026.docx", ContentType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Content: []byte("docx bytes")},
	}

	s := NewMarkdown()
	path, _, err := s.Store(msg, rule)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	got := string(data)
	assertGolden(t, "attachments", got)

	attDir := s.AttachmentsDir(msg, rule)
	assert.Equal(t, filepath.Join(filepath.Dir(path), fixtureBase+".attachments"), attDir)

	pdf, err := os.ReadFile(filepath.Join(attDir, "offer.pdf"))
	require.NoError(t, err)
	assert.Equal(t, []byte("%PDF-1.4 offer"), pdf, "attachment bytes are written verbatim")

	docx, err := os.ReadFile(filepath.Join(attDir, "R-sum-2026.docx"))
	require.NoError(t, err)
	assert.Equal(t, []byte("docx bytes"), docx)

	assert.Contains(t, got, attachmentsHeading)
	assert.Contains(t, got, "- [offer.pdf]("+fixtureBase+".attachments/offer.pdf)")
	assert.Contains(t, got, "- [R-sum-2026.docx]("+fixtureBase+".attachments/R-sum-2026.docx)")
}

func TestMarkdownAttachmentCollisionDedupe(t *testing.T) {
	dest := t.TempDir()
	rule := testRule(dest, config.FormatMarkdown)
	msg := testMessage()
	msg.Attachments = []model.Attachment{
		{Filename: "invoice.pdf", Content: []byte("one")},
		{Filename: "invoice.pdf", Content: []byte("two")},
		{Filename: "invoice.pdf", Content: []byte("three")},
		{Filename: "INVOICE.pdf", Content: []byte("four")},   // collides case-insensitively
		{Filename: "invoice-2.pdf", Content: []byte("five")}, // the dedupe suffix itself collides
	}

	s := NewMarkdown()
	path, _, err := s.Store(msg, rule)
	require.NoError(t, err)

	attDir := s.AttachmentsDir(msg, rule)
	for name, want := range map[string]string{
		"invoice.pdf":     "one",
		"invoice-2.pdf":   "two",
		"invoice-3.pdf":   "three",
		"INVOICE-4.pdf":   "four", // -2 and -3 were taken, case-insensitively
		"invoice-2-2.pdf": "five", // its own name was already handed out above
	} {
		data, err := os.ReadFile(filepath.Join(attDir, name))
		require.NoError(t, err, "expected attachment %s", name)
		assert.Equal(t, want, string(data))
	}

	entries, err := os.ReadDir(attDir)
	require.NoError(t, err)
	assert.Len(t, entries, 5, "every attachment gets its own file")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	fm := parseFrontmatter(t, string(data))
	assert.Equal(t,
		[]any{"invoice.pdf", "invoice-2.pdf", "invoice-3.pdf", "INVOICE-4.pdf", "invoice-2-2.pdf"},
		fm["attachments"],
		"frontmatter lists the names as written to disk")
}

// TestMarkdownNeutralizesReservedAttachmentExtensions is the central case: a
// sender who names an attachment `evil.md` is trying to get a file of their own
// choosing into the parse loop of anything that globs `**/*.md` under the
// destination — with forged frontmatter, that is a message with an
// attacker-chosen from:, subject: and body. `forward.eml` is the same trick
// against readers of the raw copies, and needs no malice at all: a forwarded
// message is a .eml attachment.
func TestMarkdownNeutralizesReservedAttachmentExtensions(t *testing.T) {
	dest := t.TempDir()
	rule := testRule(dest, config.FormatMarkdown)
	msg := testMessage()
	msg.Attachments = []model.Attachment{
		{Filename: "evil.md", Content: []byte("---\nfrom: ceo@acme.com\nsubject: wire the money\n---\n")},
		{Filename: "forward.eml", Content: []byte("From: ceo@acme.com\r\n\r\nwire the money\r\n")},
		{Filename: "SHOUTY.MD", Content: []byte("case must not be an escape hatch")},
		{Filename: "notes.Eml", Content: []byte("nor must mixed case")},
		{Filename: "offer.pdf", Content: []byte("%PDF-1.4 offer")},
	}

	s := NewMarkdown()
	path, _, err := s.Store(msg, rule)
	require.NoError(t, err)
	doc, err := os.ReadFile(path)
	require.NoError(t, err)
	got := string(doc)
	assertGolden(t, "attachments-reserved-ext", got)

	attDir := s.AttachmentsDir(msg, rule)
	for name, want := range map[string]string{
		"evil.md.attachment":     "---\nfrom: ceo@acme.com\nsubject: wire the money\n---\n",
		"forward.eml.attachment": "From: ceo@acme.com\r\n\r\nwire the money\r\n",
		"SHOUTY.MD.attachment":   "case must not be an escape hatch",
		"notes.Eml.attachment":   "nor must mixed case",
		"offer.pdf":              "%PDF-1.4 offer", // untouched: nothing to collide with
	} {
		data, readErr := os.ReadFile(filepath.Join(attDir, name))
		require.NoError(t, readErr, "expected attachment %s", name)
		assert.Equal(t, want, string(data), "the bytes are written verbatim under the neutralized name")
	}

	// The link text is still the name the sender chose; only the target moved.
	assert.Contains(t, got, "- [evil.md]("+fixtureBase+".attachments/evil.md.attachment)")
	assert.Contains(t, got, "- [forward.eml]("+fixtureBase+".attachments/forward.eml.attachment)")
	assert.Contains(t, got, "- [offer.pdf]("+fixtureBase+".attachments/offer.pdf)")

	// The frontmatter lists what is actually on disk, so a reader can join a
	// name onto the attachments directory and find a file.
	assert.Equal(t,
		[]any{"evil.md.attachment", "forward.eml.attachment", "SHOUTY.MD.attachment", "notes.Eml.attachment", "offer.pdf"},
		parseFrontmatter(t, got)["attachments"])
	for _, name := range parseFrontmatter(t, got)["attachments"].([]any) {
		assert.FileExists(t, filepath.Join(attDir, name.(string)))
	}

	// And the point of all of it: the only .md under the whole destination is
	// the document mail-muncher delivered.
	assert.Equal(t, []string{path}, walkExt(t, dest, MarkdownExt))
	assert.Empty(t, walkExt(t, dest, EMLExt))
}

// TestMarkdownNeutralizedNamesStillDedupe: the suffix must not cost an
// attachment its own file. Two `evil.md` parts, plus a part literally named
// `evil.md.attachment` that lands on the name the first one was rewritten to.
func TestMarkdownNeutralizedNamesStillDedupe(t *testing.T) {
	dest := t.TempDir()
	rule := testRule(dest, config.FormatMarkdown)
	msg := testMessage()
	msg.Attachments = []model.Attachment{
		{Filename: "evil.md", Content: []byte("one")},
		{Filename: "evil.md", Content: []byte("two")},
		{Filename: "EVIL.md", Content: []byte("three")},
		{Filename: "evil.md.attachment", Content: []byte("four")},
	}

	s := NewMarkdown()
	path, _, err := s.Store(msg, rule)
	require.NoError(t, err)

	attDir := s.AttachmentsDir(msg, rule)
	for name, want := range map[string]string{
		"evil.md.attachment":   "one",
		"evil-2.md.attachment": "two",
		"EVIL-3.md.attachment": "three",
		// Collides with the rewritten name of the first, so it takes a counter
		// of its own — the dedupe looks at the on-disk name, not the sender's.
		"evil.md-2.attachment": "four",
	} {
		data, readErr := os.ReadFile(filepath.Join(attDir, name))
		require.NoError(t, readErr, "expected attachment %s", name)
		assert.Equal(t, want, string(data))
	}

	entries, err := os.ReadDir(attDir)
	require.NoError(t, err)
	assert.Len(t, entries, 4, "every attachment keeps its own file")
	assert.Equal(t, []string{path}, walkExt(t, dest, MarkdownExt))
}

func TestNeutralizeExt(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"markdown", "evil.md", "evil.md.attachment"},
		{"eml", "forward.eml", "forward.eml.attachment"},
		{"upper markdown", "EVIL.MD", "EVIL.MD.attachment"},
		{"mixed eml", "Forward.Eml", "Forward.Eml.attachment"},
		{"dotted stem", "notes.2026.md", "notes.2026.md.attachment"},
		{"already suffixed", "evil.md.attachment", "evil.md.attachment"},
		{"unrelated", "offer.pdf", "offer.pdf"},
		{"no extension", "README", "README"},
		{"md inside the stem", "readme.md.txt", "readme.md.txt"},
		{"embedded, not suffix", "sendmail", "sendmail"},
		{"fallback name", fallbackAttachmentName, fallbackAttachmentName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := neutralizeExt(tt.in)
			assert.Equal(t, tt.want, got)
			assert.NotEqual(t, MarkdownExt, strings.ToLower(filepath.Ext(got)))
			assert.NotEqual(t, EMLExt, strings.ToLower(filepath.Ext(got)))
		})
	}

	// The two rules compose in the right order: the sanitizer truncates first
	// and can re-expose the extension it was preserving, so neutralizing runs
	// on its output, not on the sender's raw string. The suffix is the only
	// thing that pushes a name past the sanitizer's cap, and 131 bytes is
	// still far inside every filesystem's limit.
	t.Run("after truncation", func(t *testing.T) {
		got := neutralizeExt(sanitizeFilename(strings.Repeat("x", 400) + ".md"))
		assert.True(t, strings.HasSuffix(got, MarkdownExt+AttachmentSuffix), "got %q", got)
		assert.LessOrEqual(t, len(got), maxAttachmentNameLen+len(AttachmentSuffix))
	})
}

// TestNeutralizeExtCoversEveryDeliveredExtension keeps the reserved set and the
// sinks from drifting apart: a third format added to the package must either
// reserve its extension here or explain why it need not.
func TestNeutralizeExtCoversEveryDeliveredExtension(t *testing.T) {
	for _, ext := range []string{MarkdownExt, EMLExt} {
		assert.Contains(t, reservedExts[:], ext)
		assert.NotEqual(t, "sample"+ext, neutralizeExt("sample"+ext))
	}
	// Applying the scheme to its own output must be a no-op, or a re-run would
	// walk a name further from the sender's every time.
	for _, name := range []string{"evil.md", "forward.eml", "offer.pdf", "x" + AttachmentSuffix} {
		once := neutralizeExt(name)
		assert.Equal(t, once, neutralizeExt(once), "neutralizeExt must be idempotent")
	}
}

func TestMarkdownSkipsExistingFile(t *testing.T) {
	dest := t.TempDir()
	rule := testRule(dest, config.FormatMarkdown)
	msg := testMessage()
	msg.Attachments = []model.Attachment{{Filename: "offer.pdf", Content: []byte("original")}}
	s := NewMarkdown()

	path, skipped, err := s.Store(msg, rule)
	require.NoError(t, err)
	require.False(t, skipped)

	sentinel := []byte("edited by hand")
	require.NoError(t, os.WriteFile(path, sentinel, 0o644))
	attachment := filepath.Join(s.AttachmentsDir(msg, rule), "offer.pdf")
	require.NoError(t, os.WriteFile(attachment, []byte("edited attachment"), 0o644))

	again, skipped, err := s.Store(msg, rule)
	require.NoError(t, err)
	assert.True(t, skipped)
	assert.Equal(t, path, again)

	doc, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, sentinel, doc)

	att, err := os.ReadFile(attachment)
	require.NoError(t, err)
	assert.Equal(t, "edited attachment", string(att), "a skip must not rewrite attachments either")
}

func TestMarkdownPlanDoesNotWrite(t *testing.T) {
	dest := t.TempDir()
	rule := testRule(dest, config.FormatMarkdown)
	msg := testMessage()
	msg.Attachments = []model.Attachment{{Filename: "offer.pdf", Content: []byte("bytes")}}
	s := NewMarkdown()

	path, skipped, err := s.Plan(msg, rule)
	require.NoError(t, err)
	assert.False(t, skipped)
	assert.Equal(t, s.Path(msg, rule), path)

	entries, err := os.ReadDir(dest)
	require.NoError(t, err)
	assert.Empty(t, entries, "a dry run must not create directories or attachments")

	_, _, err = s.Store(msg, rule)
	require.NoError(t, err)
	_, skipped, err = s.Plan(msg, rule)
	require.NoError(t, err)
	assert.True(t, skipped)
}

func TestMarkdownWriteFailureLeavesNoDroppings(t *testing.T) {
	dest := t.TempDir()
	rule := testRule(dest, config.FormatMarkdown)
	msg := testMessage()
	msg.Attachments = []model.Attachment{{Filename: "offer.pdf", Content: []byte("bytes")}}
	s := NewMarkdown()

	boom := errors.New("disk on fire")
	restore := writeAndSync
	writeAndSync = func(*os.File, []byte) error { return boom }
	t.Cleanup(func() { writeAndSync = restore })

	path, skipped, err := s.Store(msg, rule)
	require.ErrorIs(t, err, boom)
	assert.False(t, skipped)
	assert.NoFileExists(t, path)

	entries, err := os.ReadDir(filepath.Join(dest, "2026", "07"))
	require.NoError(t, err)
	assert.Empty(t, entries, "neither a temp file nor a half-filled attachments directory may survive")
}

func TestMarkdownDocumentFailureRemovesAttachmentsDir(t *testing.T) {
	dest := t.TempDir()
	rule := testRule(dest, config.FormatMarkdown)
	msg := testMessage()
	msg.Attachments = []model.Attachment{{Filename: "offer.pdf", Content: []byte("bytes")}}
	s := NewMarkdown()

	// Let the attachments through, then fail on the document itself.
	boom := errors.New("no space left")
	restore := writeAndSync
	calls := 0
	writeAndSync = func(f *os.File, data []byte) error {
		calls++
		if calls > 1 {
			return boom
		}
		return restore(f, data)
	}
	t.Cleanup(func() { writeAndSync = restore })

	_, _, err := s.Store(msg, rule)
	require.ErrorIs(t, err, boom)

	assert.NoDirExists(t, s.AttachmentsDir(msg, rule), "attachments written for a document that never landed are rolled back")
	entries, err := os.ReadDir(filepath.Join(dest, "2026", "07"))
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestMarkdownStoreRejectsRuleWithoutDest(t *testing.T) {
	_, _, err := NewMarkdown().Store(testMessage(), &config.Rule{Name: "no-dest"})
	assert.ErrorIs(t, err, ErrNoDest)
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "offer.pdf", "offer.pdf"},
		{"keeps case", "Offer_Letter-v2.PDF", "Offer_Letter-v2.PDF"},
		{"spaces", "my resume 2026.pdf", "my-resume-2026.pdf"},
		{"unicode", "Résumé.pdf", "R-sum-.pdf"},
		{"unix path", "../../etc/passwd", "passwd"},
		{"windows path", `C:\Users\jane\offer.pdf`, "offer.pdf"},
		{"dot dot", "..", fallbackAttachmentName},
		{"hidden file", ".bashrc", "bashrc"},
		{"all junk", "***", fallbackAttachmentName},
		{"control characters", "re\x00port\n.pdf", "re-port-.pdf"},
		{"collapses separators", "a   b___c...d", "a-b___c...d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeFilename(tt.in)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, filepath.Base(got), "the result must be a single path element")
		})
	}

	t.Run("bounded length", func(t *testing.T) {
		got := sanitizeFilename(strings.Repeat("x", 400) + ".pdf")
		assert.LessOrEqual(t, len(got), maxAttachmentNameLen)
		assert.True(t, strings.HasSuffix(got, ".pdf"), "the extension survives truncation: %q", got)
	})
}

func TestMarkdownFilePermissions(t *testing.T) {
	dest := t.TempDir()
	rule := testRule(dest, config.FormatMarkdown)
	msg := testMessage()
	msg.Attachments = []model.Attachment{{Filename: "offer.pdf", Content: []byte("bytes")}}
	s := NewMarkdown()

	path, _, err := s.Store(msg, rule)
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	attDir := s.AttachmentsDir(msg, rule)
	dirInfo, err := os.Stat(attDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm(),
		"a decoded attachment is as private as the mail it came in")

	att, err := os.Stat(filepath.Join(attDir, "offer.pdf"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), att.Mode().Perm())
}
