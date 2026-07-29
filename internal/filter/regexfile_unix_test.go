//go:build unix

package filter

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestReadPatternFileReadsTheFileExactlyOnce is the structural half of the
// accounting's contract, and the reason the counts live next to the patterns
// rather than being derived from them.
//
// The pattern list here is a FIFO, which yields its contents to exactly one
// reader: a second os.ReadFile of it has nothing to read and blocks forever.
// So the test passes only if the patterns in force and the number of lines
// refused were both produced by the same single pass over the same bytes. An
// implementation that re-read the file to count the rejections — the shape this
// replaced — cannot pass it at all, and neither can one that reads once but
// leaves a skew window open for a caller to fall into.
func TestReadPatternFileReadsTheFileExactlyOnce(t *testing.T) {
	captureLogs(t)

	path := filepath.Join(t.TempDir(), "patterns.fifo")
	require.NoError(t, syscall.Mkfifo(path, 0o600))

	// Two lines in force, two refused: enough that a count of zero, a count of
	// everything, or a count taken from a second (empty) read all read
	// differently from the right answer.
	const body = "# tracked companies\nwagepoint\n.*\nteamtailor\n[unclosed\n"

	wrote := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			wrote <- err
			return
		}
		_, werr := f.WriteString(body)
		wrote <- errors.Join(werr, f.Close())
	}()

	type result struct {
		stats PatternFileStats
		err   error
	}
	read := make(chan result, 1)
	go func() {
		stats, err := ReadPatternFile(path)
		read <- result{stats, err}
	}()

	var got result
	select {
	case got = <-read:
	case <-time.After(30 * time.Second):
		t.Fatal("ReadPatternFile never returned: this file can be read exactly once, " +
			"so the patterns and the counts must come from the same read")
	}

	require.NoError(t, <-wrote)
	require.NoError(t, got.err)
	require.Equal(t, []string{"wagepoint", "teamtailor"}, patternSources(got.stats.Patterns))
	require.Equal(t, 2, got.stats.Rejected)
	require.Equal(t, 4, got.stats.Total)
	require.ErrorContains(t, got.stats.Err, "2 of 4 patterns rejected")
}
