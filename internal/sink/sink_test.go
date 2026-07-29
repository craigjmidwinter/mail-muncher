package sink

import (
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/model"
)

// Both sinks must satisfy the interface the pipeline codes against.
var (
	_ Sink = (*EMLSink)(nil)
	_ Sink = (*MarkdownSink)(nil)
)

// Fixed fixture values. fixtureDate is 2026-07-28T09:15:00Z; the expected
// basename fragments below are hard-coded rather than recomputed so that a
// change to the naming scheme fails loudly instead of agreeing with itself.
const (
	fixtureUnix = "1785230100"
	fixtureHash = "a00d5c5e383a1c08" // sha256("personal:18ff00aa11bb22cc")[:16]
	fixtureSlug = "re-your-application-for-senior-engineer"
	fixtureBase = fixtureUnix + "-" + fixtureHash + "-" + fixtureSlug
)

var fixtureDate = time.Date(2026, 7, 28, 9, 15, 0, 0, time.UTC)

// testMessage is the canonical fixture: a plain-text reply with one recipient,
// carrying the provider thread id a Gmail-sourced reply would have.
func testMessage() *model.Message {
	return &model.Message{
		ID:             "18ff00aa11bb22cc",
		Account:        "personal",
		Raw:            []byte("From: Jane Doe <jane@acme.com>\r\nSubject: Re: Your application for Senior Engineer\r\n\r\nHi there,\r\n"),
		From:           []mail.Address{{Name: "Jane Doe", Address: "jane@acme.com"}},
		To:             []mail.Address{{Address: "me@example.com"}},
		Subject:        "Re: Your application for Senior Engineer",
		Date:           fixtureDate,
		MessageID:      "<abc123@acme.com>",
		ThreadID:       "18fe9c0d1a2b3c4d",
		ThreadIDSource: model.ThreadIDSourceProvider,
		InReplyTo:      "<application-000@example.com>",
		References:     []string{"<application-000@example.com>"},
		Labels:         []string{"INBOX"},
		TextBody:       "Hi there,\r\n\r\nThanks for applying.   \r\n",
	}
}

// testRule is the canonical rule; dest is filled in per test with t.TempDir().
func testRule(dest string, formats ...config.Format) *config.Rule {
	if len(formats) == 0 {
		formats = []config.Format{config.FormatEML}
	}
	return &config.Rule{Name: "job-search", Dest: dest, Formats: formats}
}

func TestSlug(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		want    string
	}{
		{"plain", "Hello World", "hello-world"},
		{"lowercases", "SHOUTING SUBJECT", "shouting-subject"},
		{"collapses runs", "Re:   [URGENT] -- pay??", "re-urgent-pay"},
		{"trims edges", "...leading and trailing...", "leading-and-trailing"},
		{"keeps digits", "Invoice 2026 #4711", "invoice-2026-4711"},
		{"empty subject", "", noSubjectSlug},
		{"whitespace only", "   \t\n ", noSubjectSlug},
		{"punctuation only", "!!! ??? ---", noSubjectSlug},
		{"emoji only", "🎉🎉🎉", noSubjectSlug},
		{"emoji mixed", "🎉 Offer 🎉", "offer"},
		{"non-latin script", "Здравствуйте", noSubjectSlug},
		{"non-latin mixed", "Grüße from Köln", "gr-e-from-k-ln"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Slug(tt.subject))
		})
	}
}

func TestSlugTruncatesToMaxLen(t *testing.T) {
	long := strings.Repeat("abcdefghij", 20) // 200 characters
	require.Len(t, long, 200)

	got := Slug(long)
	assert.Len(t, got, maxSlugLen)
	assert.Equal(t, long[:maxSlugLen], got)

	// A subject whose 40th slug character is a separator gets it trimmed
	// rather than kept, so the name never ends in a dangling dash.
	ragged := Slug(strings.Repeat("a", maxSlugLen-1) + " " + strings.Repeat("b", 200))
	assert.Equal(t, strings.Repeat("a", maxSlugLen-1), ragged)
	assert.False(t, strings.HasSuffix(ragged, "-"), "slug %q ends with a separator", ragged)
}

func TestBasename(t *testing.T) {
	msg := testMessage()
	assert.Equal(t, fixtureBase, Basename(msg))

	t.Run("stable across runs", func(t *testing.T) {
		assert.Equal(t, Basename(testMessage()), Basename(testMessage()))
	})

	t.Run("identity ignores everything but account and id", func(t *testing.T) {
		other := testMessage()
		other.Subject = "Something else entirely"
		other.Raw = []byte("different bytes")
		// Same account+id => same hash fragment, different slug.
		assert.Equal(t, fixtureUnix+"-"+fixtureHash+"-something-else-entirely", Basename(other))
	})

	t.Run("different account changes the hash", func(t *testing.T) {
		other := testMessage()
		other.Account = "work"
		other.ID = "msg-2"
		assert.Contains(t, Basename(other), "-4b4c0a012c00a092-")
	})

	// The digest fragment is the whole of the collision story: it has to be
	// wide enough that two unrelated messages sharing a Date second and a
	// subject slug — both of which a sender influences — still land on
	// different names. 64 bits is not reachable by a mailbox.
	t.Run("digest is 64 bits wide", func(t *testing.T) {
		assert.Equal(t, 16, HashLen)
		assert.Len(t, identity("personal", "18ff00aa11bb22cc"), HashLen)
		assert.Equal(t, fixtureHash, strings.SplitN(Basename(msg), "-", 3)[1])
	})

	t.Run("empty subject", func(t *testing.T) {
		other := testMessage()
		other.Subject = ""
		assert.Equal(t, fixtureUnix+"-"+fixtureHash+"-"+noSubjectSlug, Basename(other))
	})

	t.Run("pre-epoch date clamps to zero", func(t *testing.T) {
		other := testMessage()
		other.Date = time.Time{}
		got := Basename(other)
		assert.True(t, strings.HasPrefix(got, "0-"), "basename %q should start with 0-", got)
		assert.False(t, strings.HasPrefix(got, "-"), "basename must never start with a dash")
	})

	t.Run("nil message", func(t *testing.T) {
		assert.NotPanics(t, func() { _ = Basename(nil) })
	})
}

func TestBasenameUsesUTCNotLocalTime(t *testing.T) {
	// 2026-07-28T09:15:00Z rendered in a zone 12 hours ahead is the 28th
	// locally too, so pick an instant that straddles the month boundary.
	utc := time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC)
	west := time.FixedZone("UTC-8", -8*60*60)

	a := testMessage()
	a.Date = utc
	b := testMessage()
	b.Date = utc.In(west) // same instant, July 31st locally

	assert.Equal(t, Basename(a), Basename(b))
	assert.Equal(t, dirFor(a, testRule("/dest")), dirFor(b, testRule("/dest")))
	assert.Equal(t, "/dest/2026/08", dirFor(a, testRule("/dest")))
}

func TestNew(t *testing.T) {
	eml, err := New(config.FormatEML)
	require.NoError(t, err)
	assert.Equal(t, config.FormatEML, eml.Format())

	md, err := New(config.FormatMarkdown)
	require.NoError(t, err)
	assert.Equal(t, config.FormatMarkdown, md.Format())

	_, err = New(config.Format("pdf"))
	assert.ErrorIs(t, err, ErrUnknownFormat)
}

func TestForRule(t *testing.T) {
	t.Run("one sink per format in order", func(t *testing.T) {
		sinks, err := ForRule(testRule("/dest", config.FormatMarkdown, config.FormatEML))
		require.NoError(t, err)
		require.Len(t, sinks, 2)
		assert.Equal(t, config.FormatMarkdown, sinks[0].Format())
		assert.Equal(t, config.FormatEML, sinks[1].Format())
	})

	t.Run("duplicates collapse", func(t *testing.T) {
		sinks, err := ForRule(testRule("/dest", config.FormatEML, config.FormatEML))
		require.NoError(t, err)
		assert.Len(t, sinks, 1)
	})

	t.Run("unknown format errors", func(t *testing.T) {
		_, err := ForRule(testRule("/dest", config.Format("pdf")))
		assert.ErrorIs(t, err, ErrUnknownFormat)
		assert.Contains(t, err.Error(), "job-search")
	})

	t.Run("nil rule", func(t *testing.T) {
		sinks, err := ForRule(nil)
		require.NoError(t, err)
		assert.Empty(t, sinks)
	})
}
