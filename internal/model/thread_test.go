package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// threaded builds a message with the given threading headers, so a test can
// state exactly which of them is present.
func threaded(messageID, inReplyTo, references string) []byte {
	raw := "From: Recruiting <recruiting@acme.example>\r\n" +
		"To: craig@example.org\r\n" +
		"Subject: Re: Your application\r\n" +
		"Date: Tue, 28 Jul 2026 09:15:00 +0000\r\n"
	if messageID != "" {
		raw += "Message-ID: " + messageID + "\r\n"
	}
	if inReplyTo != "" {
		raw += "In-Reply-To: " + inReplyTo + "\r\n"
	}
	if references != "" {
		raw += "References: " + references + "\r\n"
	}
	return []byte(raw + "\r\nbody\r\n")
}

// TestThreadIDProviderWinsOverSynthesis: a provider that groups threads itself
// is authoritative, so its id is used even when the headers would produce a
// different (and, for a mailing list, worse) answer.
func TestThreadIDProviderWinsOverSynthesis(t *testing.T) {
	raw := threaded("<c@acme.example>", "<b@acme.example>", "<a@acme.example> <b@acme.example>")

	msg, err := Parse("18f2a", "personal", raw, internalDate, nil, WithThreadID("18f29"))
	require.NoError(t, err)

	assert.Equal(t, "18f29", msg.ThreadID)
	assert.Equal(t, ThreadIDSourceProvider, msg.ThreadIDSource)
	assert.False(t, msg.ThreadIDSource.Synthesized())

	// The headers are still parsed and exposed; the provider id only wins the
	// choice of key.
	assert.Equal(t, "<b@acme.example>", msg.InReplyTo)
	assert.Equal(t, []string{"<a@acme.example>", "<b@acme.example>"}, msg.References)
}

// TestThreadIDSynthesizedFromReferencesRoot: the chain root, not the parent, so
// every message of a conversation lands on one key.
func TestThreadIDSynthesizedFromReferencesRoot(t *testing.T) {
	raw := threaded("<c@acme.example>", "<b@acme.example>", "<a@acme.example> <b@acme.example>")

	msg, err := Parse("m", "personal", raw, internalDate, nil)
	require.NoError(t, err)

	assert.Equal(t, "<a@acme.example>", msg.ThreadID)
	assert.Equal(t, ThreadIDSourceReferences, msg.ThreadIDSource)
	assert.True(t, msg.ThreadIDSource.Synthesized())
}

// TestThreadIDSynthesizedFromInReplyTo: no References, so the parent is the
// best key available.
func TestThreadIDSynthesizedFromInReplyTo(t *testing.T) {
	raw := threaded("<c@acme.example>", "<b@acme.example>", "")

	msg, err := Parse("m", "personal", raw, internalDate, nil)
	require.NoError(t, err)

	assert.Equal(t, "<b@acme.example>", msg.ThreadID)
	assert.Equal(t, ThreadIDSourceInReplyTo, msg.ThreadIDSource)
	assert.Nil(t, msg.References)
}

// TestThreadIDSelfKeyedWithoutThreadingHeaders: a message that starts a thread
// is a thread of one, keyed by itself — never an empty key.
func TestThreadIDSelfKeyedWithoutThreadingHeaders(t *testing.T) {
	msg, err := Parse("m", "personal", threaded("<solo@acme.example>", "", ""), internalDate, nil)
	require.NoError(t, err)

	assert.Equal(t, "<solo@acme.example>", msg.ThreadID)
	assert.Equal(t, msg.MessageID, msg.ThreadID)
	assert.Equal(t, ThreadIDSourceSelf, msg.ThreadIDSource)
	assert.Empty(t, msg.InReplyTo)
}

// TestThreadIDFallsBackToProviderIDWithoutMessageID keeps the never-empty
// promise for a message that carries no Message-ID at all.
func TestThreadIDFallsBackToProviderIDWithoutMessageID(t *testing.T) {
	msg, err := Parse("18f2a", "personal", threaded("", "", ""), internalDate, nil)
	require.NoError(t, err)

	assert.Empty(t, msg.MessageID)
	assert.Equal(t, "18f2a", msg.ThreadID)
	assert.Equal(t, ThreadIDSourceSelf, msg.ThreadIDSource)
}

// TestThreadIDFallsBackToDigestWithoutAnyIdentity is the last resort: no
// Message-ID and no provider id. The key is still stable across runs, which is
// all grouping needs.
func TestThreadIDFallsBackToDigestWithoutAnyIdentity(t *testing.T) {
	raw := threaded("", "", "")

	first, err := Parse("", "personal", raw, internalDate, nil)
	require.NoError(t, err)
	second, err := Parse("", "personal", raw, internalDate, nil)
	require.NoError(t, err)

	assert.NotEmpty(t, first.ThreadID)
	assert.True(t, strings.HasPrefix(first.ThreadID, "sha256:"), "got %q", first.ThreadID)
	assert.Equal(t, first.ThreadID, second.ThreadID, "the key must be stable across runs")
	assert.Equal(t, ThreadIDSourceSelf, first.ThreadIDSource)

	other, err := Parse("", "personal", threaded("", "", ""), internalDate, nil)
	require.NoError(t, err)
	assert.Equal(t, first.ThreadID, other.ThreadID, "same bytes, same thread")
}

// TestWithThreadIDIgnoresBlankValues lets a provider with no threading of its
// own pass its zero value unconditionally.
func TestWithThreadIDIgnoresBlankValues(t *testing.T) {
	for _, id := range []string{"", "   ", "\t\n"} {
		msg, err := Parse("m", "personal", threaded("<solo@acme.example>", "", ""), internalDate, nil,
			WithThreadID(id))
		require.NoError(t, err)
		assert.Equal(t, "<solo@acme.example>", msg.ThreadID)
		assert.Equal(t, ThreadIDSourceSelf, msg.ThreadIDSource, "a blank option must not claim to be the provider's")
	}
}

// TestParseNilOptionIsIgnored: options are variadic, and a nil in the slice
// must not take the run down.
func TestParseNilOptionIsIgnored(t *testing.T) {
	msg, err := Parse("m", "personal", threaded("<solo@acme.example>", "", ""), internalDate, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "<solo@acme.example>", msg.ThreadID)
}

// TestThreadIDSurvivesGarbageThreadingHeaders: these headers come from the
// sender, so every shape of nonsense has to parse to *something* without an
// error and without a panic.
func TestThreadIDSurvivesGarbageThreadingHeaders(t *testing.T) {
	tests := []struct {
		name       string
		inReplyTo  string
		references string
		wantThread string
		wantSource ThreadIDSource
		wantRefs   []string
	}{
		{
			name:       "empty references falls through to in-reply-to",
			inReplyTo:  "<b@acme.example>",
			references: "   ",
			wantThread: "<b@acme.example>",
			wantSource: ThreadIDSourceInReplyTo,
		},
		{
			name:       "unterminated bracket is dropped",
			references: "<a@acme.example",
			wantThread: "<self@acme.example>",
			wantSource: ThreadIDSourceSelf,
		},
		{
			name:       "empty brackets are not an id",
			inReplyTo:  "<>",
			references: "<> <>",
			wantThread: "<self@acme.example>",
			wantSource: ThreadIDSourceSelf,
		},
		{
			name:       "prose between ids is ignored",
			references: "(in reply to) <a@acme.example> ; also <b@acme.example> [sic]",
			wantThread: "<a@acme.example>",
			wantSource: ThreadIDSourceReferences,
			wantRefs:   []string{"<a@acme.example>", "<b@acme.example>"},
		},
		{
			name:       "ids glued together without whitespace",
			references: "<a@acme.example><b@acme.example>",
			wantThread: "<a@acme.example>",
			wantSource: ThreadIDSourceReferences,
			wantRefs:   []string{"<a@acme.example>", "<b@acme.example>"},
		},
		{
			name:       "reopened bracket restarts the id",
			references: "<broken <a@acme.example>",
			wantThread: "<a@acme.example>",
			wantSource: ThreadIDSourceReferences,
			wantRefs:   []string{"<a@acme.example>"},
		},
		{
			name:       "repeats collapse but keep order",
			references: "<a@acme.example> <b@acme.example> <a@acme.example>",
			wantThread: "<a@acme.example>",
			wantSource: ThreadIDSourceReferences,
			wantRefs:   []string{"<a@acme.example>", "<b@acme.example>"},
		},
		{
			name:       "in-reply-to with a trailing comment",
			inReplyTo:  "<b@acme.example> (Jane's message of Mon, 27 Jul 2026)",
			wantThread: "<b@acme.example>",
			wantSource: ThreadIDSourceInReplyTo,
		},
		{
			name:       "unbracketed but well-formed in-reply-to is kept",
			inReplyTo:  "b@acme.example",
			wantThread: "b@acme.example",
			wantSource: ThreadIDSourceInReplyTo,
		},
		{
			// A literal like this in every message of a mailbox would weld
			// unrelated conversations into one thread, so it is discarded.
			name:       "placeholder in-reply-to is discarded",
			inReplyTo:  "undefined",
			wantThread: "<self@acme.example>",
			wantSource: ThreadIDSourceSelf,
		},
		{
			name:       "sentence in-reply-to is discarded",
			inReplyTo:  "no message id here",
			wantThread: "<self@acme.example>",
			wantSource: ThreadIDSourceSelf,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := threaded("<self@acme.example>", tt.inReplyTo, tt.references)

			var msg *Message
			var err error
			require.NotPanics(t, func() { msg, err = Parse("m", "personal", raw, internalDate, nil) })

			require.NoError(t, err, "garbage threading headers must never fail a parse")
			require.NotNil(t, msg)
			assert.Equal(t, tt.wantThread, msg.ThreadID)
			assert.Equal(t, tt.wantSource, msg.ThreadIDSource)
			assert.Equal(t, tt.wantRefs, msg.References)
			assert.NotEmpty(t, msg.ThreadID, "ThreadID is never empty")
		})
	}
}

// TestThreadIDRepeatedReferencesHeaders: a message may carry the header more
// than once; the chain is the concatenation, in order.
func TestThreadIDRepeatedReferencesHeaders(t *testing.T) {
	raw := []byte("From: a@acme.example\r\n" +
		"Subject: Re: split chain\r\n" +
		"Message-ID: <c@acme.example>\r\n" +
		"References: <a@acme.example>\r\n" +
		"References: <b@acme.example>\r\n" +
		"\r\nbody\r\n")

	msg, err := Parse("m", "personal", raw, internalDate, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"<a@acme.example>", "<b@acme.example>"}, msg.References)
	assert.Equal(t, "<a@acme.example>", msg.ThreadID)
}

// TestThreadIDGroupsAReplyChain is the acceptance case: three real messages,
// one thread, no provider help. This is the drudgery consumers would otherwise
// each reimplement.
func TestThreadIDGroupsAReplyChain(t *testing.T) {
	root := parseFixture(t, "thread_root.eml")
	first := parseFixture(t, "thread_reply_1.eml")
	second := parseFixture(t, "thread_reply_2.eml")

	const thread = "<thread-001@acme.example>"
	for _, msg := range []*Message{root, first, second} {
		assert.Equal(t, thread, msg.ThreadID, "message %s left the thread", msg.MessageID)
		assert.True(t, msg.ThreadIDSource.Synthesized())
	}

	assert.Equal(t, ThreadIDSourceSelf, root.ThreadIDSource, "the opener is keyed by itself")
	assert.Equal(t, ThreadIDSourceReferences, first.ThreadIDSource)
	assert.Equal(t, ThreadIDSourceReferences, second.ThreadIDSource)

	assert.Empty(t, root.InReplyTo)
	assert.Equal(t, "<thread-001@acme.example>", first.InReplyTo)
	assert.Equal(t, "<thread-002@example.org>", second.InReplyTo)

	// The folded References header of the last reply unfolds to both ancestors,
	// oldest first.
	assert.Equal(t,
		[]string{"<thread-001@acme.example>", "<thread-002@example.org>"},
		second.References)
}

// TestThreadIDIsNeverEmpty locks the invariant every consumer relies on across
// the whole fixture corpus.
func TestThreadIDIsNeverEmpty(t *testing.T) {
	for _, name := range []string{
		"plain_text.eml", "multipart_alternative.eml", "html_only.eml",
		"rfc2047_subject.eml", "with_attachment.eml",
		"thread_root.eml", "thread_reply_1.eml", "thread_reply_2.eml",
	} {
		t.Run(name, func(t *testing.T) {
			msg := parseFixture(t, name)
			assert.NotEmpty(t, msg.ThreadID)
			assert.NotEmpty(t, msg.ThreadIDSource)
		})
	}
}
