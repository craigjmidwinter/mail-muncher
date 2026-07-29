package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSyncStateJSONRoundTrip(t *testing.T) {
	in := SyncState{
		HistoryID:    98765,
		LastSyncTime: time.Date(2026, 7, 28, 15, 4, 5, 0, time.UTC),
		Extra: map[string]string{
			"imap.INBOX.uidvalidity": "1650000000",
			"imap.INBOX.last_uid":    "48213",
		},
		SeenIDs: []string{"a", "b", "c"},
	}

	data, err := json.Marshal(in)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"history_id": 98765,
		"last_sync_time": "2026-07-28T15:04:05Z",
		"extra": {"imap.INBOX.uidvalidity": "1650000000", "imap.INBOX.last_uid": "48213"},
		"seen_ids": ["a","b","c"]
	}`, string(data))

	var out SyncState
	require.NoError(t, json.Unmarshal(data, &out))
	require.Equal(t, in, out)
}

func TestSyncStateZeroValueOmitsOptionalFields(t *testing.T) {
	data, err := json.Marshal(SyncState{})
	require.NoError(t, err)
	require.JSONEq(t, `{"last_sync_time":"0001-01-01T00:00:00Z"}`, string(data))
}

func TestMarkSeenFIFOCap(t *testing.T) {
	var st SyncState
	for i := 0; i < MaxSeenIDs+500; i++ {
		st.MarkSeen(fmt.Sprintf("id-%d", i))
	}

	require.Len(t, st.SeenIDs, MaxSeenIDs)
	// The oldest 500 were evicted; the newest survive, oldest-first.
	require.Equal(t, "id-500", st.SeenIDs[0])
	require.Equal(t, fmt.Sprintf("id-%d", MaxSeenIDs+499), st.SeenIDs[len(st.SeenIDs)-1])
	require.False(t, st.Seen("id-499"))
	require.True(t, st.Seen("id-500"))
	require.True(t, st.Seen(fmt.Sprintf("id-%d", MaxSeenIDs+499)))
}

func TestMarkSeenIgnoresDuplicatesAndEmpty(t *testing.T) {
	var st SyncState
	st.MarkSeen("a")
	st.MarkSeen("b")
	st.MarkSeen("a")
	st.MarkSeen("")

	require.Equal(t, []string{"a", "b"}, st.SeenIDs)
}

func TestExtraHelpers(t *testing.T) {
	var st SyncState
	require.Equal(t, "", st.GetExtra("nope"))

	st.SetExtra("imap.INBOX.last_uid", "12")
	require.Equal(t, "12", st.GetExtra("imap.INBOX.last_uid"))

	st.SetExtra("imap.INBOX.last_uid", "")
	require.Equal(t, "", st.GetExtra("imap.INBOX.last_uid"))
	require.NotContains(t, st.Extra, "imap.INBOX.last_uid")
}

func TestCloneIsDeep(t *testing.T) {
	st := SyncState{
		Extra:   map[string]string{"k": "v"},
		SeenIDs: []string{"a"},
	}
	clone := st.Clone()
	clone.SetExtra("k", "changed")
	clone.MarkSeen("b")

	require.Equal(t, "v", st.GetExtra("k"))
	require.Equal(t, []string{"a"}, st.SeenIDs)
}

func TestFakeReplaysMessages(t *testing.T) {
	msgs := []RawMessage{
		{ID: "m1", Raw: []byte("From: a@example.com\r\n\r\nhi"), InternalDate: time.Unix(1000, 0).UTC(), Labels: []string{"INBOX"}},
		{ID: "m2", Raw: []byte("From: b@example.com\r\n\r\nyo"), InternalDate: time.Unix(2000, 0).UTC()},
	}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	f := NewFake("fakemail", msgs...)
	f.Now = now
	f.NextHistoryID = 42
	f.NextExtra = map[string]string{"imap.INBOX.last_uid": "7"}

	var got []RawMessage
	out, err := f.Fetch(context.Background(), SyncState{}, func(m RawMessage) error {
		got = append(got, m)
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, "fakemail", f.Name())
	require.Equal(t, msgs, got)
	require.Equal(t, now, out.LastSyncTime)
	require.Equal(t, uint64(42), out.HistoryID)
	require.Equal(t, "7", out.GetExtra("imap.INBOX.last_uid"))
	require.Equal(t, []string{"m1", "m2"}, out.SeenIDs)
	require.Equal(t, 1, f.Calls)
	require.Equal(t, []string{"m1", "m2"}, f.Delivered)
}

func TestFakeZeroValueUsable(t *testing.T) {
	var f Fake
	require.Equal(t, "fake", f.Name())

	out, err := f.Fetch(context.Background(), SyncState{}, func(RawMessage) error {
		t.Fatal("no messages expected")
		return nil
	})
	require.NoError(t, err)
	require.False(t, out.LastSyncTime.IsZero())
}

func TestFakeCallbackErrorAbortsWithPartialState(t *testing.T) {
	f := NewFake("fakemail",
		RawMessage{ID: "m1"},
		RawMessage{ID: "m2"},
		RawMessage{ID: "m3"},
	)
	boom := errors.New("sink exploded")

	out, err := f.Fetch(context.Background(), SyncState{}, func(m RawMessage) error {
		if m.ID == "m2" {
			return boom
		}
		return nil
	})

	require.ErrorIs(t, err, boom)
	require.Equal(t, []string{"m1"}, out.SeenIDs)
	require.True(t, out.LastSyncTime.IsZero(), "state must not advance on an aborted fetch")
}

func TestFakeFailEarlyAndSkipSeen(t *testing.T) {
	f := NewFake("fakemail", RawMessage{ID: "m1"}, RawMessage{ID: "m2"})
	f.FailEarly = true
	f.FailAfter = 1
	f.FetchErr = errors.New("rate limited")

	out, err := f.Fetch(context.Background(), SyncState{}, func(RawMessage) error { return nil })
	require.ErrorIs(t, err, f.FetchErr)
	require.Equal(t, []string{"m1"}, out.SeenIDs)

	skipper := NewFake("fakemail", RawMessage{ID: "m1"}, RawMessage{ID: "m2"})
	skipper.SkipSeen = true
	var delivered []string
	_, err = skipper.Fetch(context.Background(), SyncState{SeenIDs: []string{"m1"}}, func(m RawMessage) error {
		delivered = append(delivered, m.ID)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"m2"}, delivered)
}

func TestFakeRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := NewFake("fakemail", RawMessage{ID: "m1"})
	_, err := f.Fetch(ctx, SyncState{}, func(RawMessage) error {
		t.Fatal("should not deliver after cancellation")
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestFakeSatisfiesProvider(t *testing.T) {
	var p Provider = NewFake("fakemail")
	require.Equal(t, "fakemail", p.Name())
}
