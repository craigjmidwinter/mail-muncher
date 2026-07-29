package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/craigmidwinter/mail-muncher/internal/config"
	"github.com/craigmidwinter/mail-muncher/internal/provider"
	"github.com/stretchr/testify/require"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
)

// ---------------------------------------------------------------------------
// canned Gmail API
// ---------------------------------------------------------------------------

// gmailStub is a canned Gmail API served over httptest: every response is
// configured up front, nothing touches the network, and every request is
// recorded so tests can assert on what the provider actually asked for.
type gmailStub struct {
	mu sync.Mutex

	profileHistoryID uint64
	labels           []*gmailapi.Label
	messages         map[string]*gmailapi.Message

	// listPages and historyPages are keyed by the incoming pageToken; "" is the
	// first page.
	listPages    map[string]*gmailapi.ListMessagesResponse
	historyPages map[string]*gmailapi.ListHistoryResponse

	// historyStatus, when non-zero, makes every history.list call fail with it.
	historyStatus int
	historyReason string
	// getFailures queues per-message statuses returned before the real message,
	// and getReasons overrides the `reason` those failures report.
	getFailures map[string][]int
	getReasons  map[string]string
	// listFailures queues statuses returned by messages.list before it starts
	// serving pages.
	listFailures []int
	// onGet runs (outside the lock) at the start of every messages.get.
	onGet func(id string)
	// afterList and afterHistory run once the corresponding list response has
	// been written, so a test can model the world changing mid-run: a message
	// arriving after the snapshot this run will act on, the clock moving on.
	afterList    func()
	afterHistory func()

	// mailbox, when non-empty, replaces the canned listPages: users.messages.list
	// evaluates the `q` it was actually sent against these entries. See
	// addMailboxMessage.
	mailbox []mailboxEntry

	// observed
	listQueries   []string
	listTokens    []string
	listSpamTrash []string
	historyStarts []uint64
	historyTokens []string
	historyTypes  []string
	getIDs        []string
	labelCalls    int
	profileCalls  int
}

func newGmailStub() *gmailStub {
	return &gmailStub{
		profileHistoryID: 900100,
		messages:         make(map[string]*gmailapi.Message),
		listPages:        make(map[string]*gmailapi.ListMessagesResponse),
		historyPages:     make(map[string]*gmailapi.ListHistoryResponse),
		getFailures:      make(map[string][]int),
		getReasons:       make(map[string]string),
	}
}

// addMessage registers a message the get endpoint will serve, encoding raw the
// way Gmail does (unpadded base64url).
//
// Every stub message gets a threadId, because the real users.messages.get
// returns one on the same response as `raw` — that is what makes carrying it
// free. threadOf names the value, so a test can assert on it.
func (s *gmailStub) addMessage(id string, internalDate time.Time, labelIDs []string, raw []byte) {
	s.messages[id] = &gmailapi.Message{
		Id:           id,
		ThreadId:     threadOf(id),
		InternalDate: internalDate.UnixMilli(),
		LabelIds:     labelIDs,
		Raw:          base64.RawURLEncoding.EncodeToString(raw),
	}
}

// threadOf is the stub's thread id for a message id.
func threadOf(id string) string { return "thread-" + id }

// addListPage registers one page of users.messages.list, served when the
// request carries pageToken == token, and also registers a stub message for
// each id so it can be downloaded.
func (s *gmailStub) addListPage(token, next string, ids ...string) {
	page := &gmailapi.ListMessagesResponse{NextPageToken: next}
	for _, id := range ids {
		page.Messages = append(page.Messages, &gmailapi.Message{Id: id})
		if _, ok := s.messages[id]; !ok {
			s.addMessage(id, time.UnixMilli(1700000000000), []string{"INBOX"}, []byte("Subject: "+id+"\r\n\r\nbody "+id+"\r\n"))
		}
	}
	s.listPages[token] = page
}

// addHistoryPage registers one page of users.history.list. Each id becomes a
// messagesAdded entry (and a downloadable stub message) on a record with the
// given history id.
func (s *gmailStub) addHistoryPage(token, next string, recordID, pageHistoryID uint64, ids ...string) {
	record := &gmailapi.History{Id: recordID}
	for _, id := range ids {
		record.MessagesAdded = append(record.MessagesAdded, &gmailapi.HistoryMessageAdded{
			Message: &gmailapi.Message{Id: id},
		})
		if _, ok := s.messages[id]; !ok {
			s.addMessage(id, time.UnixMilli(1700000000000), []string{"INBOX"}, []byte("Subject: "+id+"\r\n\r\nbody "+id+"\r\n"))
		}
	}
	s.historyPages[token] = &gmailapi.ListHistoryResponse{
		History:       []*gmailapi.History{record},
		HistoryId:     pageHistoryID,
		NextPageToken: next,
	}
}

// mailboxEntry is one message in the stub's evaluated mailbox.
type mailboxEntry struct {
	id           string
	received     time.Time
	spamOrTrash  bool
	matchesQuery bool
}

// addMailboxMessage puts a message in the evaluated mailbox and switches
// users.messages.list out of canned-page mode.
//
// In that mode the list handler parses the `q` the provider actually sent — the
// `after:` bound, and whether the account query was applied at all — and honours
// includeSpamTrash, then answers with the entries that genuinely match. Timeline
// tests can then assert that a message was *delivered*, which is the property
// that matters, instead of asserting on the spelling of a query string.
//
// It is safe to call while the stub is serving: tests use it from afterList to
// model a message arriving mid-run.
func (s *gmailStub) addMailboxMessage(id string, received time.Time, spamOrTrash, matchesQuery bool) {
	labels := []string{"INBOX"}
	if spamOrTrash {
		labels = []string{"SPAM"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mailbox = append(s.mailbox, mailboxEntry{
		id: id, received: received, spamOrTrash: spamOrTrash, matchesQuery: matchesQuery,
	})
	s.messages[id] = &gmailapi.Message{
		Id:           id,
		ThreadId:     threadOf(id),
		InternalDate: received.UnixMilli(),
		LabelIds:     labels,
		Raw:          base64.RawURLEncoding.EncodeToString([]byte("Subject: " + id + "\r\n\r\nbody " + id + "\r\n")),
	}
}

// mailboxPageLocked answers one users.messages.list request out of the evaluated
// mailbox. There is no pagination here; the point is which ids come back.
func (s *gmailStub) mailboxPageLocked(q url.Values) *gmailapi.ListMessagesResponse {
	query, after := parseStubQuery(q.Get("q"))
	includeSpamTrash := q.Get("includeSpamTrash") == "true"

	page := &gmailapi.ListMessagesResponse{}
	for _, e := range s.mailbox {
		if !after.IsZero() && e.received.Before(after) {
			continue
		}
		if query != "" && !e.matchesQuery {
			continue
		}
		if e.spamOrTrash && !includeSpamTrash {
			continue
		}
		page.Messages = append(page.Messages, &gmailapi.Message{Id: e.id})
	}
	return page
}

// parseStubQuery splits a scan query back into the account-query term (empty
// when none was applied) and the `after:` bound (zero when unbounded). It
// understands exactly the shapes scanQuery builds: "", "after:N", "q", and
// "(q) after:N".
func parseStubQuery(q string) (string, time.Time) {
	q = strings.TrimSpace(q)
	var after time.Time
	if i := strings.LastIndex(q, "after:"); i >= 0 {
		if secs, err := strconv.ParseInt(strings.TrimSpace(q[i+len("after:"):]), 10, 64); err == nil {
			after = time.Unix(secs, 0).UTC()
			q = strings.TrimSpace(q[:i])
		}
	}
	q = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(q, "("), ")"))
	return q, after
}

func (s *gmailStub) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /gmail/v1/users/{userId}/profile", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.profileCalls++
		historyID := s.profileHistoryID
		s.mu.Unlock()
		writeJSON(w, &gmailapi.Profile{EmailAddress: "user@example.com", HistoryId: historyID})
	})

	mux.HandleFunc("GET /gmail/v1/users/{userId}/labels", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.labelCalls++
		labels := s.labels
		s.mu.Unlock()
		writeJSON(w, &gmailapi.ListLabelsResponse{Labels: labels})
	})

	mux.HandleFunc("GET /gmail/v1/users/{userId}/messages", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		token := query.Get("pageToken")
		s.mu.Lock()
		if len(s.listFailures) > 0 {
			status := s.listFailures[0]
			s.listFailures = s.listFailures[1:]
			s.mu.Unlock()
			writeAPIError(w, status, reasonForStatus(status), "canned list failure")
			return
		}
		s.listQueries = append(s.listQueries, query.Get("q"))
		s.listTokens = append(s.listTokens, token)
		s.listSpamTrash = append(s.listSpamTrash, query.Get("includeSpamTrash"))
		page, ok := s.mailboxPageLocked(query), true
		if len(s.mailbox) == 0 {
			page, ok = s.listPages[token]
		}
		after := s.afterList
		s.mu.Unlock()
		// The hook runs once the response is on its way, so a test can model an
		// arrival this run's snapshot could not have contained.
		if after != nil {
			defer after()
		}
		if !ok {
			writeAPIError(w, http.StatusNotFound, "notFound", "no canned list page for token "+token)
			return
		}
		writeJSON(w, page)
	})

	mux.HandleFunc("GET /gmail/v1/users/{userId}/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if s.onGet != nil {
			s.onGet(id)
		}
		s.mu.Lock()
		s.getIDs = append(s.getIDs, id)
		if queue := s.getFailures[id]; len(queue) > 0 {
			status := queue[0]
			s.getFailures[id] = queue[1:]
			reason := s.getReasons[id]
			s.mu.Unlock()
			if reason == "" {
				reason = reasonForStatus(status)
			}
			writeAPIError(w, status, reason, "canned failure")
			return
		}
		msg, ok := s.messages[id]
		s.mu.Unlock()
		if !ok {
			writeAPIError(w, http.StatusNotFound, "notFound", "no canned message "+id)
			return
		}
		writeJSON(w, msg)
	})

	mux.HandleFunc("GET /gmail/v1/users/{userId}/history", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		start, _ := strconv.ParseUint(q.Get("startHistoryId"), 10, 64)
		token := q.Get("pageToken")
		s.mu.Lock()
		s.historyStarts = append(s.historyStarts, start)
		s.historyTokens = append(s.historyTokens, token)
		s.historyTypes = append(s.historyTypes, q.Get("historyTypes"))
		status, reason := s.historyStatus, s.historyReason
		page, ok := s.historyPages[token]
		after := s.afterHistory
		s.mu.Unlock()
		if after != nil {
			defer after()
		}

		if status != 0 {
			if reason == "" {
				reason = reasonForStatus(status)
			}
			writeAPIError(w, status, reason, "canned history failure")
			return
		}
		if !ok {
			writeAPIError(w, http.StatusNotFound, "notFound", "no canned history page for token "+token)
			return
		}
		writeJSON(w, page)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, http.StatusNotImplemented, "notImplemented", "unexpected request "+r.Method+" "+r.URL.Path)
	})
	return mux
}

func (s *gmailStub) snapshot(f func(*gmailStub)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic(err)
	}
}

func writeAPIError(w http.ResponseWriter, code int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":{"code":%d,"message":%q,"errors":[{"domain":"global","reason":%q,"message":%q}]}}`,
		code, message, reason, message)
}

func reasonForStatus(status int) string {
	switch status {
	case http.StatusTooManyRequests:
		return "rateLimitExceeded"
	case http.StatusForbidden:
		return "userRateLimitExceeded"
	case http.StatusNotFound:
		return "notFound"
	default:
		return "backendError"
	}
}

// newTestProvider wires a Provider to stub over httptest, with a backoff fast
// enough for a unit test.
func newTestProvider(t *testing.T, stub *gmailStub, opts FetchOptions) *Provider {
	t.Helper()
	return newTestProviderWithHandler(t, stub.handler(), opts)
}

// newTestProviderWithHandler is newTestProvider for tests that need to wrap or
// override individual routes.
func newTestProviderWithHandler(t *testing.T, h http.Handler, opts FetchOptions) *Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	if opts.Retry.BaseDelay == 0 {
		opts.Retry.BaseDelay = time.Millisecond
	}
	if opts.Retry.MaxDelay == 0 {
		opts.Retry.MaxDelay = 5 * time.Millisecond
	}
	p, err := NewWithHTTPClient(context.Background(), "work", srv.Client(), srv.URL, opts)
	require.NoError(t, err)
	return p
}

// fakeClock is a movable stand-in for time.Now, so a test can put real duration
// between the moment a run samples its watermarks and the moment it finishes.
// It is mutex-guarded because the stub's handlers move it from the server's
// goroutines while Fetch reads it from its own.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{t: t} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// collector accumulates delivered messages; it is the pipeline's fn stand-in.
type collector struct {
	msgs []provider.RawMessage
	err  error
	stop int
}

func (c *collector) fn(msg provider.RawMessage) error {
	c.msgs = append(c.msgs, msg)
	if c.err != nil && c.stop == len(c.msgs) {
		return c.err
	}
	return nil
}

func (c *collector) ids() []string {
	out := make([]string, 0, len(c.msgs))
	for _, m := range c.msgs {
		out = append(out, m.ID)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// constructors & plumbing
// ---------------------------------------------------------------------------

func TestProviderName(t *testing.T) {
	p := newTestProvider(t, newGmailStub(), FetchOptions{})
	require.Equal(t, "gmail", p.Name())
	require.Equal(t, config.ProviderGmail, p.Name())
	require.Equal(t, "work", p.Account())
	require.Implements(t, (*provider.Provider)(nil), p)
}

func TestFetchOptionsFromAccount(t *testing.T) {
	acct := &config.Account{
		Name:     "work",
		Provider: config.ProviderGmail,
		Gmail: &config.GmailConfig{
			CredentialsFile: "/creds.json",
			TokenFile:       "/token.json",
			Query:           "from:jobs@example.com",
			InitialLookback: "48h",
		},
	}
	opts := FetchOptionsFromAccount(acct)
	require.Equal(t, "from:jobs@example.com", opts.Query)
	require.Equal(t, 48*time.Hour, opts.InitialLookback)

	// Omitted initial_lookback falls back to the config default (30 days).
	acct.Gmail.InitialLookback = ""
	require.Equal(t, 720*time.Hour, FetchOptionsFromAccount(acct).InitialLookback)

	require.Equal(t, FetchOptions{}, FetchOptionsFromAccount(nil))
	require.Equal(t, FetchOptions{}, FetchOptionsFromAccount(&config.Account{Name: "x"}))
}

func TestFetchOptionsDefaults(t *testing.T) {
	got := FetchOptions{}.withDefaults()
	require.Equal(t, DefaultConcurrency, got.Concurrency)
	require.Equal(t, DefaultPageSize, got.PageSize)

	custom := FetchOptions{Concurrency: 2, PageSize: 10}.withDefaults()
	require.Equal(t, 2, custom.Concurrency)
	require.Equal(t, int64(10), custom.PageSize)
}

func TestNewWithHTTPClientRejectsNilClient(t *testing.T) {
	_, err := NewWithHTTPClient(context.Background(), "work", nil, "", FetchOptions{})
	require.ErrorContains(t, err, "nil HTTP client")
}

func TestNewFromAccountSurfacesMissingToken(t *testing.T) {
	// No network: the token file does not exist, so the error arrives before
	// any HTTP call and tells the user how to fix it.
	acct := &config.Account{
		Name:     "work",
		Provider: config.ProviderGmail,
		Gmail: &config.GmailConfig{
			CredentialsFile: "testdata/credentials.json",
			TokenFile:       t.TempDir() + "/token.json",
		},
	}
	_, err := NewFromAccount(context.Background(), acct)
	require.ErrorIs(t, err, ErrNoToken)
	require.Contains(t, err.Error(), "mail-muncher auth --account work")
}

func TestFetchRejectsNilCallback(t *testing.T) {
	p := newTestProvider(t, newGmailStub(), FetchOptions{})
	_, err := p.Fetch(context.Background(), provider.SyncState{}, nil)
	require.ErrorContains(t, err, "non-nil callback")
}

func TestFetchHonoursAlreadyCancelledContext(t *testing.T) {
	p := newTestProvider(t, newGmailStub(), FetchOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Fetch(ctx, provider.SyncState{}, func(provider.RawMessage) error { return nil })
	require.ErrorIs(t, err, context.Canceled)
}

// ---------------------------------------------------------------------------
// retry policy
// ---------------------------------------------------------------------------

func TestRetryableClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"429", &googleapi.Error{Code: 429}, true},
		{"500", &googleapi.Error{Code: 500}, true},
		{"503", &googleapi.Error{Code: 503}, true},
		{"408", &googleapi.Error{Code: 408}, true},
		{"403 rate limit", &googleapi.Error{Code: 403, Errors: []googleapi.ErrorItem{{Reason: "rateLimitExceeded"}}}, true},
		{"403 user rate limit", &googleapi.Error{Code: 403, Errors: []googleapi.ErrorItem{{Reason: "userRateLimitExceeded"}}}, true},
		{"403 rate limit in message", &googleapi.Error{Code: 403, Message: "Rate Limit Exceeded"}, true},
		{"403 daily quota", &googleapi.Error{Code: 403, Errors: []googleapi.ErrorItem{{Reason: "dailyLimitExceeded"}}}, false},
		{"403 forbidden", &googleapi.Error{Code: 403, Errors: []googleapi.ErrorItem{{Reason: "forbidden"}}}, false},
		{"401", &googleapi.Error{Code: 401}, false},
		{"404", &googleapi.Error{Code: 404}, false},
		{"400", &googleapi.Error{Code: 400}, false},
		{"decode failure", fmt.Errorf("decode base64url"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, retryable(tc.err))
			require.Equal(t, tc.want, retryable(fmt.Errorf("wrapped: %w", tc.err)))
		})
	}
}
