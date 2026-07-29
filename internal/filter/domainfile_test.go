package filter

import (
	"bufio"
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/model"
	"github.com/stretchr/testify/require"
)

// captureLogs redirects the default slog logger into a buffer for the duration
// of the test and returns everything written to it.
func captureLogs(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf.String
}

// writeDomains drops a domain list into a temp dir and returns its path.
func writeDomains(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "domains.txt")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestParseDomainList(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"plain", "example.com\nother.test\n", []string{"example.com", "other.test"}},
		{"blank lines and whitespace", "\n\n  example.com  \n\t\n", []string{"example.com"}},
		{"full line comments", "# header\nexample.com\n#trailing note\n", []string{"example.com"}},
		{"trailing comments", "example.com # applied 2026-07-01\n", []string{"example.com"}},
		{"comment only", "#example.com\n", nil},
		{"lowercased", "Example.COM\nMAIL.Example.Org\n", []string{"example.com", "mail.example.org"}},
		{"leading at", "@example.com\n@ other.test\n", []string{"example.com", "other.test"}},
		{"trailing dot", "example.com.\n", []string{"example.com"}},
		{"duplicates collapse", "example.com\nEXAMPLE.com\n@example.com\n", []string{"example.com"}},
		{"no trailing newline", "example.com", []string{"example.com"}},
		{"crlf", "example.com\r\nother.test\r\n", []string{"example.com", "other.test"}},
		{"empty file", "", nil},
		{"non-domain kept", "localhost\nexample.com\n", []string{"localhost", "example.com"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDomainList([]byte(tc.body), "test")
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestParseDomainListReportsTruncation is the regression test for a scanner
// error that used to be dropped on the floor: one over-long line silently
// deleted every entry after it, and nothing anywhere said so.
func TestParseDomainListReportsTruncation(t *testing.T) {
	captureLogs(t)

	// 1 MiB is the scanner's line cap; a longer line stops the scan dead.
	huge := strings.Repeat("a", 2*1024*1024)
	body := "first.com\n" + huge + "\nlast.com\n"

	got, err := parseDomainList([]byte(body), "/list.txt")
	require.Error(t, err, "a truncated scan must not be reported as a clean parse")
	require.ErrorIs(t, err, bufio.ErrTooLong)
	require.Contains(t, err.Error(), "truncated")
	require.Equal(t, []string{"first.com"}, got, "entries before the bad line are still usable")
	require.NotContains(t, got, "last.com", "the rest of the file really was lost")
}

// TestDomainFilesTruncationIsDegradation: the truncation reaches the pipeline
// as degradation, so a cycle can decide not to advance past mail it filtered
// with half a list.
func TestDomainFilesTruncationIsDegradation(t *testing.T) {
	captureLogs(t)
	path := writeDomains(t, "first.com\n"+strings.Repeat("a", 2*1024*1024)+"\nlast.com\n")

	files := NewDomainFiles()
	require.Equal(t, []string{"first.com"}, files.Domains(path))

	degraded := files.Degraded()
	require.Len(t, degraded, 1)
	require.Equal(t, path, degraded[0].Path)
	require.ErrorIs(t, degraded[0].Err, bufio.ErrTooLong)
	require.Contains(t, degraded[0].String(), "truncated")
}

func TestParseDomainListWarnsAboutNonDomains(t *testing.T) {
	logs := captureLogs(t)
	got, err := parseDomainList([]byte("localhost\nexample.com\n"), "/list.txt")
	require.NoError(t, err)
	require.Equal(t, []string{"localhost", "example.com"}, got)
	require.Contains(t, logs(), "does not look like a domain")
	require.Contains(t, logs(), "localhost")
}

func TestDomainFilesReadsTestdataFixture(t *testing.T) {
	got := NewDomainFiles().Domains(filepath.Join("testdata", "domains.txt"))
	require.Equal(t, []string{"example.com", "mail.example.org", "stripe.com", "squareup.com", "localhost"}, got)
}

func TestDomainFilesCachesWithinACycle(t *testing.T) {
	path := writeDomains(t, "example.com\n")
	files := NewDomainFiles()

	require.Equal(t, []string{"example.com"}, files.Domains(path))

	// A mid-cycle edit is deliberately not visible: the file is read once per
	// cycle, not once per message.
	require.NoError(t, os.WriteFile(path, []byte("other.test\n"), 0o600))
	require.Equal(t, []string{"example.com"}, files.Domains(path))

	files.BeginCycle()
	require.Equal(t, []string{"other.test"}, files.Domains(path))
}

func TestDomainFilesMissingFileMatchesNothingAndWarnsOncePerCycle(t *testing.T) {
	logs := captureLogs(t)
	path := filepath.Join(t.TempDir(), "not-created-yet.txt")
	files := NewDomainFiles()

	require.Nil(t, files.Domains(path))
	require.Nil(t, files.Domains(path))
	require.Equal(t, 1, strings.Count(logs(), "domain list unavailable"), "one warning per file per cycle")
	require.Contains(t, logs(), "level=WARN")

	// A second cycle warns again, because the file is still missing.
	files.BeginCycle()
	require.Nil(t, files.Domains(path))
	require.Equal(t, 2, strings.Count(logs(), "domain list unavailable"))

	// And once the owning program writes it, the next cycle picks it up.
	require.NoError(t, os.WriteFile(path, []byte("example.com\n"), 0o600))
	files.BeginCycle()
	require.Equal(t, []string{"example.com"}, files.Domains(path))
}

// TestDomainFilesReportMissingFileAsDegraded is the reporting half of the
// "missing file matches nothing" rule: matching nothing must stay an
// evaluation-time non-event, but the caller has to be able to find out that it
// happened, or it will advance a cursor past mail it never really evaluated.
func TestDomainFilesReportMissingFileAsDegraded(t *testing.T) {
	captureLogs(t)
	path := filepath.Join(t.TempDir(), "not-created-yet.txt")
	files := NewDomainFiles()

	require.Empty(t, files.Degraded(), "nothing read yet, nothing degraded")

	require.Nil(t, files.Domains(path), "evaluation semantics are unchanged: matches nothing")

	degraded := files.Degraded()
	require.Len(t, degraded, 1)
	require.Equal(t, path, degraded[0].Path)
	require.ErrorIs(t, degraded[0].Err, os.ErrNotExist)

	// A second cycle starts clean, and stays clean once the file appears.
	require.NoError(t, os.WriteFile(path, []byte("example.com\n"), 0o600))
	files.BeginCycle()
	require.Empty(t, files.Degraded(), "BeginCycle clears the previous cycle's degradation")
	require.Equal(t, []string{"example.com"}, files.Domains(path))
	require.Empty(t, files.Degraded())
}

// TestDomainFilesPreloadReadsUpFront: the pipeline needs to know a list is
// unreadable *before* it stores anything, not on the first message that
// happens to be evaluated against it.
func TestDomainFilesPreloadReadsUpFront(t *testing.T) {
	captureLogs(t)
	present := writeDomains(t, "example.com\n")
	missing := filepath.Join(t.TempDir(), "gone.txt")

	files := NewDomainFiles()
	files.Preload([]string{present, missing})

	degraded := files.Degraded()
	require.Len(t, degraded, 1, "only the unreadable list is degraded")
	require.Equal(t, missing, degraded[0].Path)

	// Preload primed the cache: a mid-cycle edit is still invisible.
	require.NoError(t, os.WriteFile(present, []byte("other.test\n"), 0o600))
	require.Equal(t, []string{"example.com"}, files.Domains(present))
}

// TestEngineSurfacesDegradedDomainFiles wires the two halves together the way
// the pipeline uses them.
func TestEngineSurfacesDegradedDomainFiles(t *testing.T) {
	captureLogs(t)
	missing := filepath.Join(t.TempDir(), "wanted.txt")

	e, err := NewEngine([]config.Rule{rule(t, "wanted", "", "{from_domains_file: "+missing+"}")})
	require.NoError(t, err)
	require.Equal(t, []string{missing}, e.DomainFilePaths())

	e.BeginCycle()
	e.Preload()

	degraded := e.Degraded()
	require.Len(t, degraded, 1)
	require.Equal(t, missing, degraded[0].Path)

	// The message is still evaluated, and still matches nothing.
	require.Nil(t, e.Evaluate(msg(nil)))

	require.NoError(t, os.WriteFile(missing, []byte("example.com\n"), 0o600))
	e.BeginCycle()
	e.Preload()
	require.Empty(t, e.Degraded())
	require.NotNil(t, e.Evaluate(msg(nil)), "the rule fires once its list exists")
}

func TestDomainFilesUnreadableDirectory(t *testing.T) {
	// A directory where a file was expected is unreadable, not fatal.
	captureLogs(t)
	files := NewDomainFiles()
	require.Nil(t, files.Domains(t.TempDir()))
}

func TestFromDomainsFileMatching(t *testing.T) {
	captureLogs(t)
	path := writeDomains(t, "# companies\n@Example.COM\nsquareup.com\n")

	files := NewDomainFiles()
	m, err := Compile(node(t, "{from_domains_file: "+path+"}"), WithDomainFiles(files))
	require.NoError(t, err)

	// Subdomain of a listed domain, matched case-insensitively.
	require.True(t, m.Match(msg(nil)))
	require.True(t, m.Match(msg(func(x *model.Message) { x.From = addrs("a@SQUAREUP.com") })))
	require.False(t, m.Match(msg(func(x *model.Message) { x.From = addrs("a@stripe.com") })))
	require.False(t, m.Match(msg(func(x *model.Message) { x.From = nil })))
}

func TestFromDomainsFileReloadsBetweenCycles(t *testing.T) {
	captureLogs(t)
	path := writeDomains(t, "example.com\n")

	files := NewDomainFiles()
	m, err := Compile(node(t, "{from_domains_file: "+path+"}"), WithDomainFiles(files))
	require.NoError(t, err)

	stripe := msg(func(x *model.Message) { x.From = addrs("receipts@stripe.com") })

	files.BeginCycle()
	require.True(t, m.Match(msg(nil)))
	require.False(t, m.Match(stripe))

	// The job-search tracker adds a company between cycles.
	require.NoError(t, os.WriteFile(path, []byte("example.com\nstripe.com\n"), 0o600))
	files.BeginCycle()
	require.True(t, m.Match(stripe))

	// ...and removes both, without restarting mail-muncher.
	require.NoError(t, os.WriteFile(path, []byte("# nothing tracked right now\n"), 0o600))
	files.BeginCycle()
	require.False(t, m.Match(stripe))
	require.False(t, m.Match(msg(nil)))
}

func TestFromDomainsFileMissingFileIsNotAnError(t *testing.T) {
	logs := captureLogs(t)
	path := filepath.Join(t.TempDir(), "missing.txt")

	m, err := Compile(node(t, "{from_domains_file: "+path+"}"))
	require.NoError(t, err, "a missing file must not fail compilation")
	require.False(t, m.Match(msg(nil)))
	require.Contains(t, logs(), "domain list unavailable")
}

func TestDomainFilesConcurrentUse(t *testing.T) {
	captureLogs(t)
	path := writeDomains(t, "example.com\n")
	files := NewDomainFiles()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// require.* calls Goexit, which is not safe off the test
			// goroutine; report instead.
			if got := files.Domains(path); len(got) != 1 || got[0] != "example.com" {
				t.Errorf("Domains() = %v, want [example.com]", got)
			}
		}()
	}
	wg.Wait()
}
