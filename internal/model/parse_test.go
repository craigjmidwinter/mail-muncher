package model

import (
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// internalDate stands in for the provider-supplied timestamp.
var internalDate = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err, "reading fixture %s", name)
	return raw
}

func parseFixture(t *testing.T, name string, labels ...string) *Message {
	t.Helper()
	raw := fixture(t, name)
	msg, err := Parse("msg-id-1", "personal", raw, internalDate, labels)
	require.NoError(t, err)
	require.NotNil(t, msg)
	return msg
}

func TestParsePlainText(t *testing.T) {
	msg := parseFixture(t, "plain_text.eml", "INBOX", "IMPORTANT")

	assert.Equal(t, "msg-id-1", msg.ID)
	assert.Equal(t, "personal", msg.Account)
	assert.Equal(t, fixture(t, "plain_text.eml"), msg.Raw, "Raw must be byte-faithful")
	assert.Equal(t, "Quarterly update", msg.Subject)
	assert.Equal(t, "<plain-001@acme.example>", msg.MessageID)
	assert.Equal(t, []string{"INBOX", "IMPORTANT"}, msg.Labels)

	assert.Equal(t, time.Date(2026, 7, 28, 9, 15, 0, 0, time.UTC), msg.Date.UTC())

	require.Len(t, msg.From, 1)
	assert.Equal(t, "Jane Doe", msg.From[0].Name)
	assert.Equal(t, "jane@Acme.Example", msg.From[0].Address)

	require.Len(t, msg.To, 1)
	assert.Equal(t, "craig@example.org", msg.To[0].Address)
	require.Len(t, msg.Cc, 2)
	assert.Equal(t, []string{"team@ACME.example", "bare@sub.acme.example"}, msg.RecipientAddresses()[1:])

	assert.Contains(t, msg.TextBody, "Just checking in on the quarterly numbers.")
	assert.Empty(t, msg.HTMLBody)
	assert.False(t, msg.HasAttachment())
	assert.Empty(t, msg.Attachments)
}

func TestParsePlainTextHelpers(t *testing.T) {
	msg := parseFixture(t, "plain_text.eml", "INBOX")

	assert.Equal(t, []string{"acme.example"}, msg.FromDomains())
	assert.Equal(t, []string{"jane@Acme.Example"}, msg.FromAddresses())
	assert.Equal(t,
		[]string{"craig@example.org", "team@ACME.example", "bare@sub.acme.example"},
		msg.RecipientAddresses())
	assert.Len(t, msg.Recipients(), 3)

	// To and Cc separately, for consumers that need to tell them apart. Case is
	// preserved: the local part of an addr-spec is case-sensitive in principle.
	assert.Equal(t, []string{"craig@example.org"}, msg.ToAddresses())
	assert.Equal(t, []string{"team@ACME.example", "bare@sub.acme.example"}, msg.CcAddresses())
	assert.Equal(t, append(msg.ToAddresses(), msg.CcAddresses()...), msg.RecipientAddresses())

	assert.True(t, msg.HasLabel("INBOX"))
	assert.False(t, msg.HasLabel("inbox"), "label matching is exact")
	assert.False(t, msg.HasLabel("SPAM"))
}

func TestParseHeaderAccess(t *testing.T) {
	msg := parseFixture(t, "plain_text.eml")

	// Case-insensitive lookup, whatever case the rule author wrote.
	assert.Equal(t, "hand-written", msg.Header("X-Mailer"))
	assert.Equal(t, "hand-written", msg.Header("x-mailer"))
	assert.Equal(t, "hand-written", msg.Header("X-MAILER"))

	// Repeated headers keep every value, in order.
	assert.Equal(t, []string{"spring-2026", "rerun-2026"}, msg.HeaderValues("X-Campaign-Id"))

	// Structural headers survive too.
	assert.Contains(t, msg.Header("Content-Type"), "text/plain")
	assert.Equal(t, "<plain-001@acme.example>", msg.Header("Message-ID"))

	// Absent headers are empty, never a panic.
	assert.Nil(t, msg.HeaderValues("X-Does-Not-Exist"))
	assert.Equal(t, "", msg.Header("X-Does-Not-Exist"))
}

func TestParseMultipartAlternative(t *testing.T) {
	msg := parseFixture(t, "multipart_alternative.eml")

	assert.Equal(t, "Your application was received", msg.Subject)
	assert.Contains(t, msg.TextBody, "Thanks for applying to Recruiting Example.")
	assert.Contains(t, msg.HTMLBody, "<b>Recruiting Example</b>")
	assert.NotContains(t, msg.TextBody, "<b>", "text part must not be the HTML part")
	assert.False(t, msg.HasAttachment())

	assert.Equal(t, []string{"jobs.recruiting.example"}, msg.FromDomains())
	assert.Equal(t, time.Date(2026, 7, 27, 22, 4, 11, 0, time.UTC), msg.Date.UTC())
}

func TestParseHTMLOnlyLeavesTextBodyEmpty(t *testing.T) {
	msg := parseFixture(t, "html_only.eml")

	assert.Empty(t, msg.TextBody, "HTML must not be auto-down-converted into TextBody")
	assert.Contains(t, msg.HTMLBody, "<h1>This week</h1>")
}

func TestParseWithAttachment(t *testing.T) {
	msg := parseFixture(t, "with_attachment.eml")

	assert.True(t, msg.HasAttachment())
	require.Len(t, msg.Attachments, 2)

	pdf := msg.Attachments[0]
	assert.Equal(t, "offer letter.pdf", pdf.Filename)
	assert.Equal(t, "application/pdf", pdf.ContentType)
	assert.Equal(t, "Hello, attachment!\nSecond line.\n", string(pdf.Content),
		"content must be base64-decoded")

	csv := msg.Attachments[1]
	assert.Equal(t, "benefits.csv", csv.Filename)
	assert.Equal(t, "text/csv", csv.ContentType)
	assert.Contains(t, string(csv.Content), "gold,100")

	assert.Equal(t, []string{"offer letter.pdf", "benefits.csv"}, msg.AttachmentFilenames())
	assert.Contains(t, msg.TextBody, "Please find the offer letter attached.")
}

func TestParseRFC2047EncodedHeaders(t *testing.T) {
	msg := parseFixture(t, "rfc2047_subject.eml")

	assert.Equal(t, "Café réunion: ordre du jour", msg.Subject)
	require.Len(t, msg.From, 1)
	assert.Equal(t, "José García", msg.From[0].Name)
	assert.Equal(t, "jose@Cafe.Example", msg.From[0].Address)
	assert.Equal(t, []string{"cafe.example"}, msg.FromDomains())

	// Arbitrary headers are decoded too, so `header:` regexes see UTF-8.
	assert.Equal(t, "déjà vu", msg.Header("X-Note"))

	// iso-8859-1 quoted-printable body converted to UTF-8.
	assert.Contains(t, msg.TextBody, "Le café est fermé.")
}

func TestParseBrokenMessageReturnsError(t *testing.T) {
	raw := fixture(t, "broken.eml")

	msg, err := Parse("broken-1", "personal", raw, internalDate, nil)

	require.Error(t, err, "a message with no parseable header block must error")
	assert.Nil(t, msg)
	assert.Contains(t, err.Error(), "broken-1", "error must identify the message")
}

func TestParseEmptyInputReturnsError(t *testing.T) {
	for name, raw := range map[string][]byte{
		"nil":        nil,
		"empty":      {},
		"whitespace": []byte("\r\n\r\n  \r\n"),
	} {
		t.Run(name, func(t *testing.T) {
			msg, err := Parse("empty-1", "personal", raw, internalDate, nil)
			require.ErrorIs(t, err, ErrEmptyMessage)
			assert.Nil(t, msg)
		})
	}
}

func TestParseDoesNotPanicOnHostileInput(t *testing.T) {
	hostile := [][]byte{
		[]byte("Subject: no body\r\n"),
		[]byte("Content-Type: multipart/mixed; boundary=\"X\"\r\n\r\n--X\r\n"),
		[]byte("Content-Type: multipart/mixed\r\n\r\nno boundary param at all\r\n"),
		[]byte("From: <<<>>>\r\nTo: \r\nDate: not-a-date\r\n\r\nbody\r\n"),
		[]byte("Subject: \x00\x01\x02\r\nContent-Transfer-Encoding: base64\r\n\r\n!!!not-base64!!!\r\n"),
		[]byte("\r\n\r\nbody only, no headers\r\n"),
	}
	for i, raw := range hostile {
		i, raw := i, raw
		t.Run("case", func(t *testing.T) {
			assert.NotPanics(t, func() {
				msg, err := Parse("hostile", "personal", raw, internalDate, nil)
				if err != nil {
					assert.Nil(t, msg, "case %d returned both a message and an error", i)
					return
				}
				require.NotNil(t, msg)
				// Helpers must be safe on whatever survived parsing.
				_ = msg.FromDomains()
				_ = msg.HasAttachment()
				_ = msg.Header("Subject")
				_ = msg.RecipientAddresses()
			})
		})
	}
}

func TestParseDateFallsBackToInternalDate(t *testing.T) {
	raw := []byte("From: a@b.example\r\nSubject: no date header\r\n\r\nbody\r\n")

	msg, err := Parse("nodate", "personal", raw, internalDate, nil)

	require.NoError(t, err)
	assert.Equal(t, internalDate, msg.Date)
}

func TestParseDateHeaderWinsOverInternalDate(t *testing.T) {
	msg := parseFixture(t, "plain_text.eml")
	assert.NotEqual(t, internalDate, msg.Date)
}

func TestParseLabelsAreCopied(t *testing.T) {
	labels := []string{"INBOX"}
	raw := fixture(t, "plain_text.eml")

	msg, err := Parse("m", "personal", raw, internalDate, labels)
	require.NoError(t, err)

	labels[0] = "MUTATED"
	assert.Equal(t, []string{"INBOX"}, msg.Labels, "Parse must not alias the caller's slice")
}

func TestAttachmentWithoutFilenameGetsFallback(t *testing.T) {
	raw := []byte("Subject: unnamed\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"B\"\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nbody\r\n" +
		"--B\r\nContent-Type: application/pdf\r\n" +
		"Content-Disposition: attachment\r\n\r\n" +
		"pdf-bytes\r\n" +
		"--B--\r\n")

	msg, err := Parse("unnamed", "personal", raw, internalDate, nil)

	require.NoError(t, err)
	require.Len(t, msg.Attachments, 1)
	assert.Equal(t, "attachment-1.pdf", msg.Attachments[0].Filename,
		"sinks need a non-empty name to write to disk")
}

func TestNilMessageHelpersAreSafe(t *testing.T) {
	var msg *Message
	assert.NotPanics(t, func() {
		assert.Nil(t, msg.FromDomains())
		assert.Nil(t, msg.FromAddresses())
		assert.Nil(t, msg.ToAddresses())
		assert.Nil(t, msg.CcAddresses())
		assert.Nil(t, msg.Recipients())
		assert.Nil(t, msg.RecipientAddresses())
		assert.Nil(t, msg.AttachmentFilenames())
		assert.Nil(t, msg.HeaderValues("Subject"))
		assert.Equal(t, "", msg.Header("Subject"))
		assert.False(t, msg.HasAttachment())
		assert.False(t, msg.HasLabel("INBOX"))
	})
}

func TestHeaderValuesFallsBackToCaseInsensitiveScan(t *testing.T) {
	// Headers that do not survive canonicalisation (a space before the colon,
	// exotic bytes) keep their original key; lookup must still find them.
	msg := &Message{Headers: textproto.MIMEHeader{
		"X-Weird Header": []string{"present"},
	}}
	assert.Equal(t, []string{"present"}, msg.HeaderValues("x-weird header"))
	assert.Equal(t, "present", msg.Header("X-Weird Header"))
	assert.Nil(t, msg.HeaderValues("X-Other"))
}

func TestFromDomainsMultipleAndMalformed(t *testing.T) {
	msg := &Message{From: []mail.Address{
		{Address: "a@Example.COM"},
		{Address: "malformed-no-at"},
		{Address: "b@mail.Example.com"},
		{Address: "trailing@"},
	}}
	assert.Equal(t, []string{"example.com", "mail.example.com"}, msg.FromDomains())
}
