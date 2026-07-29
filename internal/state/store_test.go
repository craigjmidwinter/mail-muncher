package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/craigmidwinter/mail-muncher/internal/provider"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "state"))

	want := provider.SyncState{
		HistoryID:    123456,
		LastSyncTime: time.Date(2026, 7, 28, 15, 4, 5, 0, time.UTC),
		Extra: map[string]string{
			"imap.INBOX.uidvalidity": "1650000000",
			"imap.INBOX.last_uid":    "48213",
		},
		SeenIDs: []string{"m1", "m2", "m3"},
	}
	require.NoError(t, s.Save("personal", want))

	got, err := s.Load("personal")
	require.NoError(t, err)
	require.Equal(t, want, got)

	path, err := s.Path("personal")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "state", "personal.json"), path)

	// One JSON file per account, at <state_dir>/<account>.json, owner-only.
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Contains(t, decoded, "history_id")
	require.Contains(t, decoded, "last_sync_time")
	require.Contains(t, decoded, "extra")
	require.Contains(t, decoded, "seen_ids")
}

func TestLoadMissingFileReturnsZeroValue(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "does-not-exist"))

	got, err := s.Load("personal")
	require.NoError(t, err)
	require.Equal(t, provider.SyncState{}, got)
	require.Zero(t, got.HistoryID)
	require.True(t, got.LastSyncTime.IsZero())
	require.Nil(t, got.Extra)
	require.Nil(t, got.SeenIDs)
}

func TestLoadCorruptFileErrors(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "personal.json"), []byte("{not json"), 0o600))

	_, err := s.Load("personal")
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse")
}

func TestSaveOverwritesPreviousState(t *testing.T) {
	s := NewStore(t.TempDir())
	require.NoError(t, s.Save("personal", provider.SyncState{HistoryID: 1}))
	require.NoError(t, s.Save("personal", provider.SyncState{HistoryID: 2}))

	got, err := s.Load("personal")
	require.NoError(t, err)
	require.Equal(t, uint64(2), got.HistoryID)
}

func TestSavePerAccountIsolation(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	require.NoError(t, s.Save("personal", provider.SyncState{HistoryID: 1}))
	require.NoError(t, s.Save("work", provider.SyncState{HistoryID: 2}))

	personal, err := s.Load("personal")
	require.NoError(t, err)
	work, err := s.Load("work")
	require.NoError(t, err)
	require.Equal(t, uint64(1), personal.HistoryID)
	require.Equal(t, uint64(2), work.HistoryID)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 2)
}

// The acceptance case: a write that dies partway through must not corrupt or
// truncate the state file, and must not litter the directory with temp files.
func TestSaveIsAtomicOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	good := provider.SyncState{HistoryID: 7, SeenIDs: []string{"m1"}}
	require.NoError(t, s.Save("personal", good))

	path, err := s.Path("personal")
	require.NoError(t, err)
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	// Simulate a disk that accepts half the bytes and then fails.
	original := writeAndSync
	diskFull := errors.New("no space left on device")
	writeAndSync = func(f *os.File, data []byte) error {
		if _, err := f.Write(data[:len(data)/2]); err != nil {
			return err
		}
		return diskFull
	}
	t.Cleanup(func() { writeAndSync = original })

	err = s.Save("personal", provider.SyncState{HistoryID: 999, SeenIDs: []string{"m1", "m2"}})
	require.ErrorIs(t, err, diskFull)

	// The old file is byte-for-byte intact...
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)

	loaded, err := s.Load("personal")
	require.NoError(t, err)
	require.Equal(t, good, loaded)

	// ...and no partial temp file was left behind.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "personal.json", entries[0].Name())
}

func TestSaveAtomicFailureLeavesNoFileWhenNoneExisted(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	original := writeAndSync
	writeAndSync = func(f *os.File, data []byte) error { return errors.New("boom") }
	t.Cleanup(func() { writeAndSync = original })

	require.Error(t, s.Save("personal", provider.SyncState{HistoryID: 1}))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries, "no partial or temp file should survive")

	got, err := s.Load("personal")
	require.NoError(t, err)
	require.Equal(t, provider.SyncState{}, got)
}

func TestSaveTrimsSeenIDsToCap(t *testing.T) {
	s := NewStore(t.TempDir())

	ids := make([]string, 0, provider.MaxSeenIDs+250)
	for i := range provider.MaxSeenIDs + 250 {
		ids = append(ids, fmt.Sprintf("id-%d", i))
	}
	require.NoError(t, s.Save("personal", provider.SyncState{SeenIDs: ids}))

	got, err := s.Load("personal")
	require.NoError(t, err)
	require.Len(t, got.SeenIDs, provider.MaxSeenIDs)
	require.Equal(t, "id-250", got.SeenIDs[0], "oldest ids are evicted first (FIFO)")
	require.Equal(t, fmt.Sprintf("id-%d", provider.MaxSeenIDs+249), got.SeenIDs[len(got.SeenIDs)-1])
}

// Seen ids accumulate across runs the way the pipeline uses them: load, mark,
// save — and the set stays capped.
func TestSeenIDEvictionAcrossRuns(t *testing.T) {
	s := NewStore(t.TempDir())

	next := 0
	for run := 0; run < 3; run++ {
		st, err := s.Load("personal")
		require.NoError(t, err)
		for i := 0; i < 1000; i++ {
			st.MarkSeen(fmt.Sprintf("id-%d", next))
			next++
		}
		require.NoError(t, s.Save("personal", st))
	}

	got, err := s.Load("personal")
	require.NoError(t, err)
	require.Len(t, got.SeenIDs, provider.MaxSeenIDs)
	require.False(t, got.Seen("id-0"), "the earliest ids fell off the front")
	require.True(t, got.Seen("id-2999"), "the most recent ids survive")
	require.Equal(t, "id-1000", got.SeenIDs[0])
}

func TestLoadTrimsOversizedSeenSetOnDisk(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	ids := make([]string, 0, provider.MaxSeenIDs+10)
	for i := range provider.MaxSeenIDs + 10 {
		ids = append(ids, fmt.Sprintf("id-%d", i))
	}
	data, err := json.Marshal(provider.SyncState{SeenIDs: ids})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "personal.json"), data, 0o600))

	got, err := s.Load("personal")
	require.NoError(t, err)
	require.Len(t, got.SeenIDs, provider.MaxSeenIDs)
}

func TestInvalidAccountNames(t *testing.T) {
	s := NewStore(t.TempDir())

	for _, account := range []string{"", "..", "../escape", "a/b", "nested/../../etc/passwd"} {
		_, err := s.Load(account)
		require.ErrorIs(t, err, ErrInvalidAccount, "Load(%q)", account)
		require.ErrorIs(t, s.Save(account, provider.SyncState{}), ErrInvalidAccount, "Save(%q)", account)
	}
}

func TestValidAccountNamesWithDots(t *testing.T) {
	s := NewStore(t.TempDir())
	require.NoError(t, s.Save("user@example.com", provider.SyncState{HistoryID: 5}))

	got, err := s.Load("user@example.com")
	require.NoError(t, err)
	require.Equal(t, uint64(5), got.HistoryID)
}

func TestDelete(t *testing.T) {
	s := NewStore(t.TempDir())
	require.NoError(t, s.Save("personal", provider.SyncState{HistoryID: 3}))
	require.NoError(t, s.Delete("personal"))
	require.NoError(t, s.Delete("personal"), "deleting a missing file is not an error")

	got, err := s.Load("personal")
	require.NoError(t, err)
	require.Equal(t, provider.SyncState{}, got)
}

func TestSaveCreatesStateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "c")
	s := NewStore(dir)
	require.NoError(t, s.Save("personal", provider.SyncState{}))

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.True(t, info.IsDir())
}

func TestLockExcludesASecondHolder(t *testing.T) {
	s := NewStore(t.TempDir())

	lock, err := s.TryLock()
	require.NoError(t, err)
	require.Equal(t, s.LockPath(), lock.Path())

	// flock is per-process-advisory: a second *Flock handle on the same file in
	// the same process still contends on some platforms, so assert via the
	// waiting API with a deadline instead of asserting immediate failure.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = NewStore(s.Dir()).LockWait(ctx, 5*time.Millisecond)
	require.Error(t, err)

	require.NoError(t, lock.Unlock())
	require.NoError(t, lock.Unlock(), "unlocking twice is a no-op")

	again, err := s.TryLock()
	require.NoError(t, err)
	require.NoError(t, again.Unlock())
}
