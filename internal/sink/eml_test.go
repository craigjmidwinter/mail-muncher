package sink

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/model"
)

func TestEMLPathConstruction(t *testing.T) {
	msg := testMessage()
	rule := testRule("/archive/job-search")

	want := "/archive/job-search/2026/07/" + fixtureBase + ".eml"
	assert.Equal(t, want, NewEML().Path(msg, rule))
}

func TestEMLPathWeirdSubjects(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		want    string
	}{
		{"empty", "", fixtureUnix + "-" + fixtureHash + "-no-subject.eml"},
		{"emoji", "🎉 Congrats! 🎉", fixtureUnix + "-" + fixtureHash + "-congrats.eml"},
		{"emoji only", "🎉🎉🎉", fixtureUnix + "-" + fixtureHash + "-no-subject.eml"},
		{
			"200 characters",
			strings.Repeat("Very Long Subject ", 12)[:200],
			fixtureUnix + "-" + fixtureHash + "-very-long-subject-very-long-subject-very.eml",
		},
		{"path traversal attempt", "../../etc/passwd", fixtureUnix + "-" + fixtureHash + "-etc-passwd.eml"},
		{"newlines and tabs", "line one\nline\ttwo", fixtureUnix + "-" + fixtureHash + "-line-one-line-two.eml"},
	}

	s := NewEML()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A fresh destination per case: several of these subjects slug
			// to the same name on purpose, and a shared dest would turn the
			// second one into a skip.
			dest := t.TempDir()
			rule := testRule(dest)

			msg := testMessage()
			msg.Subject = tt.subject

			assert.Equal(t, filepath.Join(dest, "2026", "07", tt.want), s.Path(msg, rule))

			// The name must also survive an actual write: no separator
			// smuggled in, nothing landing outside the month directory.
			path, skipped, err := s.Store(msg, rule)
			require.NoError(t, err)
			assert.False(t, skipped)
			assert.Equal(t, filepath.Join(dest, "2026", "07"), filepath.Dir(path))
			assert.FileExists(t, path)
		})
	}
}

func TestEMLStoreIsByteFaithful(t *testing.T) {
	dest := t.TempDir()
	msg := testMessage()
	// Raw bytes a re-encoder would mangle: CRLF endings, trailing
	// whitespace, an 8-bit body, and no trailing newline.
	msg.Raw = []byte("From: jane@acme.com\r\nSubject: =?utf-8?B?8J+OiQ==?=\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nbody with trailing space   \r\n\xc3\xa9\xff\r\nno final newline")

	path, skipped, err := NewEML().Store(msg, testRule(dest))
	require.NoError(t, err)
	assert.False(t, skipped)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, msg.Raw, got, "the file must be the fetched bytes, unchanged")
}

func TestEMLStoreCreatesMonthDirectories(t *testing.T) {
	dest := t.TempDir()
	msg := testMessage()
	msg.Date = time.Date(2026, 1, 3, 23, 59, 0, 0, time.UTC)

	path, _, err := NewEML().Store(msg, testRule(dest))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dest, "2026", "01"), filepath.Dir(path), "month must be zero-padded")

	info, err := os.Stat(filepath.Join(dest, "2026", "01"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestEMLStoreSkipsExistingFile(t *testing.T) {
	dest := t.TempDir()
	msg := testMessage()
	rule := testRule(dest)
	s := NewEML()

	path, skipped, err := s.Store(msg, rule)
	require.NoError(t, err)
	require.False(t, skipped)

	// Stand-in for "this file was already archived": if the sink rewrote
	// it, this sentinel would disappear.
	sentinel := []byte("archived earlier, do not touch")
	require.NoError(t, os.WriteFile(path, sentinel, 0o644))

	again, skipped, err := s.Store(msg, rule)
	require.NoError(t, err)
	assert.True(t, skipped, "a second Store of the same message must skip")
	assert.Equal(t, path, again)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, sentinel, got, "skipping must not rewrite the file")
}

func TestEMLStoreIsIdempotentAcrossSinkInstances(t *testing.T) {
	dest := t.TempDir()
	rule := testRule(dest)

	// Two runs of the tool, two Message values built from the same mail.
	first, _, err := NewEML().Store(testMessage(), rule)
	require.NoError(t, err)
	second, skipped, err := NewEML().Store(testMessage(), rule)
	require.NoError(t, err)

	assert.Equal(t, first, second)
	assert.True(t, skipped)

	entries, err := os.ReadDir(filepath.Join(dest, "2026", "07"))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "the same message must never produce two files")
}

func TestEMLPlanDoesNotWrite(t *testing.T) {
	dest := t.TempDir()
	msg := testMessage()
	rule := testRule(dest)
	s := NewEML()

	// This is what `--dry-run` calls: it reports the path it would write
	// without creating so much as a directory.
	path, skipped, err := s.Plan(msg, rule)
	require.NoError(t, err)
	assert.False(t, skipped)
	assert.Equal(t, s.Path(msg, rule), path)

	entries, err := os.ReadDir(dest)
	require.NoError(t, err)
	assert.Empty(t, entries, "a dry run must leave the destination untouched")

	// Once the file exists, Plan reports the skip a real run would take.
	_, _, err = s.Store(msg, rule)
	require.NoError(t, err)
	_, skipped, err = s.Plan(msg, rule)
	require.NoError(t, err)
	assert.True(t, skipped)
}

func TestEMLStoreWriteFailureLeavesNoDroppings(t *testing.T) {
	dest := t.TempDir()
	msg := testMessage()
	rule := testRule(dest)

	boom := errors.New("disk on fire")
	restore := writeAndSync
	writeAndSync = func(*os.File, []byte) error { return boom }
	t.Cleanup(func() { writeAndSync = restore })

	path, skipped, err := NewEML().Store(msg, rule)
	require.ErrorIs(t, err, boom)
	assert.False(t, skipped)
	assert.NoFileExists(t, path)

	// The month directory is created up front, but nothing may be left
	// inside it — no partial .eml, no temp file.
	entries, err := os.ReadDir(filepath.Join(dest, "2026", "07"))
	require.NoError(t, err)
	assert.Empty(t, entries, "a failed write must leave no temp file behind")
}

func TestEMLStoreRejectsRuleWithoutDest(t *testing.T) {
	_, _, err := NewEML().Store(testMessage(), &config.Rule{Name: "no-dest"})
	assert.ErrorIs(t, err, ErrNoDest)

	_, _, err = NewEML().Store(testMessage(), nil)
	assert.ErrorIs(t, err, ErrNoDest)
}

func TestEMLStoreEmptyRaw(t *testing.T) {
	dest := t.TempDir()
	msg := &model.Message{ID: "empty", Account: "personal", Date: fixtureDate}

	path, _, err := NewEML().Store(msg, testRule(dest))
	require.NoError(t, err)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestEMLFilePermissions pins the modes as literals, not as the package
// constants: archived mail is private correspondence, and "no other local user
// can read it" is a property of the numbers, not of whatever the constants
// happen to say.
func TestEMLFilePermissions(t *testing.T) {
	// A dest that does not exist yet, so every directory on the way down is
	// one this package created.
	dest := filepath.Join(t.TempDir(), "archive", "job-search")
	path, _, err := NewEML().Store(testMessage(), testRule(dest))
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	for _, d := range []string{filepath.Dir(path), filepath.Join(dest, "2026"), dest} {
		info, err := os.Stat(d)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "directory %s", d)
	}
}
