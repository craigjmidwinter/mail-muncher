package mcpserver

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
)

// Limits every tool applies. They are part of the contract an agent codes
// against: a call that asks for more comes back capped, with truncated set,
// rather than refused.
const (
	// DefaultLimit is the number of summaries returned when `limit` is omitted.
	DefaultLimit = 50
	// MaxLimit is the ceiling on `limit`.
	MaxLimit = 500
	// DefaultBodyChars is how much of a body read_message returns by default.
	DefaultBodyChars = 20000
	// MaxBodyChars is the ceiling on `max_body_chars`.
	MaxBodyChars = 500000
	// snippetRadius is how many characters of context search_messages puts on
	// each side of a hit.
	snippetRadius = 120
)

// MessageSummary is one stored message as list_messages and search_messages
// report it: enough to decide whether to open it, and never the body.
//
// ThreadID is always present, so a caller can group a page of results into
// conversations without a second call. ThreadIDSource says how much to trust
// that grouping: "provider" means the mail provider grouped the thread itself,
// and "references" / "in_reply_to" / "self" mean mail-muncher reconstructed it
// from headers the sender controls.
type MessageSummary struct {
	ID             string    `json:"id" jsonschema:"stable identity digest for this message; pass it to read_message"`
	Path           string    `json:"path" jsonschema:"absolute path of the preferred rendering on disk"`
	Rule           string    `json:"rule" jsonschema:"name of the rule that claimed this message"`
	Account        string    `json:"account,omitempty" jsonschema:"configured account the message was fetched from"`
	From           string    `json:"from,omitempty" jsonschema:"From header, display name included when there is one"`
	To             []string  `json:"to,omitempty" jsonschema:"To recipients"`
	Subject        string    `json:"subject" jsonschema:"Subject header"`
	Date           time.Time `json:"date" jsonschema:"message date in UTC"`
	MessageID      string    `json:"message_id,omitempty" jsonschema:"RFC822 Message-ID header"`
	ThreadID       string    `json:"thread_id" jsonschema:"conversation this message belongs to; never empty"`
	ThreadIDSource string    `json:"thread_id_source" jsonschema:"how thread_id was arrived at: provider, references, in_reply_to or self"`
	Labels         []string  `json:"labels,omitempty" jsonschema:"provider labels or folders"`
	HasAttachment  bool      `json:"has_attachment" jsonschema:"whether the message carries a non-inline attachment"`
	Formats        []string  `json:"formats" jsonschema:"renderings of this message that exist on disk"`
	Snippet        string    `json:"snippet,omitempty" jsonschema:"text around the search hit, for search_messages"`
}

// AttachmentInfo names one attachment and how big it is. The bytes themselves
// are never returned: an agent that wants one opens the file next to the
// message.
type AttachmentInfo struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
	Bytes       int64  `json:"bytes"`
}

// FileRef is one rendering of a message on disk.
type FileRef struct {
	Format string `json:"format" jsonschema:"eml or markdown"`
	Path   string `json:"path" jsonschema:"absolute path on disk"`
	Bytes  int64  `json:"bytes"`
}

// MessageDetail is one stored message in full, as read_message returns it.
type MessageDetail struct {
	ID             string           `json:"id"`
	Path           string           `json:"path"`
	Rule           string           `json:"rule"`
	Account        string           `json:"account,omitempty"`
	From           string           `json:"from,omitempty"`
	To             []string         `json:"to,omitempty"`
	Cc             []string         `json:"cc,omitempty"`
	Subject        string           `json:"subject"`
	Date           time.Time        `json:"date"`
	MessageID      string           `json:"message_id,omitempty"`
	ThreadID       string           `json:"thread_id"`
	ThreadIDSource string           `json:"thread_id_source"`
	InReplyTo      string           `json:"in_reply_to,omitempty"`
	Labels         []string         `json:"labels,omitempty"`
	HasAttachment  bool             `json:"has_attachment"`
	Attachments    []AttachmentInfo `json:"attachments,omitempty"`
	Files          []FileRef        `json:"files"`
	Body           string           `json:"body"`
	BodyFormat     string           `json:"body_format" jsonschema:"markdown when the body came from a rendered .md or an HTML part, text otherwise"`
	BodyTruncated  bool             `json:"body_truncated"`
}

// ThreadSummary groups one conversation. A hiring process, a support ticket or
// a booking is a thread, not a message, so list_messages can return them that
// way directly.
type ThreadSummary struct {
	ThreadID       string           `json:"thread_id"`
	ThreadIDSource string           `json:"thread_id_source" jsonschema:"the weakest source among the thread's messages, so a partly-reconstructed thread is not reported as provider-grouped"`
	Subject        string           `json:"subject" jsonschema:"subject of the oldest message in the thread"`
	Count          int              `json:"count"`
	FirstDate      time.Time        `json:"first_date"`
	LastDate       time.Time        `json:"last_date"`
	Participants   []string         `json:"participants,omitempty" jsonschema:"distinct From addresses in the thread, oldest first"`
	Messages       []MessageSummary `json:"messages" jsonschema:"the thread's messages, oldest first"`
}

// ---------------------------------------------------------------- list_messages

// ListMessagesInput are the arguments of list_messages. Every filter is
// optional; with none of them the call returns the newest DefaultLimit
// messages across the whole archive.
type ListMessagesInput struct {
	Rule          string `json:"rule,omitempty" jsonschema:"only messages claimed by this rule (see list_rules)"`
	Account       string `json:"account,omitempty" jsonschema:"only messages from this configured account"`
	ThreadID      string `json:"thread_id,omitempty" jsonschema:"only messages in this conversation"`
	Since         string `json:"since,omitempty" jsonschema:"inclusive lower bound on the message date, RFC3339 or YYYY-MM-DD"`
	Until         string `json:"until,omitempty" jsonschema:"upper bound on the message date, RFC3339 or YYYY-MM-DD; a bare date includes the whole day"`
	Limit         int    `json:"limit,omitempty" jsonschema:"maximum messages to return, default 50, maximum 500"`
	GroupByThread bool   `json:"group_by_thread,omitempty" jsonschema:"also group the results into conversations, oldest message first within each"`
}

// ListMessagesOutput is what list_messages returns.
type ListMessagesOutput struct {
	Messages  []MessageSummary `json:"messages" jsonschema:"matching messages, newest first"`
	Threads   []ThreadSummary  `json:"threads,omitempty" jsonschema:"the returned messages grouped into conversations, most recently active first; only when group_by_thread was set. A thread whose other messages fell outside the filters or the limit is shown partially; filter by thread_id to get all of it"`
	Count     int              `json:"count" jsonschema:"messages returned"`
	Matched   int              `json:"matched" jsonschema:"messages matching the filters before the limit was applied"`
	Truncated bool             `json:"truncated" jsonschema:"whether matched exceeded the limit"`
	Limit     int              `json:"limit" jsonschema:"the limit actually applied"`
}

// listMessages answers "what is in the archive", newest first.
func (s *Server) listMessages(in ListMessagesInput) (ListMessagesOutput, error) {
	records, err := s.archive.Index()
	if err != nil {
		return ListMessagesOutput{}, err
	}

	sel, err := newSelector(in.Rule, in.Account, in.ThreadID, in.Since, in.Until)
	if err != nil {
		return ListMessagesOutput{}, err
	}

	matched := sel.apply(records)
	limit := clampLimit(in.Limit)

	page := matched
	if len(page) > limit {
		page = page[:limit]
	}

	out := ListMessagesOutput{
		Messages:  summaries(page, ""),
		Count:     len(page),
		Matched:   len(matched),
		Truncated: len(matched) > limit,
		Limit:     limit,
	}
	if in.GroupByThread {
		out.Threads = groupThreads(page)
	}
	return out, nil
}

// ---------------------------------------------------------------- read_message

// ReadMessageInput are the arguments of read_message. Exactly one of ID and
// Path is required.
type ReadMessageInput struct {
	ID           string `json:"id,omitempty" jsonschema:"the id from a message summary"`
	Path         string `json:"path,omitempty" jsonschema:"a path from a message summary or a run manifest; must be inside a configured destination directory"`
	Thread       bool   `json:"thread,omitempty" jsonschema:"also return every other message in the same conversation, oldest first"`
	MaxBodyChars int    `json:"max_body_chars,omitempty" jsonschema:"truncate each body to this many characters, default 20000, maximum 500000"`
}

// ReadMessageOutput is what read_message returns.
type ReadMessageOutput struct {
	Message MessageDetail   `json:"message"`
	Thread  []MessageDetail `json:"thread,omitempty" jsonschema:"the whole conversation including this message, oldest first; only when thread was set"`
}

// readMessage returns one message in full, and optionally its conversation.
//
// Returning the thread is nearly free — the index already groups by thread id —
// and it is what an agent asking about a hiring process or a booking actually
// wants, so it is a flag rather than a second round trip.
func (s *Server) readMessage(in ReadMessageInput) (ReadMessageOutput, error) {
	records, err := s.archive.Index()
	if err != nil {
		return ReadMessageOutput{}, err
	}

	target, err := s.locate(records, in.ID, in.Path)
	if err != nil {
		return ReadMessageOutput{}, err
	}

	budget := clampBodyChars(in.MaxBodyChars)
	out := ReadMessageOutput{Message: detailOf(target, budget)}

	if in.Thread {
		thread := make([]*record, 0, 4)
		for _, r := range records {
			if r.threadID == target.threadID {
				thread = append(thread, r)
			}
		}
		sortByDateAscending(thread)
		out.Thread = make([]MessageDetail, 0, len(thread))
		for _, r := range thread {
			out.Thread = append(out.Thread, detailOf(r, budget))
		}
	}
	return out, nil
}

// locate finds the record a read_message call names, by id or by path.
func (s *Server) locate(records []*record, id, path string) (*record, error) {
	id, path = strings.TrimSpace(id), strings.TrimSpace(path)
	switch {
	case id == "" && path == "":
		return nil, errors.New("one of id or path is required")
	case id != "" && path != "":
		return nil, errors.New("give either id or path, not both")
	}

	if path != "" {
		// The jail runs first, on the caller's string, before anything is
		// compared against the index.
		resolved, _, err := s.archive.Resolve(path)
		if err != nil {
			return nil, err
		}
		for _, r := range records {
			for _, f := range r.files {
				if f.path == resolved {
					return r, nil
				}
			}
		}
		return nil, fmt.Errorf("%w: %s", ErrNotFound, echo(path))
	}

	id = strings.ToLower(id)
	for _, r := range records {
		if r.id == id {
			return r, nil
		}
	}
	return nil, fmt.Errorf("%w with id %s", ErrNotFound, echo(id))
}

// -------------------------------------------------------------- search_messages

// SearchMessagesInput are the arguments of search_messages.
type SearchMessagesInput struct {
	Query    string `json:"query" jsonschema:"text to look for, matched case-insensitively in the subject, sender, recipients, labels, attachment names and body"`
	Rule     string `json:"rule,omitempty" jsonschema:"only messages claimed by this rule"`
	Account  string `json:"account,omitempty" jsonschema:"only messages from this configured account"`
	ThreadID string `json:"thread_id,omitempty" jsonschema:"only messages in this conversation"`
	Since    string `json:"since,omitempty" jsonschema:"inclusive lower bound on the message date, RFC3339 or YYYY-MM-DD"`
	Until    string `json:"until,omitempty" jsonschema:"upper bound on the message date, RFC3339 or YYYY-MM-DD; a bare date includes the whole day"`
	Limit    int    `json:"limit,omitempty" jsonschema:"maximum messages to return, default 50, maximum 500"`
}

// SearchMessagesOutput is what search_messages returns.
type SearchMessagesOutput struct {
	Query     string           `json:"query"`
	Messages  []MessageSummary `json:"messages" jsonschema:"matching messages, newest first, each with a snippet around the hit"`
	Count     int              `json:"count"`
	Matched   int              `json:"matched"`
	Truncated bool             `json:"truncated"`
	Limit     int              `json:"limit"`
}

// searchMessages is a case-insensitive substring search over the archive.
func (s *Server) searchMessages(in SearchMessagesInput) (SearchMessagesOutput, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return SearchMessagesOutput{}, errors.New("query is required and must not be blank")
	}

	records, err := s.archive.Index()
	if err != nil {
		return SearchMessagesOutput{}, err
	}

	sel, err := newSelector(in.Rule, in.Account, in.ThreadID, in.Since, in.Until)
	if err != nil {
		return SearchMessagesOutput{}, err
	}

	needle := strings.ToLower(query)
	matched := make([]*record, 0, 16)
	for _, r := range sel.apply(records) {
		if strings.Contains(strings.ToLower(r.searchText()), needle) {
			matched = append(matched, r)
		}
	}

	limit := clampLimit(in.Limit)
	page := matched
	if len(page) > limit {
		page = page[:limit]
	}

	return SearchMessagesOutput{
		Query:     query,
		Messages:  summaries(page, query),
		Count:     len(page),
		Matched:   len(matched),
		Truncated: len(matched) > limit,
		Limit:     limit,
	}, nil
}

// ------------------------------------------------------------------- selectors

// selector is the filter set list_messages and search_messages share.
type selector struct {
	rule     string
	account  string
	threadID string
	since    time.Time
	until    time.Time
}

// newSelector parses the filters, reporting a malformed date bound rather than
// silently ignoring it.
func newSelector(rule, account, threadID, since, until string) (*selector, error) {
	s := &selector{
		rule:     strings.TrimSpace(rule),
		account:  strings.TrimSpace(account),
		threadID: strings.TrimSpace(threadID),
	}
	var err error
	if s.since, err = parseBound(since, false); err != nil {
		return nil, fmt.Errorf("since: %w", err)
	}
	if s.until, err = parseBound(until, true); err != nil {
		return nil, fmt.Errorf("until: %w", err)
	}
	return s, nil
}

// apply keeps the records that pass every filter, preserving the index order
// (newest first).
func (s *selector) apply(records []*record) []*record {
	out := make([]*record, 0, len(records))
	for _, r := range records {
		switch {
		case s.rule != "" && !strings.EqualFold(r.rule, s.rule):
		case s.account != "" && !strings.EqualFold(r.account, s.account):
		case s.threadID != "" && r.threadID != s.threadID:
		case !s.since.IsZero() && r.date.Before(s.since):
		case !s.until.IsZero() && !r.date.Before(s.until):
		default:
			out = append(out, r)
		}
	}
	return out
}

// parseBound reads a date bound. RFC3339 is accepted with or without a zone,
// and so is a bare `YYYY-MM-DD`.
//
// A bare date used as an upper bound covers the whole day: `until: 2026-07-28`
// includes everything that arrived on the 28th, which is what an agent writing
// that string means. The returned bound is always exclusive for `until` and
// inclusive for `since`.
func parseBound(s string, endOfDay bool) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		if endOfDay {
			return t.UTC().AddDate(0, 0, 1), nil
		}
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("cannot read %s as a date; use RFC3339 (2026-07-28T09:00:00Z) or YYYY-MM-DD", echo(s))
}

// clampLimit applies the documented default and ceiling.
func clampLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultLimit
	case n > MaxLimit:
		return MaxLimit
	default:
		return n
	}
}

// clampBodyChars applies the documented default and ceiling.
func clampBodyChars(n int) int {
	switch {
	case n <= 0:
		return DefaultBodyChars
	case n > MaxBodyChars:
		return MaxBodyChars
	default:
		return n
	}
}

// ------------------------------------------------------------------ projection

// summaries projects records onto the wire type. A non-empty query adds the
// snippet around the first hit.
func summaries(records []*record, query string) []MessageSummary {
	out := make([]MessageSummary, 0, len(records))
	for _, r := range records {
		s := MessageSummary{
			ID:             r.id,
			Path:           r.path,
			Rule:           r.rule,
			Account:        r.account,
			From:           r.from,
			To:             r.to,
			Subject:        r.subject,
			Date:           r.date,
			MessageID:      r.messageID,
			ThreadID:       r.threadID,
			ThreadIDSource: r.threadIDSource,
			Labels:         r.labels,
			HasAttachment:  len(r.attachments) > 0,
			Formats:        formatsOf(r),
		}
		if query != "" {
			s.Snippet = snippet(r.searchText(), query)
		}
		out = append(out, s)
	}
	return out
}

// detailOf projects one record onto the full wire type, truncating the body to
// budget characters.
func detailOf(r *record, budget int) MessageDetail {
	body, truncated := truncate(r.body, budget)

	files := make([]FileRef, 0, len(r.files))
	for _, f := range r.files {
		files = append(files, FileRef{Format: string(f.format), Path: f.path, Bytes: f.size})
	}

	return MessageDetail{
		ID:             r.id,
		Path:           r.path,
		Rule:           r.rule,
		Account:        r.account,
		From:           r.from,
		To:             r.to,
		Cc:             r.cc,
		Subject:        r.subject,
		Date:           r.date,
		MessageID:      r.messageID,
		ThreadID:       r.threadID,
		ThreadIDSource: r.threadIDSource,
		InReplyTo:      r.inReplyTo,
		Labels:         r.labels,
		HasAttachment:  len(r.attachments) > 0,
		Attachments:    r.attachments,
		Files:          files,
		Body:           body,
		BodyFormat:     r.bodyFormat,
		BodyTruncated:  truncated,
	}
}

// formatsOf lists the renderings of a message that exist on disk.
func formatsOf(r *record) []string {
	out := make([]string, 0, len(r.files))
	for _, f := range r.files {
		out = append(out, string(f.format))
	}
	return out
}

// groupThreads folds a page of records into conversations, most recently
// active first, with each thread's messages oldest first — reading order.
func groupThreads(records []*record) []ThreadSummary {
	order := make([]string, 0, len(records))
	byThread := make(map[string][]*record, len(records))
	for _, r := range records {
		if _, ok := byThread[r.threadID]; !ok {
			order = append(order, r.threadID)
		}
		byThread[r.threadID] = append(byThread[r.threadID], r)
	}

	out := make([]ThreadSummary, 0, len(order))
	for _, id := range order {
		members := byThread[id]
		sortByDateAscending(members)

		t := ThreadSummary{
			ThreadID:       id,
			ThreadIDSource: weakestSource(members),
			Subject:        members[0].subject,
			Count:          len(members),
			FirstDate:      members[0].date,
			LastDate:       members[len(members)-1].date,
			Messages:       summaries(members, ""),
		}
		seen := make(map[string]bool, len(members))
		for _, m := range members {
			if m.from != "" && !seen[m.from] {
				seen[m.from] = true
				t.Participants = append(t.Participants, m.from)
			}
		}
		out = append(out, t)
	}
	return out
}

// weakestSource reports the least trustworthy thread-id source in a group. A
// thread is only as well-grouped as its worst-attested member, and reporting
// the best one would overstate the guarantee.
func weakestSource(records []*record) string {
	rank := map[string]int{"provider": 0, "references": 1, "in_reply_to": 2, "self": 3}
	worst := records[0].threadIDSource
	for _, r := range records[1:] {
		if rank[r.threadIDSource] > rank[worst] {
			worst = r.threadIDSource
		}
	}
	return worst
}

// sortByDateAscending puts the oldest message first, which is reading order
// for a conversation. Ties break on id so the order is total.
func sortByDateAscending(records []*record) {
	sort.SliceStable(records, func(i, j int) bool {
		a, b := records[i], records[j]
		if !a.date.Equal(b.date) {
			return a.date.Before(b.date)
		}
		return a.id < b.id
	})
}

// snippet returns the text around the first case-insensitive occurrence of
// query, with whitespace collapsed and ellipses where text was cut.
func snippet(haystack, query string) string {
	i := strings.Index(strings.ToLower(haystack), strings.ToLower(query))
	if i < 0 {
		return ""
	}

	start := i - snippetRadius
	if start < 0 {
		start = 0
	}
	end := i + len(query) + snippetRadius
	if end > len(haystack) {
		end = len(haystack)
	}

	// Do not split a rune in half; the result is JSON and must stay valid UTF-8.
	for start > 0 && !utf8Start(haystack[start]) {
		start--
	}
	for end < len(haystack) && !utf8Start(haystack[end]) {
		end++
	}

	var b strings.Builder
	if start > 0 {
		b.WriteString("…")
	}
	b.WriteString(strings.Join(strings.Fields(haystack[start:end]), " "))
	if end < len(haystack) {
		b.WriteString("…")
	}
	return b.String()
}

// utf8Start reports whether b begins a UTF-8 encoded rune.
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// truncate cuts s to at most n characters (not bytes), reporting whether it
// had to.
func truncate(s string, n int) (string, bool) {
	if n <= 0 {
		return "", s != ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s, false
	}
	return string(runes[:n]), true
}

// formatNames renders a rule's formats for the wire.
func formatNames(formats []config.Format) []string {
	out := make([]string, 0, len(formats))
	for _, f := range formats {
		out = append(out, string(f))
	}
	return out
}
