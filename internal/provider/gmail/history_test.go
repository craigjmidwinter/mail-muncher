package gmail

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/craigjmidwinter/mail-muncher/internal/provider"
	"github.com/stretchr/testify/require"
	gmailapi "google.golang.org/api/gmail/v1"
)

func TestIncrementalSyncWalksMultipleHistoryPages(t *testing.T) {
	stub := newGmailStub()
	stub.profileHistoryID = 999999 // must not be used on the incremental path
	stub.addHistoryPage("", "h2", 1001, 1001, "m1", "m2")
	stub.addHistoryPage("h2", "h3", 1002, 1002, "m3")
	stub.addHistoryPage("h3", "", 1007, 1009, "m4")

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	state, err := p.Fetch(context.Background(), provider.SyncState{HistoryID: 1000}, got.fn)
	require.NoError(t, err)

	require.Equal(t, []string{"m1", "m2", "m3", "m4"}, got.ids())
	stub.snapshot(func(s *gmailStub) {
		require.Equal(t, []string{"", "h2", "h3"}, s.historyTokens, "every nextPageToken is followed")
		require.Equal(t, []uint64{1000, 1000, 1000}, s.historyStarts)
		require.Equal(t, []string{"messageAdded", "messageAdded", "messageAdded"}, s.historyTypes)
		require.Empty(t, s.listQueries, "the incremental path must not fall back to a full scan")
		require.Zero(t, s.profileCalls, "the incremental path must not read the profile")
	})

	require.Equal(t, uint64(1009), state.HistoryID, "cursor advances to the highest history id seen")
	require.Equal(t, fixedNow, state.LastSyncTime)
	require.ElementsMatch(t, []string{"m1", "m2", "m3", "m4"}, state.SeenIDs)
}

func TestIncrementalSyncDedupesIDsAcrossPages(t *testing.T) {
	stub := newGmailStub()
	stub.addHistoryPage("", "h2", 2001, 2001, "m1", "m2")
	stub.addHistoryPage("h2", "", 2002, 2002, "m2", "m3")

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	_, err := p.Fetch(context.Background(), provider.SyncState{HistoryID: 2000}, got.fn)
	require.NoError(t, err)
	require.Equal(t, []string{"m1", "m2", "m3"}, got.ids())
	stub.snapshot(func(s *gmailStub) {
		require.Len(t, s.getIDs, 3, "an id repeated across history pages is downloaded once")
	})
}

func TestIncrementalSyncAdvancesCursorWithNothingNew(t *testing.T) {
	stub := newGmailStub()
	stub.historyPages[""] = &gmailapi.ListHistoryResponse{HistoryId: 3500}

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	state, err := p.Fetch(context.Background(), provider.SyncState{HistoryID: 3000}, got.fn)
	require.NoError(t, err)

	require.Empty(t, got.msgs)
	require.Equal(t, uint64(3500), state.HistoryID, "an empty history page still moves the cursor forward")
	require.Equal(t, fixedNow, state.LastSyncTime)
	stub.snapshot(func(s *gmailStub) {
		require.Empty(t, s.getIDs)
		require.Zero(t, s.labelCalls, "no downloads means no need for the label map")
	})
}

func TestIncrementalSyncNeverGoesBackwards(t *testing.T) {
	stub := newGmailStub()
	// A page that reports an older historyId than the cursor we started from.
	stub.addHistoryPage("", "", 10, 20, "m1")

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	state, err := p.Fetch(context.Background(), provider.SyncState{HistoryID: 5000}, got.fn)
	require.NoError(t, err)
	require.Equal(t, uint64(5000), state.HistoryID)
}

func TestIncrementalSyncSkipsSeenIDs(t *testing.T) {
	stub := newGmailStub()
	stub.addHistoryPage("", "", 4001, 4001, "m1", "m2")

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	_, err := p.Fetch(context.Background(), provider.SyncState{HistoryID: 4000, SeenIDs: []string{"m1"}}, got.fn)
	require.NoError(t, err)
	require.Equal(t, []string{"m2"}, got.ids())
}

func TestExpiredHistoryIDFallsBackToFullScanInTheSameRun(t *testing.T) {
	stub := newGmailStub()
	stub.historyStatus = http.StatusNotFound
	stub.profileHistoryID = 77777
	stub.addListPage("", "page-2", "m1", "m2")
	stub.addListPage("page-2", "", "m3")

	p := newTestProvider(t, stub, FetchOptions{
		Query:           "from:jobs@example.com",
		InitialLookback: 720 * time.Hour,
		Now:             at(fixedNow),
	})

	var got collector
	lastSync := fixedNow.Add(-6 * time.Hour)
	before := provider.SyncState{HistoryID: 4242, LastSyncTime: lastSync}
	state, err := p.Fetch(context.Background(), before, got.fn)
	require.NoError(t, err, "an expired history id is recoverable, not fatal")

	// The full scan ran in this same call, bounded by LastSyncTime (less the
	// recovery overlap) rather than the initial lookback — and *not* by the
	// account query, because a recovery scan stands in for the incremental path
	// and has to enumerate the same population.
	stub.snapshot(func(s *gmailStub) {
		require.Len(t, s.historyStarts, 1, "history.list is tried once, then abandoned")
		require.Len(t, s.listQueries, 2, "the fallback full scan ran, across both pages")
		require.Equal(t,
			fmt.Sprintf("after:%d", lastSync.Add(-RecoveryOverlap).Unix()),
			s.listQueries[0])
		require.NotContains(t, s.listQueries[0], "jobs@example.com")
		require.Equal(t, []string{"true", "true"}, s.listSpamTrash)
		require.Equal(t, 1, s.profileCalls)
	})
	require.Equal(t, []string{"m1", "m2", "m3"}, got.ids())
	require.Equal(t, uint64(77777), state.HistoryID, "the cursor is replaced from the profile")
	require.Equal(t, fixedNow, state.LastSyncTime)
}

func TestHistoryErrorsOtherThan404AreFatal(t *testing.T) {
	stub := newGmailStub()
	stub.historyStatus = http.StatusUnauthorized
	stub.historyReason = "authError"
	stub.addListPage("", "", "m1")

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	state, err := p.Fetch(context.Background(), provider.SyncState{HistoryID: 100}, got.fn)
	require.Error(t, err)
	require.Contains(t, err.Error(), "list history for account \"work\"")
	require.NotErrorIs(t, err, ErrHistoryExpired)
	require.Equal(t, uint64(100), state.HistoryID, "a hard failure leaves the cursor alone")
	stub.snapshot(func(s *gmailStub) {
		require.Empty(t, s.listQueries, "only a 404 falls back to a full scan")
	})
}

func TestHistory404IsClassifiedAsExpired(t *testing.T) {
	stub := newGmailStub()
	stub.historyStatus = http.StatusNotFound

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	_, _, err := p.historyIDs(context.Background(), 12345)
	require.ErrorIs(t, err, ErrHistoryExpired)
	require.Contains(t, err.Error(), "12345")
}

func TestHistoryTolerantOfSparseRecords(t *testing.T) {
	stub := newGmailStub()
	stub.historyPages[""] = &gmailapi.ListHistoryResponse{
		HistoryId: 6100,
		History: []*gmailapi.History{
			nil,
			{Id: 6001},
			{Id: 6002, MessagesAdded: []*gmailapi.HistoryMessageAdded{nil, {}, {Message: &gmailapi.Message{}}}},
			{Id: 6003, MessagesAdded: []*gmailapi.HistoryMessageAdded{{Message: &gmailapi.Message{Id: "m1"}}}},
		},
	}
	stub.addMessage("m1", time.UnixMilli(1700000000000), []string{"INBOX"}, []byte("Subject: m1\r\n\r\n"))

	p := newTestProvider(t, stub, FetchOptions{Now: at(fixedNow)})
	var got collector
	state, err := p.Fetch(context.Background(), provider.SyncState{HistoryID: 6000}, got.fn)
	require.NoError(t, err)
	require.Equal(t, []string{"m1"}, got.ids())
	require.Equal(t, uint64(6100), state.HistoryID)
}
