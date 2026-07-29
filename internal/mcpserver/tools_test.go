package mcpserver

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/craigmidwinter/mail-muncher/internal/model"
)

// seedArchive writes a small but varied archive: two accounts, three rules, a
// two-message thread, an eml-only message and an attachment.
func seedArchive(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t)
	f.store(message{
		id:        "hiring-1",
		account:   "personal",
		rule:      "job-search",
		subject:   "Interview scheduled",
		from:      "Recruiter <recruiter@acme.example>",
		date:      day(2026, time.July, 20, 9),
		threadID:  "thread-hiring",
		messageID: "<hiring-1@acme.example>",
		body:      "We would like to book a call on Thursday about the platform role.",
	})
	f.store(message{
		id:        "hiring-2",
		account:   "personal",
		rule:      "job-search",
		subject:   "Re: Interview scheduled",
		from:      "Recruiter <recruiter@acme.example>",
		date:      day(2026, time.July, 22, 14),
		threadID:  "thread-hiring",
		messageID: "<hiring-2@acme.example>",
		inReplyTo: "<hiring-1@acme.example>",
		body:      "Confirming Thursday at 10:00. The panel will cover system design.",
	})
	f.store(message{
		id:       "offer-1",
		account:  "personal",
		rule:     "job-search",
		subject:  "Offer letter",
		from:     "HR <hr@acme.example>",
		date:     day(2026, time.July, 25, 8),
		threadID: "thread-offer",
		body:     "Please find the offer attached.",
		attach:   "offer.pdf",
	})
	f.store(message{
		id:      "news-1",
		account: "work",
		rule:    "newsletters",
		subject: "This week in Go",
		from:    "Digest <digest@news.example>",
		date:    day(2026, time.July, 21, 6),
		body:    "Generics are still generics. Also: system design reading list.",
	})
	return f
}

// TestListMessagesNewestFirst is the default call: no filters, everything,
// newest first.
func TestListMessagesNewestFirst(t *testing.T) {
	s := seedArchive(t).server(nil)

	out, err := s.listMessages(ListMessagesInput{})
	require.NoError(t, err)
	require.Equal(t, 4, out.Matched)
	require.Equal(t, 4, out.Count)
	require.False(t, out.Truncated)
	require.Equal(t, DefaultLimit, out.Limit)
	require.Empty(t, out.Threads, "grouping is opt-in")

	subjects := make([]string, 0, len(out.Messages))
	for _, m := range out.Messages {
		subjects = append(subjects, m.Subject)
		require.NotEmpty(t, m.ThreadID, "every summary carries a thread id")
		require.NotEmpty(t, m.ThreadIDSource)
		require.NotEmpty(t, m.ID)
		require.NotEmpty(t, m.Formats)
		require.Empty(t, m.Snippet, "list_messages has no query to snippet around")
	}
	require.Equal(t, []string{"Offer letter", "Re: Interview scheduled", "This week in Go", "Interview scheduled"}, subjects)
}

// TestListMessagesFilters covers each filter independently.
func TestListMessagesFilters(t *testing.T) {
	s := seedArchive(t).server(nil)

	byRule, err := s.listMessages(ListMessagesInput{Rule: "newsletters"})
	require.NoError(t, err)
	require.Equal(t, 1, byRule.Count)
	require.Equal(t, "This week in Go", byRule.Messages[0].Subject)

	byAccount, err := s.listMessages(ListMessagesInput{Account: "personal"})
	require.NoError(t, err)
	require.Equal(t, 3, byAccount.Count)
	for _, m := range byAccount.Messages {
		require.Equal(t, "personal", m.Account)
	}

	byThread, err := s.listMessages(ListMessagesInput{ThreadID: "thread-hiring"})
	require.NoError(t, err)
	require.Equal(t, 2, byThread.Count)

	// since is inclusive; until on a bare date covers the whole day.
	window, err := s.listMessages(ListMessagesInput{Since: "2026-07-21", Until: "2026-07-22"})
	require.NoError(t, err)
	require.Equal(t, 2, window.Count)

	single, err := s.listMessages(ListMessagesInput{Since: "2026-07-25T00:00:00Z"})
	require.NoError(t, err)
	require.Equal(t, 1, single.Count)
	require.Equal(t, "Offer letter", single.Messages[0].Subject)

	none, err := s.listMessages(ListMessagesInput{Rule: "job-search", Account: "work"})
	require.NoError(t, err)
	require.Zero(t, none.Count)
	require.NotNil(t, none.Messages, "an empty result is [], never null")
}

// TestListMessagesLimits pins the default, the ceiling and the truncation flag.
func TestListMessagesLimits(t *testing.T) {
	s := seedArchive(t).server(nil)

	capped, err := s.listMessages(ListMessagesInput{Limit: 2})
	require.NoError(t, err)
	require.Equal(t, 2, capped.Count)
	require.Equal(t, 4, capped.Matched, "matched counts before the limit")
	require.True(t, capped.Truncated)
	require.Equal(t, "Offer letter", capped.Messages[0].Subject, "the limit keeps the newest")

	over, err := s.listMessages(ListMessagesInput{Limit: 10_000})
	require.NoError(t, err)
	require.Equal(t, MaxLimit, over.Limit)

	zero, err := s.listMessages(ListMessagesInput{Limit: 0})
	require.NoError(t, err)
	require.Equal(t, DefaultLimit, zero.Limit)

	negative, err := s.listMessages(ListMessagesInput{Limit: -5})
	require.NoError(t, err)
	require.Equal(t, DefaultLimit, negative.Limit)
}

// TestListMessagesGroupsByThread: a hiring process is a thread, and the tool
// can hand it over as one.
func TestListMessagesGroupsByThread(t *testing.T) {
	s := seedArchive(t).server(nil)

	got, err := s.listMessages(ListMessagesInput{GroupByThread: true})
	require.NoError(t, err)
	require.Len(t, got.Messages, 4)
	require.Len(t, got.Threads, 3)

	// Threads appear in the order their newest message did.
	require.Equal(t, "thread-offer", got.Threads[0].ThreadID)
	require.Equal(t, "thread-hiring", got.Threads[1].ThreadID)

	hiring := got.Threads[1]
	require.Equal(t, 2, hiring.Count)
	require.Equal(t, "Interview scheduled", hiring.Subject, "the thread is named by its oldest message")
	require.Equal(t, day(2026, time.July, 20, 9), hiring.FirstDate)
	require.Equal(t, day(2026, time.July, 22, 14), hiring.LastDate)
	require.Equal(t, []string{"Recruiter <recruiter@acme.example>"}, hiring.Participants)
	require.Equal(t, string(model.ThreadIDSourceProvider), hiring.ThreadIDSource)

	// Inside a thread the order is reading order, oldest first.
	require.Equal(t, "Interview scheduled", hiring.Messages[0].Subject)
	require.Equal(t, "Re: Interview scheduled", hiring.Messages[1].Subject)
}

// TestWeakestThreadSource: a thread is only as well-attested as its worst
// member, so a mixed thread must not be reported as provider-grouped.
func TestWeakestThreadSource(t *testing.T) {
	records := []*record{
		{fileRecord: &fileRecord{threadIDSource: "provider"}},
		{fileRecord: &fileRecord{threadIDSource: "references"}},
		{fileRecord: &fileRecord{threadIDSource: "provider"}},
	}
	require.Equal(t, "references", weakestSource(records))

	require.Equal(t, "provider", weakestSource(records[:1]))
}

// TestReadMessageByIDAndPath: both ways of naming a message reach the same
// record, in full.
func TestReadMessageByIDAndPath(t *testing.T) {
	s := seedArchive(t).server(nil)

	list, err := s.listMessages(ListMessagesInput{ThreadID: "thread-offer"})
	require.NoError(t, err)
	require.Len(t, list.Messages, 1)
	summary := list.Messages[0]

	byID, err := s.readMessage(ReadMessageInput{ID: summary.ID})
	require.NoError(t, err)
	require.Equal(t, "Offer letter", byID.Message.Subject)
	require.Equal(t, "HR <hr@acme.example>", byID.Message.From)
	require.Contains(t, byID.Message.Body, "offer attached")
	require.False(t, byID.Message.BodyTruncated)
	require.True(t, byID.Message.HasAttachment)
	require.Len(t, byID.Message.Attachments, 1)
	require.Equal(t, "offer.pdf", byID.Message.Attachments[0].Filename)
	require.Positive(t, byID.Message.Attachments[0].Bytes)
	require.Len(t, byID.Message.Files, 2, "both renderings are named")
	require.Empty(t, byID.Thread, "the thread is opt-in")

	byPath, err := s.readMessage(ReadMessageInput{Path: summary.Path})
	require.NoError(t, err)
	require.Equal(t, byID.Message, byPath.Message)

	// The id lookup is case-insensitive, since it is hex.
	upper, err := s.readMessage(ReadMessageInput{ID: strings.ToUpper(summary.ID)})
	require.NoError(t, err)
	require.Equal(t, byID.Message.ID, upper.Message.ID)

	_, err = s.readMessage(ReadMessageInput{ID: "00000000"})
	require.ErrorIs(t, err, ErrNotFound)
}

// TestReadMessageReturnsThread is the thread-awareness payoff: one call gets
// the whole exchange, in reading order.
func TestReadMessageReturnsThread(t *testing.T) {
	s := seedArchive(t).server(nil)

	list, err := s.listMessages(ListMessagesInput{ThreadID: "thread-hiring"})
	require.NoError(t, err)
	require.Len(t, list.Messages, 2)

	got, err := s.readMessage(ReadMessageInput{ID: list.Messages[0].ID, Thread: true})
	require.NoError(t, err)
	require.Equal(t, "Re: Interview scheduled", got.Message.Subject)

	require.Len(t, got.Thread, 2)
	require.Equal(t, "Interview scheduled", got.Thread[0].Subject)
	require.Equal(t, "Re: Interview scheduled", got.Thread[1].Subject)
	require.Equal(t, "<hiring-1@acme.example>", got.Thread[1].InReplyTo)
	for _, m := range got.Thread {
		require.Equal(t, "thread-hiring", m.ThreadID)
		require.NotEmpty(t, m.Body)
	}
}

// TestReadMessageTruncatesBody: bodies go to an LLM, so the budget is honoured
// and the truncation is declared rather than silent.
func TestReadMessageTruncatesBody(t *testing.T) {
	f := newFixture(t)
	f.store(message{
		id:      "long",
		account: "personal",
		rule:    "job-search",
		subject: "Long",
		from:    "Recruiter <recruiter@acme.example>",
		date:    day(2026, time.July, 20, 9),
		body:    strings.Repeat("abcdefghij", 500),
	})
	s := f.server(nil)

	list, err := s.listMessages(ListMessagesInput{})
	require.NoError(t, err)
	require.Len(t, list.Messages, 1)

	short, err := s.readMessage(ReadMessageInput{ID: list.Messages[0].ID, MaxBodyChars: 100})
	require.NoError(t, err)
	require.Len(t, []rune(short.Message.Body), 100)
	require.True(t, short.Message.BodyTruncated)

	full, err := s.readMessage(ReadMessageInput{ID: list.Messages[0].ID})
	require.NoError(t, err)
	require.False(t, full.Message.BodyTruncated)
	require.Len(t, []rune(full.Message.Body), 5000)
}

// TestSearchMessages covers matching, the snippet, and the fields searched.
func TestSearchMessages(t *testing.T) {
	s := seedArchive(t).server(nil)

	got, err := s.searchMessages(SearchMessagesInput{Query: "system design"})
	require.NoError(t, err)
	require.Equal(t, 2, got.Count, "the body of a .md and of an .eml-only message both match")
	require.Equal(t, "system design", got.Query)
	for _, m := range got.Messages {
		require.Contains(t, strings.ToLower(m.Snippet), "system design")
		require.NotContains(t, m.Snippet, "\n", "a snippet is one line")
	}

	// Case-insensitive, and the subject counts.
	bySubject, err := s.searchMessages(SearchMessagesInput{Query: "OFFER LETTER"})
	require.NoError(t, err)
	require.Equal(t, 1, bySubject.Count)

	// So does the sender, and an attachment name.
	bySender, err := s.searchMessages(SearchMessagesInput{Query: "digest@news.example"})
	require.NoError(t, err)
	require.Equal(t, 1, bySender.Count)

	byAttachment, err := s.searchMessages(SearchMessagesInput{Query: "offer.pdf"})
	require.NoError(t, err)
	require.Equal(t, 1, byAttachment.Count)

	// Filters compose with the query.
	scoped, err := s.searchMessages(SearchMessagesInput{Query: "system design", Rule: "job-search"})
	require.NoError(t, err)
	require.Equal(t, 1, scoped.Count)

	windowed, err := s.searchMessages(SearchMessagesInput{Query: "system design", Since: "2026-07-22"})
	require.NoError(t, err)
	require.Equal(t, 1, windowed.Count)

	empty, err := s.searchMessages(SearchMessagesInput{Query: "no such words anywhere"})
	require.NoError(t, err)
	require.Zero(t, empty.Count)
	require.NotNil(t, empty.Messages)

	_, err = s.searchMessages(SearchMessagesInput{Query: "   "})
	require.ErrorContains(t, err, "query is required")
}

// TestSearchMessagesLimit: search is limited and truncation is reported the
// same way listing is.
func TestSearchMessagesLimit(t *testing.T) {
	s := seedArchive(t).server(nil)

	got, err := s.searchMessages(SearchMessagesInput{Query: "e", Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 1, got.Count)
	require.Greater(t, got.Matched, 1)
	require.True(t, got.Truncated)
}

// TestSnippet pins the shape of the excerpt handed to an LLM.
func TestSnippet(t *testing.T) {
	long := strings.Repeat("x", 500) + " NEEDLE " + strings.Repeat("y", 500)

	got := snippet(long, "needle")
	require.Contains(t, got, "NEEDLE")
	require.True(t, strings.HasPrefix(got, "…"))
	require.True(t, strings.HasSuffix(got, "…"))
	require.Less(t, len(got), 4*snippetRadius)

	require.Equal(t, "", snippet("nothing here", "absent"))

	// Whitespace is collapsed, so a snippet is always one line.
	require.Equal(t, "a needle b", snippet("a\n\tneedle   b", "needle"))

	// Multi-byte text is never cut mid-rune.
	wide := strings.Repeat("日本語テキスト", 40) + "needle" + strings.Repeat("日本語テキスト", 40)
	require.True(t, utf8Valid(snippet(wide, "needle")))
}

// utf8Valid reports whether s is well-formed UTF-8.
func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

// TestParseBound covers every date spelling the tools accept, and the
// end-of-day rule for a bare date used as an upper bound.
func TestParseBound(t *testing.T) {
	zero, err := parseBound("", false)
	require.NoError(t, err)
	require.True(t, zero.IsZero())

	rfc, err := parseBound("2026-07-28T09:30:00Z", false)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC), rfc)

	offset, err := parseBound("2026-07-28T09:30:00+02:00", false)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 7, 28, 7, 30, 0, 0, time.UTC), offset)

	since, err := parseBound("2026-07-28", false)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC), since)

	until, err := parseBound("2026-07-28", true)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC), until,
		"a bare date as an upper bound covers the whole day")

	_, err = parseBound("last tuesday", false)
	require.ErrorContains(t, err, "RFC3339")

	_, err = newSelector("", "", "", "nonsense", "")
	require.ErrorContains(t, err, "since:")

	_, err = newSelector("", "", "", "", "nonsense")
	require.ErrorContains(t, err, "until:")
}

// TestToolsNeverExposeCredentials: no tool result may name a credentials file,
// a token file, the state directory or the config file.
func TestToolsNeverExposeCredentials(t *testing.T) {
	f := seedArchive(t)
	s := f.server(nil)

	list, err := s.listMessages(ListMessagesInput{})
	require.NoError(t, err)
	read, err := s.readMessage(ReadMessageInput{ID: list.Messages[0].ID, Thread: true})
	require.NoError(t, err)
	search, err := s.searchMessages(SearchMessagesInput{Query: "e"})
	require.NoError(t, err)
	rules, err := s.listRules()
	require.NoError(t, err)

	blob := render(t, list) + render(t, read) + render(t, search) + render(t, rules)
	for _, secret := range []string{
		"credentials.json",
		"work-credentials.json",
		"token.json",
		"work-token.json",
		f.cfg.Path,
		f.cfg.StateDir,
	} {
		require.NotContains(t, blob, secret, "a tool result named %q", secret)
	}
}
