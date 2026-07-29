package gmail

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/craigjmidwinter/mail-muncher/internal/provider"
	"github.com/stretchr/testify/require"
)

var fixedNow = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

func at(t time.Time) func() time.Time { return func() time.Time { return t } }

// ---------------------------------------------------------------------------
// pagination
// ---------------------------------------------------------------------------

func TestFullScanPaginatesAcrossPages(t *testing.T) {
	stub := newGmailStub()
	stub.profileHistoryID = 5150
	stub.addListPage("", "page-2", "m1", "m2")
	stub.addListPage("page-2", "page-3", "m3", "m4")
	stub.addListPage("page-3", "", "m5")

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	state, err := p.Fetch(context.Background(), provider.SyncState{}, got.fn)
	require.NoError(t, err)

	require.Equal(t, []string{"m1", "m2", "m3", "m4", "m5"}, got.ids())
	stub.snapshot(func(s *gmailStub) {
		require.Equal(t, []string{"", "page-2", "page-3"}, s.listTokens, "each nextPageToken must be followed")
		require.Len(t, s.getIDs, 5)
	})

	require.Equal(t, uint64(5150), state.HistoryID, "history cursor comes from the profile after a full scan")
	require.Equal(t, fixedNow, state.LastSyncTime)
	require.ElementsMatch(t, []string{"m1", "m2", "m3", "m4", "m5"}, state.SeenIDs)
}

func TestFullScanDedupesIDsRepeatedAcrossPages(t *testing.T) {
	stub := newGmailStub()
	stub.addListPage("", "page-2", "m1", "m2")
	stub.addListPage("page-2", "", "m2", "m3")

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	_, err := p.Fetch(context.Background(), provider.SyncState{}, got.fn)
	require.NoError(t, err)
	require.Equal(t, []string{"m1", "m2", "m3"}, got.ids())
}

func TestFullScanSkipsSeenIDs(t *testing.T) {
	stub := newGmailStub()
	stub.addListPage("", "", "m1", "m2", "m3")

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	state := provider.SyncState{SeenIDs: []string{"m2"}}
	out, err := p.Fetch(context.Background(), state, got.fn)
	require.NoError(t, err)

	require.Equal(t, []string{"m1", "m3"}, got.ids())
	stub.snapshot(func(s *gmailStub) {
		require.NotContains(t, s.getIDs, "m2", "an already-seen id must not be downloaded again")
	})
	require.ElementsMatch(t, []string{"m1", "m2", "m3"}, out.SeenIDs)
}

// ---------------------------------------------------------------------------
// RAW download / base64url
// ---------------------------------------------------------------------------

func TestDecodeRawMatchesFixtureBytes(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "message-raw.eml"))
	require.NoError(t, err)
	encoded, err := os.ReadFile(filepath.Join("testdata", "message-raw.b64"))
	require.NoError(t, err)
	b64 := strings.TrimSpace(string(encoded))

	// The fixture is chosen so the encoding uses both base64url-only
	// characters; a standard-alphabet decoder would not round-trip it.
	require.Contains(t, b64, "-")
	require.Contains(t, b64, "_")

	got, err := decodeRaw(b64)
	require.NoError(t, err)
	require.Equal(t, want, got, "decoded bytes must be byte-identical to the fixture")
}

func TestDecodeRawTolerantForms(t *testing.T) {
	want := []byte{0xfb, 0xf0, 0x00, 0xfb, 0xff, 0xbf}

	t.Run("unpadded url", func(t *testing.T) {
		got, err := decodeRaw(base64.RawURLEncoding.EncodeToString(want))
		require.NoError(t, err)
		require.Equal(t, want, got)
	})
	t.Run("padded url", func(t *testing.T) {
		got, err := decodeRaw(base64.URLEncoding.EncodeToString(want))
		require.NoError(t, err)
		require.Equal(t, want, got)
	})
	t.Run("wrapped in newlines", func(t *testing.T) {
		enc := base64.RawURLEncoding.EncodeToString(want)
		got, err := decodeRaw(enc[:4] + "\r\n" + enc[4:])
		require.NoError(t, err)
		require.Equal(t, want, got)
	})
	t.Run("standard alphabet", func(t *testing.T) {
		got, err := decodeRaw(base64.StdEncoding.EncodeToString(want))
		require.NoError(t, err)
		require.Equal(t, want, got)
	})
	t.Run("empty", func(t *testing.T) {
		_, err := decodeRaw("")
		require.ErrorContains(t, err, "no `raw` field")
	})
	t.Run("not base64", func(t *testing.T) {
		_, err := decodeRaw("!!!!not base64!!!!")
		require.ErrorContains(t, err, "decode base64url")
	})
}

func TestFetchDeliversRawBytesInternalDateAndLabelNames(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "message-raw.eml"))
	require.NoError(t, err)

	internal := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	stub := newGmailStub()
	stub.labels = labelSet(map[string]string{
		"INBOX":     "INBOX",
		"UNREAD":    "UNREAD",
		"Label_912": "Job search/Recruiters",
	})
	stub.addMessage("m1", internal, []string{"INBOX", "Label_912", "UNREAD", "Label_unknown"}, raw)
	stub.addListPage("", "", "m1")

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	_, err = p.Fetch(context.Background(), provider.SyncState{}, got.fn)
	require.NoError(t, err)

	require.Len(t, got.msgs, 1)
	msg := got.msgs[0]
	require.Equal(t, "m1", msg.ID)
	require.Equal(t, raw, msg.Raw)
	require.True(t, msg.InternalDate.Equal(internal), "got %s", msg.InternalDate)
	require.Equal(t, time.UTC, msg.InternalDate.Location())
	require.Equal(t, threadOf("m1"), msg.ThreadID, "threadId rides along on the format=RAW response")
	require.Equal(t,
		[]string{"INBOX", "Job search/Recruiters", "UNREAD", "Label_unknown"},
		msg.Labels,
		"label ids resolve to names; unknown ids pass through")

	// One get per message, and no thread lookup of any kind: the thread id was
	// already in the response we had to make anyway.
	require.Equal(t, []string{"m1"}, stub.getIDs)
}

// TestFetchCarriesThreadIDOnBothRoutes: full scan and incremental history both
// funnel through the same format=RAW download, so neither can lose the thread
// id and neither pays an extra request for it.
func TestFetchCarriesThreadIDOnBothRoutes(t *testing.T) {
	t.Run("full scan", func(t *testing.T) {
		stub := newGmailStub()
		stub.addListPage("", "", "m1", "m2")

		p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
		var got collector
		_, err := p.Fetch(context.Background(), provider.SyncState{}, got.fn)
		require.NoError(t, err)

		require.Len(t, got.msgs, 2)
		for _, msg := range got.msgs {
			require.Equal(t, threadOf(msg.ID), msg.ThreadID)
		}
	})

	t.Run("history", func(t *testing.T) {
		stub := newGmailStub()
		stub.addHistoryPage("", "", 1001, 1001, "m1", "m2")

		p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
		var got collector
		_, err := p.Fetch(context.Background(), provider.SyncState{HistoryID: 900}, got.fn)
		require.NoError(t, err)

		require.Len(t, got.msgs, 2)
		for _, msg := range got.msgs {
			require.Equal(t, threadOf(msg.ID), msg.ThreadID)
		}
	})
}

// TestFetchLeavesThreadIDEmptyWhenGmailSendsNone: nothing invents a value here.
// model.Parse is where the header-derived fallback lives, so the provider seam
// reports exactly what the provider said.
func TestFetchLeavesThreadIDEmptyWhenGmailSendsNone(t *testing.T) {
	stub := newGmailStub()
	stub.addListPage("", "", "m1")
	stub.messages["m1"].ThreadId = "  "

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	_, err := p.Fetch(context.Background(), provider.SyncState{}, got.fn)
	require.NoError(t, err)

	require.Len(t, got.msgs, 1)
	require.Empty(t, got.msgs[0].ThreadID)
}

func TestFetchFetchesLabelsOncePerRun(t *testing.T) {
	stub := newGmailStub()
	stub.labels = labelSet(map[string]string{"INBOX": "INBOX"})
	stub.addListPage("", "", "m1", "m2", "m3", "m4", "m5", "m6")

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	_, err := p.Fetch(context.Background(), provider.SyncState{}, got.fn)
	require.NoError(t, err)
	require.Len(t, got.msgs, 6)
	stub.snapshot(func(s *gmailStub) {
		require.Equal(t, 1, s.labelCalls, "users.labels.list is fetched once and cached")
	})
}

// ---------------------------------------------------------------------------
// backoff
// ---------------------------------------------------------------------------

func TestGetRetriesAfter429ThenSucceeds(t *testing.T) {
	stub := newGmailStub()
	stub.addListPage("", "", "m1", "m2")
	stub.getFailures["m1"] = []int{http.StatusTooManyRequests}

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	state, err := p.Fetch(context.Background(), provider.SyncState{}, got.fn)
	require.NoError(t, err, "a 429 must be retried, not fatal")

	require.Equal(t, []string{"m1", "m2"}, got.ids())
	stub.snapshot(func(s *gmailStub) {
		require.Equal(t, 2, countOf(s.getIDs, "m1"), "m1 is attempted twice: the 429 then the success")
	})
	require.ElementsMatch(t, []string{"m1", "m2"}, state.SeenIDs)
}

func TestGetRetries403RateLimitAndGivesUpEventually(t *testing.T) {
	stub := newGmailStub()
	stub.addListPage("", "", "m1")
	// More failures than attempts, so the run fails after exhausting them.
	stub.getFailures["m1"] = []int{403, 403, 403, 403, 403, 403, 403}

	p := newTestProvider(t, stub, FetchOptions{
		Now:   at(fixedNow),
		Retry: provider.RetryConfig{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond},
	})
	var got collector
	_, err := p.Fetch(context.Background(), provider.SyncState{}, got.fn)
	require.Error(t, err)
	require.Contains(t, err.Error(), "giving up after 3 attempt(s)")
	require.Contains(t, err.Error(), "get message m1")
	stub.snapshot(func(s *gmailStub) {
		require.Equal(t, 3, countOf(s.getIDs, "m1"))
	})
}

func TestGetDoesNotRetryNonRateLimit403(t *testing.T) {
	stub := newGmailStub()
	stub.addListPage("", "", "m1")
	stub.getFailures["m1"] = []int{403, 403, 403}
	stub.getReasons["m1"] = "forbidden"

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	_, err := p.Fetch(context.Background(), provider.SyncState{}, got.fn)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "giving up", "a plain 403 is a hard failure, not a rate limit")
	stub.snapshot(func(s *gmailStub) {
		require.Equal(t, 1, countOf(s.getIDs, "m1"), "a non-rate-limit 403 is attempted once")
	})
}

func TestListRetriesAfterTransientFailures(t *testing.T) {
	stub := newGmailStub()
	stub.addListPage("", "", "m1")
	stub.listFailures = []int{http.StatusInternalServerError, http.StatusTooManyRequests}

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	_, err := p.Fetch(context.Background(), provider.SyncState{}, got.fn)
	require.NoError(t, err, "5xx and 429 on messages.list must be retried")
	require.Equal(t, []string{"m1"}, got.ids())
}

func TestListGivesUpWithAClearError(t *testing.T) {
	stub := newGmailStub()
	stub.addListPage("", "", "m1")
	stub.listFailures = []int{500, 500, 500, 500, 500, 500}

	p := newTestProvider(t, stub, FetchOptions{
		Now:   at(fixedNow),
		Retry: provider.RetryConfig{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond},
	})
	var got collector
	_, err := p.Fetch(context.Background(), provider.SyncState{}, got.fn)
	require.Error(t, err)
	require.Contains(t, err.Error(), "list messages for account \"work\"")
	require.Contains(t, err.Error(), "giving up after 2 attempt(s)")
}

// ---------------------------------------------------------------------------
// cancellation
// ---------------------------------------------------------------------------

func TestFetchCancellationStopsWorkerPool(t *testing.T) {
	const (
		total       = 60
		concurrency = 3
	)

	stub := newGmailStub()
	ids := make([]string, 0, total)
	for i := 0; i < total; i++ {
		ids = append(ids, fmt.Sprintf("m%02d", i))
	}
	stub.addListPage("", "", ids...)

	// Every download parks until the test releases it, so the pool is
	// observably saturated at exactly Concurrency in-flight requests.
	started := make(chan string, total)
	release := make(chan struct{})
	stub.onGet = func(id string) {
		started <- id
		<-release
	}

	p := newTestProvider(t, stub, FetchOptions{Concurrency: concurrency, Now: at(fixedNow)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got collector
	done := make(chan error, 1)
	go func() {
		_, err := p.Fetch(ctx, provider.SyncState{}, got.fn)
		done <- err
	}()

	for i := 0; i < concurrency; i++ {
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			t.Fatal("the worker pool never reached full concurrency")
		}
	}
	require.Empty(t, started,
		"the pool must not run more than Concurrency downloads at once (all %d workers are parked)", concurrency)

	cancel()
	close(release)

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("Fetch did not return after its context was cancelled")
	}

	attempted := concurrency + len(started)
	require.Less(t, attempted, total/2,
		"cancellation must stop the pool dispatching the remaining ids, but %d/%d were attempted", attempted, total)
	require.Less(t, len(got.msgs), total)
}

func TestFetchStopsWhenCallbackFails(t *testing.T) {
	stub := newGmailStub()
	stub.addListPage("", "", "m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8")

	p := newTestProvider(t, stub, FetchOptions{Concurrency: 1, Now: at(fixedNow)})
	got := collector{err: fmt.Errorf("sink is full"), stop: 2}
	state, err := p.Fetch(context.Background(), provider.SyncState{}, got.fn)
	require.ErrorContains(t, err, "sink is full")

	require.Len(t, got.msgs, 2)
	require.Len(t, state.SeenIDs, 1, "only the message the sink accepted is marked seen")
	require.True(t, state.LastSyncTime.IsZero(), "a failed run does not advance the cursor")
}

// ---------------------------------------------------------------------------
// query construction / initial_lookback
// ---------------------------------------------------------------------------

func TestScanQuery(t *testing.T) {
	lookback := 720 * time.Hour
	lastSync := fixedNow.Add(-3 * time.Hour)

	tests := []struct {
		name      string
		opts      FetchOptions
		state     provider.SyncState
		firstEver bool
		want      string
	}{
		{
			name:      "first run with lookback",
			opts:      FetchOptions{InitialLookback: lookback},
			firstEver: true,
			want:      fmt.Sprintf("after:%d", fixedNow.Add(-lookback).Unix()),
		},
		{
			name:      "first run with lookback and query",
			opts:      FetchOptions{InitialLookback: lookback, Query: "from:a OR from:b"},
			firstEver: true,
			want:      fmt.Sprintf("(from:a OR from:b) after:%d", fixedNow.Add(-lookback).Unix()),
		},
		{
			// A recovery scan stands in for the incremental path, which Gmail
			// does not query-filter, so the account query is dropped and the
			// bound is backed off by RecoveryOverlap.
			name:  "recovery scan drops the query and overlaps the watermark",
			opts:  FetchOptions{InitialLookback: lookback, Query: "from:a"},
			state: provider.SyncState{LastSyncTime: lastSync},
			want:  fmt.Sprintf("after:%d", lastSync.Add(-RecoveryOverlap).Unix()),
		},
		{
			name:      "no lookback, no state, no query",
			opts:      FetchOptions{},
			firstEver: true,
			want:      "",
		},
		{
			name:      "no lookback, no state, query only",
			opts:      FetchOptions{Query: "label:inbox"},
			firstEver: true,
			want:      "label:inbox",
		},
		{
			// A cursor with no watermark to fall back on: unbounded beats
			// borrowing the first-run lookback, which could hide older mail.
			name:  "recovery with no watermark is unbounded",
			opts:  FetchOptions{InitialLookback: lookback, Query: "from:a"},
			state: provider.SyncState{},
			want:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.Now = at(fixedNow)
			p := &Provider{account: "work", opts: tc.opts.withDefaults()}
			require.Equal(t, tc.want, p.scanQuery(tc.state, tc.firstEver))
		})
	}
}

func TestInitialLookbackAppliedToFirstRunOnly(t *testing.T) {
	stub := newGmailStub()
	stub.profileHistoryID = 42
	stub.addListPage("", "", "m1")

	p := newTestProvider(t, stub, FetchOptions{
		Query:           "from:jobs@example.com",
		InitialLookback: 720 * time.Hour,
		Now:             at(fixedNow),
	})

	var first collector
	state, err := p.Fetch(context.Background(), provider.SyncState{}, first.fn)
	require.NoError(t, err)

	wantFirst := fmt.Sprintf("(from:jobs@example.com) after:%d", fixedNow.Add(-720*time.Hour).Unix())
	stub.snapshot(func(s *gmailStub) {
		require.Equal(t, []string{wantFirst}, s.listQueries)
	})

	// Second run: the cursor from the profile drives history, so force another
	// full scan by clearing it. That is a *recovery* scan, so the bound is
	// LastSyncTime (less the overlap) rather than the lookback, and the account
	// query is gone — see scanQuery.
	state.HistoryID = 0
	var second collector
	_, err = p.Fetch(context.Background(), state, second.fn)
	require.NoError(t, err)

	wantSecond := fmt.Sprintf("after:%d", state.LastSyncTime.Add(-RecoveryOverlap).Unix())
	stub.snapshot(func(s *gmailStub) {
		require.Equal(t, []string{wantFirst, wantSecond}, s.listQueries)
		require.NotEqual(t, wantFirst, wantSecond)
	})
}

// ---------------------------------------------------------------------------
// watermark consistency (BUG 1)
// ---------------------------------------------------------------------------

// TestWatermarksAreSampledTogether pins the invariant both routes rely on:
// LastSyncTime never describes a later instant than the HistoryID stored beside
// it. Stamping completion time is what broke it.
func TestWatermarksAreSampledTogether(t *testing.T) {
	t.Run("full scan", func(t *testing.T) {
		clock := newFakeClock(fixedNow)
		stub := newGmailStub()
		stub.addListPage("", "", "m1", "m2")
		// The mailbox is read, then time passes before the run finishes.
		stub.afterList = func() { clock.advance(90 * time.Second) }

		p := newTestProvider(t, stub, FetchOptions{Now: clock.now})
		var got collector
		state, err := p.Fetch(context.Background(), provider.SyncState{}, got.fn)
		require.NoError(t, err)

		require.Equal(t, fixedNow, state.LastSyncTime,
			"LastSyncTime must be the instant the profile cursor was sampled, not completion")
		require.True(t, clock.now().After(state.LastSyncTime), "the test clock really did move on")
	})

	t.Run("incremental", func(t *testing.T) {
		clock := newFakeClock(fixedNow)
		stub := newGmailStub()
		stub.addHistoryPage("", "", 1001, 1001, "m1")
		stub.afterHistory = func() { clock.advance(90 * time.Second) }

		p := newTestProvider(t, stub, FetchOptions{Now: clock.now})
		var got collector
		state, err := p.Fetch(context.Background(), provider.SyncState{HistoryID: 1000}, got.fn)
		require.NoError(t, err)

		require.Equal(t, fixedNow, state.LastSyncTime,
			"LastSyncTime must be the instant history.list was asked, not completion")
		require.Equal(t, uint64(1001), state.HistoryID)
	})
}

// TestMidScanArrivalSurvivesCursorExpiry is the exact bug, as a timeline.
//
//	t0      run A samples the profile cursor and starts listing
//	t0+30s  message "midscan" arrives — too late for run A's list snapshot
//	t0+60s  run A finishes and writes its watermarks
//	t0+8d   run B: the history cursor has aged out, so the fallback scan is the
//	        only thing that can still deliver "midscan"
//
// With LastSyncTime stamped at completion the fallback's `after:` bound sits at
// t0+60s, past the message's own date, so the scan skips it — and then installs
// a fresh profile cursor, so no later run ever enumerates it either. The message
// is silently lost.
//
// The stub evaluates the `q` it is sent against a real mailbox, so this asserts
// on what was *delivered*, not on how the query string was spelled.
func TestMidScanArrivalSurvivesCursorExpiry(t *testing.T) {
	clock := newFakeClock(fixedNow)
	stub := newGmailStub()
	stub.profileHistoryID = 1000
	stub.addMailboxMessage("old", fixedNow.Add(-time.Hour), false, true)

	// The arrival lands after run A's list snapshot was taken, so run A cannot
	// see it — exactly the window the watermarks have to cover.
	var once sync.Once
	stub.afterList = func() {
		once.Do(func() {
			clock.advance(30 * time.Second)
			stub.addMailboxMessage("midscan", clock.now(), false, true)
			clock.advance(30 * time.Second)
		})
	}

	p := newTestProvider(t, stub, FetchOptions{
		Query:           "from:jobs@example.com",
		InitialLookback: 720 * time.Hour,
		Now:             clock.now,
	})

	var runA collector
	state, err := p.Fetch(context.Background(), provider.SyncState{}, runA.fn)
	require.NoError(t, err)
	require.Equal(t, []string{"old"}, runA.ids(), "run A cannot see a message that arrived after its list")
	require.Equal(t, fixedNow, state.LastSyncTime,
		"the watermark must describe the moment the cursor was sampled")

	// Run B, over a week later: Gmail has dropped the history cursor.
	stub.afterList = nil
	clock.set(fixedNow.Add(8 * 24 * time.Hour))
	stub.mu.Lock()
	stub.historyStatus = http.StatusNotFound
	stub.profileHistoryID = 9000
	stub.mu.Unlock()

	var runB collector
	state, err = p.Fetch(context.Background(), state, runB.fn)
	require.NoError(t, err, "an expired cursor is recoverable")
	require.Contains(t, runB.ids(), "midscan",
		"a message that arrived between the sampled cursor and the end of the scan must survive the fallback")
	require.Equal(t, uint64(9000), state.HistoryID, "and the fallback does replace the cursor, so this was the last chance")
}

// TestClockJumpDoesNotLoseTheMessage is the same window as the timeline above,
// widened by an untrustworthy clock rather than by the duration of the scan.
// Each subtest is defeated by removing one of the two protections, so neither
// can be dropped quietly:
//
//   - a machine whose clock simply runs ahead of Gmail's needs RecoveryOverlap;
//   - a clock that jumps forward mid-scan by more than the overlap needs the
//     watermark to have been sampled up front, because no fixed overlap covers
//     an arbitrary NTP step.
func TestClockJumpDoesNotLoseTheMessage(t *testing.T) {
	// runTimeline does one scan during which `arrival` lands too late for the
	// list snapshot (with the clock doing whatever `duringScan` says), then a
	// second run a week later whose history cursor has expired. It answers with
	// what the second run delivered.
	runTimeline := func(t *testing.T, arrival time.Time, duringScan func(c *fakeClock)) []string {
		t.Helper()
		clock := newFakeClock(fixedNow)
		stub := newGmailStub()
		stub.profileHistoryID = 1000
		stub.addMailboxMessage("old", fixedNow.Add(-time.Hour), false, true)

		var once sync.Once
		stub.afterList = func() {
			once.Do(func() {
				stub.addMailboxMessage("late", arrival, false, true)
				duringScan(clock)
			})
		}

		p := newTestProvider(t, stub, FetchOptions{InitialLookback: 720 * time.Hour, Now: clock.now})

		var runA collector
		state, err := p.Fetch(context.Background(), provider.SyncState{}, runA.fn)
		require.NoError(t, err)
		require.Equal(t, []string{"old"}, runA.ids(), "run A cannot see the late arrival")

		stub.afterList = nil
		clock.set(fixedNow.Add(8 * 24 * time.Hour))
		stub.mu.Lock()
		stub.historyStatus = http.StatusNotFound
		stub.mu.Unlock()

		var runB collector
		_, err = p.Fetch(context.Background(), state, runB.fn)
		require.NoError(t, err)
		return runB.ids()
	}

	t.Run("clock ahead of gmail", func(t *testing.T) {
		// Gmail dates the arrival 45 minutes *before* the watermark this machine
		// wrote, so the fallback bound would exclude it on the nose.
		got := runTimeline(t, fixedNow.Add(-45*time.Minute), func(*fakeClock) {})
		require.Contains(t, got, "late",
			"RecoveryOverlap must absorb clock skew between this machine and Gmail")
	})

	t.Run("wall clock jumps forward mid-scan", func(t *testing.T) {
		// An NTP step of three days lands between the sampled cursor and the end
		// of the scan. A completion-time watermark would be three days ahead of
		// the message — far past anything a fixed overlap can rescue.
		got := runTimeline(t, fixedNow.Add(30*time.Second), func(c *fakeClock) {
			c.advance(72 * time.Hour)
		})
		require.Contains(t, got, "late",
			"the watermark must be the sampled instant, so a forward clock jump cannot skip past a real arrival")
	})
}

// ---------------------------------------------------------------------------
// population consistency (BUG 2)
// ---------------------------------------------------------------------------

// TestRecoveryScanCoversSamePopulationAsHistory is the invariant behind BUG 2:
// a full scan that replaces an expired cursor must enumerate what the
// incremental path would have. users.history.list is neither query-filtered nor
// Spam/Trash-filtered, so the fallback must not be either — anything the
// fallback misses is missed for good, because it installs a fresh cursor.
func TestRecoveryScanCoversSamePopulationAsHistory(t *testing.T) {
	lastSync := fixedNow.Add(-2 * time.Hour)
	arrived := fixedNow.Add(-time.Hour)

	// One of each interesting kind: inside the account query, outside it, in
	// Spam, in Trash.
	populate := func(s *gmailStub) {
		s.addMailboxMessage("matches-query", arrived, false, true)
		s.addMailboxMessage("outside-query", arrived, false, false)
		s.addMailboxMessage("in-spam", arrived, true, true)
		s.addMailboxMessage("in-trash", arrived, true, false)
	}
	opts := FetchOptions{Query: "from:jobs@example.com", InitialLookback: 720 * time.Hour, Now: at(fixedNow)}
	state := provider.SyncState{HistoryID: 4242, LastSyncTime: lastSync}

	// Incremental: history reports every arrival, whatever label it carries.
	historyStub := newGmailStub()
	populate(historyStub)
	historyStub.addHistoryPage("", "", 4300, 4300, "matches-query", "outside-query", "in-spam", "in-trash")
	var viaHistory collector
	_, err := newTestProvider(t, historyStub, opts).Fetch(context.Background(), state, viaHistory.fn)
	require.NoError(t, err)
	require.Len(t, viaHistory.ids(), 4, "sanity: the incremental path delivers all four")

	// Recovery: the same cursor, now expired.
	recoveryStub := newGmailStub()
	populate(recoveryStub)
	recoveryStub.historyStatus = http.StatusNotFound
	var viaRecovery collector
	_, err = newTestProvider(t, recoveryStub, opts).Fetch(context.Background(), state, viaRecovery.fn)
	require.NoError(t, err)

	require.Equal(t, viaHistory.ids(), viaRecovery.ids(),
		"the fallback scan and the incremental path must cover the same mailbox")
	recoveryStub.snapshot(func(s *gmailStub) {
		require.Equal(t, []string{"true"}, s.listSpamTrash,
			"messages.list hides Spam and Trash by default; history does not, so the scan must ask for them")
		require.NotContains(t, s.listQueries[0], "jobs@example.com",
			"the account query is a cost bound for the first run, not a filter on recovery")
	})
}

// TestFullScanAlwaysIncludesSpamTrash: the flag is not conditional on which kind
// of scan this is, because the population has to match history on every route.
func TestFullScanAlwaysIncludesSpamTrash(t *testing.T) {
	stub := newGmailStub()
	stub.addListPage("", "page-2", "m1")
	stub.addListPage("page-2", "", "m2")

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	_, err := p.Fetch(context.Background(), provider.SyncState{}, got.fn)
	require.NoError(t, err)
	stub.snapshot(func(s *gmailStub) {
		require.Equal(t, []string{"true", "true"}, s.listSpamTrash, "every page asks for spam and trash")
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func countOf(haystack []string, needle string) int {
	n := 0
	for _, v := range haystack {
		if v == needle {
			n++
		}
	}
	return n
}
