package imap_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/provider"
	mmimap "github.com/craigjmidwinter/mail-muncher/internal/provider/imap"
)

const testAccount = "personal"

// newProvider builds a Provider pointed at the test server, with a short
// backoff so a genuinely failing test fails fast instead of sleeping.
func newProvider(t *testing.T, srv *testServer, mutate ...func(*mmimap.Options)) *mmimap.Provider {
	t.Helper()
	opts := mmimap.Options{
		Host:            srv.Host,
		Port:            srv.Port,
		Username:        testUsername,
		PasswordCmd:     passwordCmd,
		Mailboxes:       []string{"INBOX"},
		TLS:             srv.TLSConfig != nil,
		TLSConfig:       srv.TLSConfig,
		InitialLookback: 30 * 24 * time.Hour,
		Retry:           provider.RetryConfig{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
	}
	for _, m := range mutate {
		m(&opts)
	}
	p, err := mmimap.New(testAccount, opts)
	require.NoError(t, err)
	return p
}

// fetchAll drives one Fetch and collects everything delivered.
func fetchAll(t *testing.T, p *mmimap.Provider, state provider.SyncState) ([]provider.RawMessage, provider.SyncState) {
	t.Helper()
	got, out, err := fetchCollect(context.Background(), p, state, nil)
	require.NoError(t, err)
	return got, out
}

// fetchCollect drives one Fetch, optionally letting the caller interfere with
// the delivery callback.
func fetchCollect(ctx context.Context, p *mmimap.Provider, state provider.SyncState,
	each func(provider.RawMessage) error) ([]provider.RawMessage, provider.SyncState, error) {

	var got []provider.RawMessage
	out, err := p.Fetch(ctx, state, func(msg provider.RawMessage) error {
		got = append(got, msg)
		if each != nil {
			return each(msg)
		}
		return nil
	})
	return got, out, err
}

func subjects(msgs []provider.RawMessage) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, subjectOf(m.Raw))
	}
	return out
}

func subjectOf(raw []byte) string {
	for _, line := range strings.Split(string(raw), "\r\n") {
		if s, ok := strings.CutPrefix(line, "Subject: "); ok {
			return s
		}
	}
	return ""
}

func TestProviderName(t *testing.T) {
	srv := newTestServer(t)
	p := newProvider(t, srv)
	assert.Equal(t, config.ProviderIMAP, p.Name())
	assert.Equal(t, testAccount, p.Account())
}

// A first-ever fetch has no cursor at all, so it resyncs from initial_lookback
// and must deliver exactly the window that bounds — not the whole mailbox.
func TestFetchInitialIsBoundedByLookback(t *testing.T) {
	srv := newTestServer(t)
	now := time.Now()
	srv.Append("INBOX", "ancient", now.Add(-400*24*time.Hour))
	srv.Append("INBOX", "recent one", now.Add(-48*time.Hour))
	srv.Append("INBOX", "recent two", now.Add(-1*time.Hour))

	p := newProvider(t, srv)
	got, state := fetchAll(t, p, provider.SyncState{})

	assert.Equal(t, []string{"recent one", "recent two"}, subjects(got))

	validity := srv.UIDValidity("INBOX")
	assert.Equal(t, fmt.Sprint(validity), state.GetExtra("imap.INBOX.uidvalidity"))
	// UID 3 is the newest message; the cursor must sit at it even though UIDs
	// 1..2 include one the lookback excluded.
	assert.Equal(t, "3", state.GetExtra("imap.INBOX.last_uid"))
	assert.False(t, state.LastSyncTime.IsZero(), "LastSyncTime must advance after a clean fetch")

	// Ids are <account>:<mailbox>:<uidvalidity>:<uid>, and nothing downstream
	// gets a thread id: model.Parse synthesizes one from the References chain.
	require.Len(t, got, 2)
	assert.Equal(t, fmt.Sprintf("%s:INBOX:%d:2", testAccount, validity), got[0].ID)
	assert.Equal(t, fmt.Sprintf("%s:INBOX:%d:3", testAccount, validity), got[1].ID)
	assert.Empty(t, got[0].ThreadID)
	assert.Equal(t, []string{"INBOX"}, got[0].Labels)
	assert.WithinDuration(t, now.Add(-48*time.Hour), got[0].InternalDate, time.Second)
	// Raw is the message the server holds, byte for byte.
	assert.Equal(t, rawMessage("recent one"), string(got[0].Raw))
}

// An initial_lookback of zero means "everything", which is the escape hatch for
// a first archive of an old mailbox.
func TestFetchInitialWithoutLookbackTakesEverything(t *testing.T) {
	srv := newTestServer(t)
	srv.Append("INBOX", "ancient", time.Now().Add(-400*24*time.Hour))
	srv.Append("INBOX", "recent", time.Now())

	p := newProvider(t, srv, func(o *mmimap.Options) { o.InitialLookback = 0 })
	got, _ := fetchAll(t, p, provider.SyncState{})
	assert.Equal(t, []string{"ancient", "recent"}, subjects(got))
}

// The steady state: a second cycle over an unchanged mailbox must ask for
// nothing, and a third must deliver only what arrived in between.
func TestFetchIncrementalByUID(t *testing.T) {
	srv := newTestServer(t)
	srv.Append("INBOX", "first", time.Now().Add(-2*time.Hour))
	srv.Append("INBOX", "second", time.Now().Add(-time.Hour))

	p := newProvider(t, srv)

	got, state := fetchAll(t, p, provider.SyncState{})
	require.Equal(t, []string{"first", "second"}, subjects(got))
	require.Equal(t, "2", state.GetExtra("imap.INBOX.last_uid"))

	// Nothing new. This is the case the `n:*` range gets wrong on its own:
	// `UID FETCH 3:*` against a mailbox topping out at UID 2 answers with
	// message 2, so a provider that trusted the range would re-deliver it
	// every single cycle.
	got, state = fetchAll(t, p, state)
	assert.Empty(t, got, "an unchanged mailbox must deliver nothing")
	assert.Equal(t, "2", state.GetExtra("imap.INBOX.last_uid"))

	srv.Append("INBOX", "third", time.Now())
	got, state = fetchAll(t, p, state)
	assert.Equal(t, []string{"third"}, subjects(got))
	assert.Equal(t, "3", state.GetExtra("imap.INBOX.last_uid"))
}

// The protocol's "everything you knew is invalid" signal. A stored cursor from
// a previous incarnation of the mailbox must be thrown away wholesale, not
// carried forward — carrying it forward is how mail goes missing.
func TestUIDValidityChangeForcesResync(t *testing.T) {
	srv := newTestServer(t)
	srv.Append("INBOX", "ancient", time.Now().Add(-400*24*time.Hour))
	srv.Append("INBOX", "one", time.Now().Add(-3*time.Hour))
	srv.Append("INBOX", "two", time.Now().Add(-2*time.Hour))
	srv.Append("INBOX", "three", time.Now().Add(-time.Hour))

	validity := srv.UIDValidity("INBOX")
	require.NotEqual(t, uint32(999), validity)

	// A cursor from the mailbox's previous life: a different UIDVALIDITY, and a
	// last_uid past everything that now exists.
	stale := provider.SyncState{Extra: map[string]string{
		"imap.INBOX.uidvalidity": "999",
		"imap.INBOX.last_uid":    "1000",
	}}

	p := newProvider(t, srv)
	got, state := fetchAll(t, p, stale)

	// Resynced from initial_lookback rather than resumed from UID 1001 — which
	// would have delivered nothing at all and silently lost three messages.
	assert.Equal(t, []string{"one", "two", "three"}, subjects(got))
	assert.Equal(t, fmt.Sprint(validity), state.GetExtra("imap.INBOX.uidvalidity"))
	assert.Equal(t, "4", state.GetExtra("imap.INBOX.last_uid"))

	// And the resync is bounded by initial_lookback, not a full mailbox dump.
	assert.NotContains(t, subjects(got), "ancient")

	// The new ids carry the new UIDVALIDITY, so nothing collides with whatever
	// the old incarnation of UID 2 was archived as.
	require.NotEmpty(t, got)
	assert.Equal(t, fmt.Sprintf("%s:INBOX:%d:2", testAccount, validity), got[0].ID)
}

// A cursor whose stored UIDVALIDITY matches but whose last_uid is unreadable
// must re-read rather than guess.
func TestUnparseableLastUIDResyncsRatherThanSkips(t *testing.T) {
	srv := newTestServer(t)
	srv.Append("INBOX", "one", time.Now())

	p := newProvider(t, srv)
	_, state := fetchAll(t, p, provider.SyncState{})

	state.SetExtra("imap.INBOX.last_uid", "not-a-number")
	got, _ := fetchAll(t, p, state)
	assert.Equal(t, []string{"one"}, subjects(got), "a corrupt cursor must cost a re-read, never a skipped message")
}

// Each mailbox carries its own independent cursor, and its name is the label
// the filter engine matches on.
func TestFetchMultipleMailboxes(t *testing.T) {
	srv := newTestServer(t)
	srv.CreateMailbox("Archive")
	srv.Append("INBOX", "inbox one", time.Now().Add(-2*time.Hour))
	srv.Append("Archive", "archive one", time.Now().Add(-2*time.Hour))
	srv.Append("Archive", "archive two", time.Now().Add(-time.Hour))

	p := newProvider(t, srv, func(o *mmimap.Options) { o.Mailboxes = []string{"INBOX", "Archive"} })

	got, state := fetchAll(t, p, provider.SyncState{})
	assert.Equal(t, []string{"inbox one", "archive one", "archive two"}, subjects(got))

	byLabel := map[string][]string{}
	for _, m := range got {
		require.Len(t, m.Labels, 1)
		byLabel[m.Labels[0]] = append(byLabel[m.Labels[0]], subjectOf(m.Raw))
	}
	assert.Equal(t, []string{"inbox one"}, byLabel["INBOX"])
	assert.Equal(t, []string{"archive one", "archive two"}, byLabel["Archive"])

	assert.Equal(t, "1", state.GetExtra("imap.INBOX.last_uid"))
	assert.Equal(t, "2", state.GetExtra("imap.Archive.last_uid"))
	assert.NotEqual(t, state.GetExtra("imap.INBOX.uidvalidity"), state.GetExtra("imap.Archive.uidvalidity"),
		"two mailboxes must not share a UIDVALIDITY key")

	// New mail in one folder must not disturb the other's cursor.
	srv.Append("INBOX", "inbox two", time.Now())
	got, state = fetchAll(t, p, state)
	assert.Equal(t, []string{"inbox two"}, subjects(got))
	assert.Equal(t, "2", state.GetExtra("imap.INBOX.last_uid"))
	assert.Equal(t, "2", state.GetExtra("imap.Archive.last_uid"))
}

// A mailbox the server does not have is an error, not a silent skip: a typo in
// `mailboxes:` must not look like an empty folder.
func TestFetchUnknownMailboxIsAnError(t *testing.T) {
	srv := newTestServer(t)
	p := newProvider(t, srv, func(o *mmimap.Options) { o.Mailboxes = []string{"Nope"} })

	_, _, err := fetchCollect(context.Background(), p, provider.SyncState{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Nope")
}

// THE READ-ONLY GUARANTEE.
//
// mail-muncher reads mailboxes it does not own, often ones a human also reads.
// Marking their mail \Seen would be a visible, irreversible change to someone
// else's inbox, and there is no server-side setting standing behind us: whether
// it happens is decided entirely by whether the fetch says BODY.PEEK[] or
// BODY[]. This test asserts the outcome on the server, and then proves the
// assertion is not vacuous by making the same server mark mail seen.
func TestFetchNeverMarksMessagesSeen(t *testing.T) {
	srv := newTestServer(t)
	srv.Append("INBOX", "one", time.Now().Add(-2*time.Hour))
	srv.Append("INBOX", "two", time.Now().Add(-time.Hour))
	require.Equal(t, uint32(2), srv.Unseen("INBOX"), "messages start unseen")

	p := newProvider(t, srv)
	got, state := fetchAll(t, p, provider.SyncState{})
	require.Len(t, got, 2)

	assert.Equal(t, uint32(2), srv.Unseen("INBOX"),
		"fetching bodies must not set \\Seen: use BODY.PEEK[], never BODY[]")

	// A second, incremental cycle exercises the other code path into the same
	// FETCH, so neither route can regress independently.
	srv.Append("INBOX", "three", time.Now())
	_, _ = fetchAll(t, p, state)
	assert.Equal(t, uint32(3), srv.Unseen("INBOX"), "an incremental fetch must not set \\Seen either")

	// Control: the same server, the same mailbox, a bare BODY[]. If this did
	// not flip the count, the assertions above would pass no matter what the
	// provider sent.
	markSeenWithBareFetch(t, srv)
	assert.Equal(t, uint32(0), srv.Unseen("INBOX"),
		"control failed: this server does not implement \\Seen-on-BODY[], so the PEEK assertions above prove nothing")
}

// markSeenWithBareFetch is the negative control for the test above: a normal
// client issuing the non-PEEK fetch the provider must never issue.
func markSeenWithBareFetch(t *testing.T, srv *testServer) {
	t.Helper()
	c, err := imapclient.DialInsecure(srv.Addr, nil)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	require.NoError(t, c.Login(testUsername, testPassword).Wait())
	_, err = c.Select("INBOX", nil).Wait()
	require.NoError(t, err)

	cmd := c.Fetch(goimap.UIDSet{{Start: 1, Stop: 0}}, &goimap.FetchOptions{
		UID:         true,
		BodySection: []*goimap.FetchItemBodySection{{Peek: false}},
	})
	_, err = cmd.Collect()
	require.NoError(t, err)
}

// Cancelling the context is the hard stop: it must be reported as such rather
// than as a mysterious connection error, and it must not be retried.
func TestFetchRespectsContextCancellation(t *testing.T) {
	srv := newTestServer(t)
	srv.Append("INBOX", "one", time.Now().Add(-2*time.Hour))
	srv.Append("INBOX", "two", time.Now().Add(-time.Hour))

	p := newProvider(t, srv)

	t.Run("cancelled before the fetch starts", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got, _, err := fetchCollect(ctx, p, provider.SyncState{}, nil)
		assert.Empty(t, got)
		assert.True(t, errIsAny(err, context.Canceled), "want context.Canceled, got %v", err)
	})

	t.Run("cancelled mid-delivery", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		got, state, err := fetchCollect(ctx, p, provider.SyncState{}, func(provider.RawMessage) error {
			cancel()
			return nil
		})
		assert.True(t, errIsAny(err, context.Canceled), "want context.Canceled, got %v", err)
		assert.Len(t, got, 1, "the message in flight is delivered; the next one is not")
		// A hard cancellation abandons work mid-flight, so the pipeline throws
		// this state away — but what comes back must still describe only what
		// actually got through.
		assert.Equal(t, "1", state.GetExtra("imap.INBOX.last_uid"))
		assert.True(t, state.LastSyncTime.IsZero(), "an aborted fetch must not stamp a completed sync")
	})
}

// The pipeline stops a graceful shutdown by returning a sentinel from the
// callback. It must come back unwrapped enough for errors.Is, and the state
// must describe exactly what was accepted — that pairing is what lets a
// signalled daemon save its progress.
func TestFetchCallbackErrorReturnsSentinelAndPartialState(t *testing.T) {
	srv := newTestServer(t)
	srv.Append("INBOX", "one", time.Now().Add(-3*time.Hour))
	srv.Append("INBOX", "two", time.Now().Add(-2*time.Hour))
	srv.Append("INBOX", "three", time.Now().Add(-time.Hour))

	stop := errors.New("stop requested")
	p := newProvider(t, srv)

	seen := 0
	got, state, err := fetchCollect(context.Background(), p, provider.SyncState{}, func(provider.RawMessage) error {
		seen++
		if seen == 2 {
			return stop
		}
		return nil
	})

	require.ErrorIs(t, err, stop, "a callback error must reach the pipeline intact")
	assert.Len(t, got, 2)
	assert.Equal(t, "2", state.GetExtra("imap.INBOX.last_uid"),
		"the cursor must sit at the last message the callback accepted")
	assert.Equal(t, fmt.Sprint(srv.UIDValidity("INBOX")), state.GetExtra("imap.INBOX.uidvalidity"))
	assert.True(t, state.LastSyncTime.IsZero())

	// Resuming from that state picks up exactly where it stopped.
	got, _ = fetchAll(t, p, state)
	assert.Equal(t, []string{"three"}, subjects(got))
}

// Ids already in the seen set are not re-delivered, and the cursor still moves
// past them.
func TestFetchSkipsAlreadySeenIDs(t *testing.T) {
	srv := newTestServer(t)
	srv.Append("INBOX", "one", time.Now().Add(-2*time.Hour))
	srv.Append("INBOX", "two", time.Now().Add(-time.Hour))

	validity := srv.UIDValidity("INBOX")
	state := provider.SyncState{SeenIDs: []string{fmt.Sprintf("%s:INBOX:%d:1", testAccount, validity)}}

	p := newProvider(t, srv)
	got, state := fetchAll(t, p, state)
	assert.Equal(t, []string{"two"}, subjects(got))
	assert.Equal(t, "2", state.GetExtra("imap.INBOX.last_uid"))
}

// An empty mailbox costs nothing and, crucially, does not write a half cursor
// that the next run would read as "incremental from UID 0" — that would be a
// full mailbox re-read ignoring initial_lookback.
func TestFetchEmptyMailbox(t *testing.T) {
	srv := newTestServer(t)
	p := newProvider(t, srv)

	got, state := fetchAll(t, p, provider.SyncState{})
	assert.Empty(t, got)
	assert.Empty(t, state.GetExtra("imap.INBOX.last_uid"))
	assert.Empty(t, state.GetExtra("imap.INBOX.uidvalidity"))
	assert.False(t, state.LastSyncTime.IsZero())

	srv.Append("INBOX", "first ever", time.Now())
	got, state = fetchAll(t, p, state)
	assert.Equal(t, []string{"first ever"}, subjects(got))
	assert.Equal(t, "1", state.GetExtra("imap.INBOX.last_uid"))
}

// State the provider does not own is carried through untouched.
func TestFetchPreservesForeignState(t *testing.T) {
	srv := newTestServer(t)
	srv.Append("INBOX", "one", time.Now())

	in := provider.SyncState{
		HistoryID: 4242,
		Extra:     map[string]string{"someone.elses": "value"},
		SeenIDs:   []string{"gmail:abc"},
	}
	p := newProvider(t, srv)
	_, out := fetchAll(t, p, in)

	assert.Equal(t, uint64(4242), out.HistoryID)
	assert.Equal(t, "value", out.GetExtra("someone.elses"))
	assert.Equal(t, []string{"gmail:abc"}, out.SeenIDs)
	// And the caller's state was not mutated in place.
	assert.Empty(t, in.GetExtra("imap.INBOX.last_uid"))
}

// The default configuration is `tls: true`, so it gets a test rather than an
// assumption.
func TestFetchOverTLS(t *testing.T) {
	srv := newTLSTestServer(t)
	srv.Append("INBOX", "encrypted", time.Now())

	p := newProvider(t, srv)
	got, state := fetchAll(t, p, provider.SyncState{})
	assert.Equal(t, []string{"encrypted"}, subjects(got))
	assert.Equal(t, "1", state.GetExtra("imap.INBOX.last_uid"))
}

// A TLS server the client cannot verify must fail, and must fail once: a
// certificate does not get better on the second attempt.
func TestFetchRefusesUntrustedTLS(t *testing.T) {
	srv := newTLSTestServer(t)
	p := newProvider(t, srv, func(o *mmimap.Options) { o.TLSConfig = nil })

	started := time.Now()
	_, _, err := fetchCollect(context.Background(), p, provider.SyncState{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TLS handshake")
	assert.Less(t, time.Since(started), 5*time.Second, "a certificate failure must not be retried with backoff")
}

// A wrong password is the server declining on purpose. It must be reported in
// terms the user can act on, and it must not be retried.
func TestFetchLoginFailure(t *testing.T) {
	srv := newTestServer(t)
	p := newProvider(t, srv, func(o *mmimap.Options) { o.PasswordCmd = "printf 'wrong'" })

	_, _, err := fetchCollect(context.Background(), p, provider.SyncState{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "login as")
	assert.Contains(t, err.Error(), "password_cmd")
	assert.NotContains(t, err.Error(), "wrong", "the password must never appear in an error")
}

func TestFetchRequiresCallback(t *testing.T) {
	srv := newTestServer(t)
	p := newProvider(t, srv)
	_, err := p.Fetch(context.Background(), provider.SyncState{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "callback")
}

func TestNewValidatesOptions(t *testing.T) {
	_, err := mmimap.New("", mmimap.Options{Host: "h", Username: "u"})
	assert.ErrorContains(t, err, "account")

	_, err = mmimap.New("a", mmimap.Options{Username: "u"})
	assert.ErrorContains(t, err, "host")

	_, err = mmimap.New("a", mmimap.Options{Host: "h"})
	assert.ErrorContains(t, err, "username")
}

func TestNewFromAccount(t *testing.T) {
	tls := false
	account := &config.Account{
		Name:     "work",
		Provider: config.ProviderIMAP,
		IMAP: &config.IMAPConfig{
			Host:            "imap.example.test",
			Username:        "someone@example.test",
			PasswordCmd:     "true",
			TLS:             &tls,
			InitialLookback: "48h",
		},
	}
	p, err := mmimap.NewFromAccount(context.Background(), account)
	require.NoError(t, err)
	assert.Equal(t, "work", p.Account())
	assert.Equal(t, config.ProviderIMAP, p.Name())

	opts := mmimap.OptionsFromAccount(account)
	assert.Equal(t, config.DefaultIMAPPort, opts.Port)
	assert.Equal(t, []string{config.DefaultIMAPMailbox}, opts.Mailboxes)
	assert.False(t, opts.TLS)
	assert.Equal(t, 48*time.Hour, opts.InitialLookback)

	_, err = mmimap.NewFromAccount(context.Background(), nil)
	assert.ErrorContains(t, err, "nil account")

	_, err = mmimap.NewFromAccount(context.Background(), &config.Account{Name: "x", Provider: config.ProviderIMAP})
	assert.ErrorContains(t, err, "imap:")
}

func TestMessageIDAndExtraKey(t *testing.T) {
	assert.Equal(t, "personal:INBOX:1650000000:48213",
		mmimap.MessageID("personal", "INBOX", 1650000000, 48213))
	assert.Equal(t, "imap.INBOX.uidvalidity", mmimap.ExtraKey("INBOX", "uidvalidity"))
	assert.Equal(t, "imap.Archive/2024.last_uid", mmimap.ExtraKey("Archive/2024", "last_uid"))
}

func TestExtraKeysAreStable(t *testing.T) {
	// The key layout is written into state files on disk, so a rename here
	// would silently resync every installed mailbox.
	assert.True(t, strings.HasPrefix(mmimap.ExtraKey("INBOX", "last_uid"), mmimap.ExtraPrefix))
}
