package gmail

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/craigjmidwinter/mail-muncher/internal/provider"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
)

// ---------------------------------------------------------------------------
// a message deleted between the listing and the download (BUG: the wedge)
//
// A message can be deleted after users.history.list or users.messages.list has
// named it and before users.messages.get reaches it. Gmail then answers 404 for
// that id.
//
// Treating that as a fetch failure is what wedged a real installation: the
// cycle aborted, the pipeline refused to save state after a fetch error, the
// cursor stayed put, the next cycle asked for the same window, re-listed the
// same dead id, and 404'd again — forever, with no way out but deleting the
// state file.
//
// The fix is one distinction. A 404 on a *specific message* is authoritative
// evidence that the message is gone, so it is skipped and the cycle completes.
// Every other failure still aborts, because a message that might still exist
// must never be passed over.
// ---------------------------------------------------------------------------

// TestVanishedMessageDoesNotAbortIncrementalCycle is the reported scenario
// exactly: a history window listing three messages, the middle one deleted
// before the download reaches it.
func TestVanishedMessageDoesNotAbortIncrementalCycle(t *testing.T) {
	stub := newGmailStub()
	stub.addHistoryPage("", "", 1001, 1001, "m1", "m2", "m3")
	stub.vanish("m2")

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	state, err := p.Fetch(context.Background(), provider.SyncState{HistoryID: 1000}, got.fn)

	require.NoError(t, err, "a message that no longer exists must not fail the cycle")
	require.Equal(t, []string{"m1", "m3"}, got.ids(), "the messages that do exist are still delivered")
	require.Equal(t, uint64(1001), state.HistoryID,
		"the cursor must advance, or the same dead id is re-listed every cycle forever")
	require.Equal(t, fixedNow, state.LastSyncTime)
	require.ElementsMatch(t, []string{"m1", "m3"}, state.SeenIDs,
		"a message that does not exist is not a delivery, so it stays out of the seen-set")
	require.Equal(t, 1, p.VanishedCount(), "the skip is counted, never silent")
}

// TestVanishedMessageDoesNotWedgeTheCursor is the test that would have caught
// the reported bug. Skipping the message is not enough on its own: if the cursor
// did not move, the next cycle would ask the same question and get the same
// 404. Two consecutive cycles over a mailbox whose message stays deleted must
// make progress.
func TestVanishedMessageDoesNotWedgeTheCursor(t *testing.T) {
	stub := newGmailStub()
	// Gmail does not re-report history records at or below the cursor it was
	// given, so the second cycle's window is genuinely a different one.
	stub.historyHonorsStart = true
	stub.addHistoryPage("", "", 1001, 1001, "m1", "m2", "m3")
	stub.vanish("m2")

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})

	var first collector
	state, err := p.Fetch(context.Background(), provider.SyncState{HistoryID: 1000}, first.fn)
	require.NoError(t, err)
	require.Equal(t, []string{"m1", "m3"}, first.ids())
	require.Equal(t, uint64(1001), state.HistoryID)
	require.Equal(t, 1, p.VanishedCount())

	// The world moves on: new mail arrives, m2 is still deleted.
	stub.snapshot(func(s *gmailStub) { s.getIDs = nil })
	stub.addHistoryPage("", "", 1002, 1002, "m4")

	var second collector
	next, err := p.Fetch(context.Background(), state, second.fn)
	require.NoError(t, err)

	require.Equal(t, []string{"m4"}, second.ids(), "the mail that was waiting behind the wedge is delivered")
	require.Equal(t, uint64(1002), next.HistoryID, "and the cursor keeps moving")
	require.Equal(t, 0, p.VanishedCount(), "the counter describes one cycle, not the Provider's lifetime")
	stub.snapshot(func(s *gmailStub) {
		require.NotContains(t, s.getIDs, "m2", "the second cycle must not re-fetch the dead id")
		require.Equal(t, []uint64{1000, 1001}, s.historyStarts,
			"the second cycle asked from the advanced cursor, not the pinned one")
	})
}

// TestVanishedMessageDoesNotAbortFullScan: the same race exists between
// users.messages.list and the download, and both routes share one download
// helper, so both must behave the same way.
func TestVanishedMessageDoesNotAbortFullScan(t *testing.T) {
	stub := newGmailStub()
	stub.profileHistoryID = 7000
	stub.addListPage("", "", "m1", "m2", "m3")
	stub.vanish("m2")

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	state, err := p.Fetch(context.Background(), provider.SyncState{}, got.fn)

	require.NoError(t, err)
	require.Equal(t, []string{"m1", "m3"}, got.ids())
	require.Equal(t, uint64(7000), state.HistoryID, "the scan still installs the profile cursor")
	require.Equal(t, fixedNow, state.LastSyncTime)
	require.ElementsMatch(t, []string{"m1", "m3"}, state.SeenIDs)
	require.Equal(t, 1, p.VanishedCount())
}

// TestVanishedMessageDoesNotWedgeFullScanCursor is the two-cycle wedge test on
// the full-scan path: the second scan must not be pinned to the same window.
func TestVanishedMessageDoesNotWedgeFullScanCursor(t *testing.T) {
	stub := newGmailStub()
	stub.profileHistoryID = 7000
	arrived := fixedNow.Add(-time.Hour)
	stub.addMailboxMessage("m1", arrived, false, true)
	stub.addMailboxMessage("m2", arrived, false, true)
	stub.addMailboxMessage("m3", arrived, false, true)
	stub.vanish("m2")

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})

	var first collector
	state, err := p.Fetch(context.Background(), provider.SyncState{}, first.fn)
	require.NoError(t, err)
	require.Equal(t, []string{"m1", "m3"}, first.ids())
	require.Equal(t, uint64(7000), state.HistoryID)
	require.Equal(t, fixedNow, state.LastSyncTime)
	require.Equal(t, 1, p.VanishedCount())

	// Next cycle: Gmail has stopped listing the deleted message, the mailbox
	// cursor has moved on. HistoryID is cleared to force the scan route again —
	// the same trick TestInitialLookbackAppliedToFirstRunOnly uses.
	stub.unlist("m2")
	stub.snapshot(func(s *gmailStub) {
		s.getIDs = nil
		s.profileHistoryID = 7100
	})
	state.HistoryID = 0

	var second collector
	next, err := p.Fetch(context.Background(), state, second.fn)
	require.NoError(t, err)

	require.Empty(t, second.msgs, "m1 and m3 are already seen, and m2 no longer exists")
	require.Equal(t, uint64(7100), next.HistoryID, "the cursor keeps moving")
	require.Equal(t, 0, p.VanishedCount())
	stub.snapshot(func(s *gmailStub) {
		require.NotContains(t, s.getIDs, "m2", "the second cycle must not re-fetch the dead id")
	})
}

// TestMailboxThatKeepsListingAGoneMessageStillCompletes is the pessimistic
// version: even if a listing kept naming an id that can only ever 404, every
// cycle completes and every cycle advances. The skip cannot become a loop that
// stops mail moving.
func TestMailboxThatKeepsListingAGoneMessageStillCompletes(t *testing.T) {
	stub := newGmailStub()
	stub.profileHistoryID = 7000
	stub.addListPage("", "", "m1", "m2")
	stub.vanish("m2")

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})

	var got collector
	state, err := p.Fetch(context.Background(), provider.SyncState{}, got.fn)
	require.NoError(t, err)
	require.Equal(t, 1, p.VanishedCount())

	stub.snapshot(func(s *gmailStub) { s.profileHistoryID = 7100 })
	state.HistoryID = 0
	next, err := p.Fetch(context.Background(), state, got.fn)
	require.NoError(t, err, "the second cycle completes too")
	require.Equal(t, uint64(7100), next.HistoryID, "and still advances")
	require.Equal(t, 1, p.VanishedCount(), "the id is re-listed and skipped again, not fatal")
}

// ---------------------------------------------------------------------------
// negative controls: everything that is *not* "this message is gone"
//
// These matter as much as the fix. Skipping a message that might still exist
// would advance the cursor past mail that was never delivered — silent mail
// loss, the worst thing this tool can do. Each of these must still abort, and
// must still leave the cursor exactly where it was.
// ---------------------------------------------------------------------------

func TestOnlyAMissingMessageIsBenign(t *testing.T) {
	tests := []struct {
		name     string
		statuses []int
		reason   string
		retry    provider.RetryConfig
	}{
		{
			name:     "403 forbidden",
			statuses: []int{403, 403, 403},
			reason:   "forbidden",
		},
		{
			name:     "401 unauthorized",
			statuses: []int{401, 401, 401},
			reason:   "authError",
		},
		{
			name:     "429 rate limited, retries exhausted",
			statuses: []int{429, 429, 429, 429},
			retry:    provider.RetryConfig{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond},
		},
		{
			name:     "500 backend error, retries exhausted",
			statuses: []int{500, 500, 500, 500},
			retry:    provider.RetryConfig{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := newGmailStub()
			stub.addHistoryPage("", "", 1001, 1001, "m1", "m2", "m3")
			stub.getFailures["m2"] = tc.statuses
			if tc.reason != "" {
				stub.getReasons["m2"] = tc.reason
			}

			opts := FetchOptions{Now: at(fixedNow), Retry: tc.retry, Concurrency: 1}
			p := newTestProvider(t, stub, opts)
			var got collector
			state, err := p.Fetch(context.Background(), provider.SyncState{HistoryID: 1000}, got.fn)

			require.Error(t, err, "a message that may still exist must not be skipped")
			require.Contains(t, err.Error(), "get message m2")
			require.Equal(t, uint64(1000), state.HistoryID, "the cursor must not advance past mail that was never delivered")
			require.True(t, state.LastSyncTime.IsZero(), "and neither must the fallback watermark")
			require.Equal(t, 0, p.VanishedCount(), "nothing vanished; something failed")
		})
	}
}

// TestTransportFailureStillAborts covers the case that never reaches
// googleapi.CheckResponse at all: the connection dies mid-request.
func TestTransportFailureStillAborts(t *testing.T) {
	stub := newGmailStub()
	stub.addHistoryPage("", "", 1001, 1001, "m1", "m2", "m3")
	base := stub.handler()

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/messages/m2") {
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "not hijackable", http.StatusInternalServerError)
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				return
			}
			_ = conn.Close()
			return
		}
		base.ServeHTTP(w, r)
	})

	p := newTestProviderWithHandler(t, h, FetchOptions{
		Now:         at(fixedNow),
		Concurrency: 1,
		Retry:       provider.RetryConfig{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond},
	})
	var got collector
	state, err := p.Fetch(context.Background(), provider.SyncState{HistoryID: 1000}, got.fn)

	require.Error(t, err, "a dead connection says nothing about whether the message exists")
	require.Equal(t, uint64(1000), state.HistoryID, "the cursor must not advance")
	require.True(t, state.LastSyncTime.IsZero())
	require.Equal(t, 0, p.VanishedCount())
}

// TestExpiredHistoryCursorIsStillNotAVanishedMessage keeps the two 404s apart. A
// 404 from users.history.list means the cursor aged out and already falls back
// to a full scan in the same run; it must not be re-read as "a message is gone".
func TestExpiredHistoryCursorIsStillNotAVanishedMessage(t *testing.T) {
	stub := newGmailStub()
	stub.historyStatus = http.StatusNotFound
	stub.profileHistoryID = 8000
	stub.addListPage("", "", "m1")

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	state, err := p.Fetch(context.Background(), provider.SyncState{HistoryID: 1000, LastSyncTime: fixedNow.Add(-time.Hour)}, got.fn)

	require.NoError(t, err)
	require.Equal(t, []string{"m1"}, got.ids(), "the run recovers with a full scan, in the same cycle")
	require.Equal(t, uint64(8000), state.HistoryID)
	require.Equal(t, 0, p.VanishedCount(), "an expired cursor is not a missing message")
}

// ---------------------------------------------------------------------------
// the predicate itself
// ---------------------------------------------------------------------------

func TestMessageGonePredicate(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "404",
			err:  &googleapi.Error{Code: http.StatusNotFound, Message: "Requested entity was not found."},
			want: true,
		},
		{
			name: "notFound reason without a 404 status",
			err: &googleapi.Error{Code: http.StatusBadRequest, Errors: []googleapi.ErrorItem{
				{Reason: "notFound", Message: "Not Found"},
			}},
			want: true,
		},
		{
			name: "404 wrapped the way getRaw's caller sees it",
			err: fmt.Errorf("giving up after 1 attempt(s): %w",
				&googleapi.Error{Code: http.StatusNotFound}),
			want: true,
		},
		{
			name: "403",
			err:  &googleapi.Error{Code: http.StatusForbidden},
			want: false,
		},
		{
			name: "401",
			err:  &googleapi.Error{Code: http.StatusUnauthorized},
			want: false,
		},
		{
			name: "429",
			err:  &googleapi.Error{Code: http.StatusTooManyRequests},
			want: false,
		},
		{
			name: "500",
			err:  &googleapi.Error{Code: http.StatusInternalServerError},
			want: false,
		},
		{
			name: "410 gone is not a Gmail answer we have seen, so it is not assumed benign",
			err:  &googleapi.Error{Code: http.StatusGone},
			want: false,
		},
		{
			name: "context cancellation",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "a plain error",
			err:  errString("connection reset by peer"),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, messageGone(tc.err))
		})
	}
}

// errString is a minimal error value for the predicate table.
type errString string

func (e errString) Error() string { return string(e) }
