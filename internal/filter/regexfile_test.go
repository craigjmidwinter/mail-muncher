package filter

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/model"
	"github.com/stretchr/testify/require"
)

// writePatterns drops a pattern list into a temp dir and returns its path.
func writePatterns(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "patterns.txt")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// patternSources renders compiled patterns back to their source text, so a
// table test can state what it expects without constructing regexps.
func patternSources(res []*regexp.Regexp) []string {
	if len(res) == 0 {
		return nil
	}
	out := make([]string, 0, len(res))
	for _, re := range res {
		out = append(out, re.String())
	}
	return out
}

// problemText renders validation findings as one blob a test can assert on.
func problemText(ps config.Problems) string {
	lines := make([]string, 0, len(ps))
	for _, p := range ps {
		lines = append(lines, p.String())
	}
	return strings.Join(lines, "\n")
}

func TestParsePatternList(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"plain", "wagepoint\nteamtailor\n", []string{"wagepoint", "teamtailor"}},
		{"blank lines and whitespace", "\n\n  wagepoint  \n\t\n", []string{"wagepoint"}},
		{"full line comments", "# companies\nwagepoint\n#  not this one\n", []string{"wagepoint"}},
		{"indented comment", "   # still a comment\nwagepoint\n", []string{"wagepoint"}},
		{
			// A `#` is a literal in RE2 and truncating there would silently
			// change what the pattern matches, so trailing comments are not a
			// thing in a pattern list.
			"hash mid-line is part of the pattern",
			"ticket#[0-9]+\n",
			[]string{"ticket#[0-9]+"},
		},
		{"case preserved", "WagePoint\n(?i)acme\n", []string{"WagePoint", "(?i)acme"}},
		{"duplicates collapse", "wagepoint\nwagepoint\n", []string{"wagepoint"}},
		{"no trailing newline", "wagepoint", []string{"wagepoint"}},
		{"crlf", "wagepoint\r\nteamtailor\r\n", []string{"wagepoint", "teamtailor"}},
		{"empty file", "", nil},
		{"comments only", "# nothing tracked right now\n", nil},
		{"anchored and escaped", `^no-?reply@acme\.com$`, []string{`^no-?reply@acme\.com$`}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLogs(t)
			got, err := parsePatternList([]byte(tc.body), "test")
			require.NoError(t, err)
			require.Equal(t, tc.want, patternSources(got))
		})
	}
}

// TestParsePatternListRefusesOverBroadPatterns is the guard that makes this
// predicate different from a domain list. A typo in a domain file matches
// nothing; a typo here can match every message in the mailbox.
func TestParsePatternListRefusesOverBroadPatterns(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
	}{
		{"empty", ""},
		{"dot star", ".*"},
		{"dot plus optional", ".?"},
		{"star alone", "a*"},
		{"caret alone", "^"},
		{"dollar alone", "$"},
		{"anchored empty", "^$"},
		{"empty group", "()"},
		{"flags only", "(?i)"},
		{"optional group", "(wagepoint)?"},
		{"alternation with empty branch", "wagepoint|"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := compileListPattern(tc.pattern)
			require.Error(t, err, "%q matches every address and must be refused", tc.pattern)
			require.Contains(t, err.Error(), "every message")
		})
	}
}

// TestFromRegexFileCatchAllMatchesNothing is the end-to-end version of the
// guard: a file whose only line is `.*` must not claim the mailbox.
func TestFromRegexFileCatchAllMatchesNothing(t *testing.T) {
	logs := captureLogs(t)
	path := writePatterns(t, ".*\n")

	files := NewFiles()
	m, err := Compile(node(t, "{from_regex_file: "+path+"}"), WithFiles(files))
	require.NoError(t, err)

	require.False(t, m.Match(msg(nil)), "a catch-all pattern must not match every message")
	require.False(t, m.Match(msg(func(x *model.Message) { x.From = addrs("anyone@anywhere.test") })))
	require.False(t, m.Match(msg(func(x *model.Message) { x.From = nil })))

	// And the refusal is reported, not silent.
	require.Contains(t, logs(), "pattern list entry rejected")
	degraded := files.Degraded()
	require.Len(t, degraded, 1)
	require.Contains(t, degraded[0].Err.Error(), "line 1")
	require.Contains(t, degraded[0].Err.Error(), "empty string")
}

// TestParsePatternListKeepsTheRestAfterOneBadLine: a generated file with one
// bad entry loses one subscription, not all of them.
func TestParsePatternListKeepsTheRestAfterOneBadLine(t *testing.T) {
	logs := captureLogs(t)
	body := "wagepoint\n[unclosed\nteamtailor\n.*\ngreenhouse\n"

	got, err := parsePatternList([]byte(body), "/patterns.txt")
	require.Equal(t, []string{"wagepoint", "teamtailor", "greenhouse"}, patternSources(got))

	require.Error(t, err, "the rejected lines must reach the caller as degradation")
	require.Contains(t, err.Error(), "2 of 5 patterns rejected")
	require.Contains(t, err.Error(), "line 2")
	require.Contains(t, err.Error(), "invalid regular expression")
	require.Contains(t, err.Error(), "line 4")
	require.Contains(t, err.Error(), "empty string")

	require.Contains(t, logs(), `line=2`)
	require.Contains(t, logs(), `pattern=[unclosed`)
}

// TestParsePatternListBoundsTheReportedLines keeps a file of prose from
// producing an unreadable degradation message.
func TestParsePatternListBoundsTheReportedLines(t *testing.T) {
	captureLogs(t)
	body := strings.Repeat("[unclosed\n", 12)

	got, err := parsePatternList([]byte(body), "/patterns.txt")
	require.Empty(t, got)
	require.Error(t, err)
	require.Contains(t, err.Error(), "12 of 12 patterns rejected")
	require.Contains(t, err.Error(), "and 7 more")
	require.Equal(t, maxReportedBadLines, strings.Count(err.Error(), "invalid regular expression"))
}

// TestParsePatternListLogsTheCount is the early-warning signal: a file that
// dropped from twelve patterns to one is visible in the run output rather than
// discovered by way of a full mailbox on disk.
func TestParsePatternListLogsTheCount(t *testing.T) {
	logs := captureLogs(t)
	_, err := parsePatternList([]byte("wagepoint\nteamtailor\n[unclosed\n"), "/patterns.txt")
	require.Error(t, err)
	require.Contains(t, logs(), "pattern list loaded")
	require.Contains(t, logs(), "patterns=2")
	require.Contains(t, logs(), "rejected=1")
}

func TestParsePatternListReportsTruncation(t *testing.T) {
	captureLogs(t)

	// 1 MiB is the scanner's line cap; a longer line stops the scan dead.
	body := "first\n" + strings.Repeat("a", 2*1024*1024) + "\nlast\n"

	got, err := parsePatternList([]byte(body), "/patterns.txt")
	require.Error(t, err, "a truncated scan must not be reported as a clean parse")
	require.ErrorIs(t, err, bufio.ErrTooLong)
	require.Equal(t, []string{"first"}, patternSources(got))
}

func TestFromRegexFileMatching(t *testing.T) {
	captureLogs(t)
	// The motivating case: a company whose sending host cannot be enumerated.
	path := writePatterns(t, "# tracked companies\nwagepoint\n(?i)^careers@acme\\.io$\n")

	files := NewFiles()
	m, err := Compile(node(t, "{from_regex_file: "+path+"}"), WithFiles(files))
	require.NoError(t, err)

	cases := []struct {
		name string
		from []string
		want bool
	}{
		{"ats subdomain", []string{"no-reply@wagepoint.teamtailor.com"}, true},
		{"company mail host", []string{"hr@mail.wagepoint.com"}, true},
		{"unrelated tld", []string{"notifications@wagepoint-hr.example"}, true},
		{"anchored pattern, exact", []string{"careers@acme.io"}, true},
		{"anchored pattern, case folded by (?i)", []string{"Careers@ACME.io"}, true},
		{"anchored pattern, not a substring match", []string{"x-careers@acme.io.evil.test"}, false},
		{"no match", []string{"receipts@stripe.com"}, false},
		{"one of several From addresses matches", []string{"a@stripe.com", "b@wagepoint.com"}, true},
		{"no From at all", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, m.Match(msg(func(x *model.Message) { x.From = addrs(tc.from...) })))
		})
	}
}

// TestFromRegexFileIsCaseSensitiveByDefault: a regex is case-sensitive by
// construction, and `(?i)` is how a caller asks otherwise. Lowercasing the file
// the way the domain parser does would silently change what a pattern matches.
func TestFromRegexFileIsCaseSensitiveByDefault(t *testing.T) {
	captureLogs(t)
	path := writePatterns(t, "WagePoint\n")

	m, err := Compile(node(t, "{from_regex_file: "+path+"}"), WithFiles(NewFiles()))
	require.NoError(t, err)

	require.True(t, m.Match(msg(func(x *model.Message) { x.From = addrs("hr@WagePoint.com") })))
	require.False(t, m.Match(msg(func(x *model.Message) { x.From = addrs("hr@wagepoint.com") })))
}

func TestFromRegexFileReloadsBetweenCycles(t *testing.T) {
	captureLogs(t)
	path := writePatterns(t, "wagepoint\n")

	files := NewFiles()
	m, err := Compile(node(t, "{from_regex_file: "+path+"}"), WithFiles(files))
	require.NoError(t, err)

	wagepoint := msg(func(x *model.Message) { x.From = addrs("hr@mail.wagepoint.com") })
	acme := msg(func(x *model.Message) { x.From = addrs("careers@acme.io") })

	files.BeginCycle()
	require.True(t, m.Match(wagepoint))
	require.False(t, m.Match(acme))

	// A mid-cycle edit is deliberately not visible: read once per cycle, not
	// once per message.
	require.NoError(t, os.WriteFile(path, []byte("wagepoint\nacme\n"), 0o600))
	require.False(t, m.Match(acme))

	// The generating program's edit lands on the next cycle.
	files.BeginCycle()
	require.True(t, m.Match(acme))

	// ...and so does its removal, without restarting mail-muncher.
	require.NoError(t, os.WriteFile(path, []byte("# nothing tracked right now\n"), 0o600))
	files.BeginCycle()
	require.False(t, m.Match(acme))
	require.False(t, m.Match(wagepoint))
}

func TestFromRegexFileMissingFileMatchesNothing(t *testing.T) {
	logs := captureLogs(t)
	path := filepath.Join(t.TempDir(), "not-created-yet.txt")

	files := NewFiles()
	m, err := Compile(node(t, "{from_regex_file: "+path+"}"), WithFiles(files))
	require.NoError(t, err, "a missing file must not fail compilation")

	require.False(t, m.Match(msg(nil)))
	require.False(t, m.Match(msg(nil)))
	require.Equal(t, 1, strings.Count(logs(), "pattern list unavailable"), "one warning per file per cycle")

	// Reported through the same degradation mechanism as a domain list, so
	// `on_degraded_filter` governs the cursor with no second policy.
	degraded := files.Degraded()
	require.Len(t, degraded, 1)
	require.Equal(t, path, degraded[0].Path)
	require.ErrorIs(t, degraded[0].Err, os.ErrNotExist)

	// Once the owning program writes it, the next cycle picks it up.
	require.NoError(t, os.WriteFile(path, []byte("example\n"), 0o600))
	files.BeginCycle()
	require.Empty(t, files.Degraded())
	require.True(t, m.Match(msg(nil)))
}

func TestFromRegexFileEmptyPathIsACompileError(t *testing.T) {
	_, err := Compile(node(t, "{from_regex_file: '  '}"))
	require.ErrorContains(t, err, "from_regex_file: path must not be empty")
}

// TestFromRegexFileCompilesPatternsOncePerRead: the compiled patterns handed to
// the matcher are the same objects on every message of a cycle, and different
// ones after BeginCycle.
func TestFromRegexFileCompilesPatternsOncePerRead(t *testing.T) {
	captureLogs(t)
	path := writePatterns(t, "wagepoint\n")
	files := NewFiles()

	first := files.Patterns(path)
	require.Len(t, first, 1)
	require.Same(t, first[0], files.Patterns(path)[0], "no recompilation within a cycle")

	files.BeginCycle()
	require.NotSame(t, first[0], files.Patterns(path)[0], "a new cycle recompiles")
}

// TestFilesShareOneDegradationMapAcrossKinds: a rule mixing both file-valued
// predicates reports both through the one mechanism the pipeline already reads.
func TestFilesShareOneDegradationMapAcrossKinds(t *testing.T) {
	captureLogs(t)
	dir := t.TempDir()
	domains := filepath.Join(dir, "domains.txt")
	patterns := filepath.Join(dir, "patterns.txt")

	e, err := NewEngine([]config.Rule{rule(t, "jobs", "",
		"{any: [{from_domains_file: "+domains+"}, {from_regex_file: "+patterns+"}]}")})
	require.NoError(t, err)

	require.Equal(t, []string{domains}, e.DomainFilePaths())
	require.Equal(t, []string{patterns}, e.RegexFilePaths())
	require.Equal(t, []FileRef{
		{Kind: FileKindDomains, Path: domains},
		{Kind: FileKindPatterns, Path: patterns},
	}, e.FileRefs())

	e.BeginCycle()
	e.Preload()

	got := e.Degraded()
	require.Len(t, got, 2, "both kinds report through the same mechanism")
	require.Equal(t, domains, got[0].Path)
	require.Equal(t, patterns, got[1].Path)

	require.Nil(t, e.Evaluate(msg(nil)), "and evaluation still just matches nothing")

	require.NoError(t, os.WriteFile(domains, []byte("nowhere.test\n"), 0o600))
	require.NoError(t, os.WriteFile(patterns, []byte("wagepoint\n"), 0o600))
	e.BeginCycle()
	e.Preload()
	require.Empty(t, e.Degraded())
	require.NotNil(t, e.Evaluate(msg(func(x *model.Message) { x.From = addrs("hr@wagepoint.io") })))
}

func TestFilesPatternsConcurrentUse(t *testing.T) {
	captureLogs(t)
	path := writePatterns(t, "wagepoint\n")
	files := NewFiles()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// require.* calls Goexit, which is not safe off the test
			// goroutine; report instead.
			if got := files.Patterns(path); len(got) != 1 || got[0].String() != "wagepoint" {
				t.Errorf("Patterns() = %v, want [wagepoint]", got)
			}
		}()
	}
	wg.Wait()
}

// TestValidateReportsUncompilablePatterns is the `validate` half: a file whose
// patterns cannot compile is reported before a cycle ever runs, as a warning —
// the file belongs to another program, so it must not stop mail-muncher.
func TestValidateReportsUncompilablePatterns(t *testing.T) {
	captureLogs(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.txt")
	require.NoError(t, os.WriteFile(bad, []byte("wagepoint\n[unclosed\n.*\n"), 0o600))
	missing := filepath.Join(dir, "not-yet.txt")

	cfg := &config.Config{
		StateDir: dir,
		Accounts: []config.Account{{
			Name: "personal", Provider: config.ProviderIMAP,
			IMAP: &config.IMAPConfig{Host: "h", Username: "u", PasswordCmd: "true"},
		}},
		Rules: []config.Rule{
			rule(t, "bad-patterns", "", "{from_regex_file: "+bad+"}"),
			rule(t, "not-written-yet", "", "{from_regex_file: "+missing+"}"),
		},
	}

	ps := config.Validate(cfg)
	require.NoError(t, ps.Err(), "an externally-owned file is never an error-severity problem")

	report := problemText(ps.Warnings())
	require.Contains(t, report, "rules[0].match.from_regex_file")
	require.Contains(t, report, "2 of 3 patterns rejected")
	require.Contains(t, report, "line 2")
	require.Contains(t, report, "line 3")

	require.Contains(t, report, "rules[1].match.from_regex_file")
	require.Contains(t, report, "file does not exist yet")
}

// TestValidateReportsAnEmptyPatternFile: a file that exists but lists nothing
// usable is worth saying out loud, because the rule silently matches nothing.
func TestValidateReportsAnEmptyPatternFile(t *testing.T) {
	captureLogs(t)
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.txt")
	require.NoError(t, os.WriteFile(empty, []byte("# nothing tracked right now\n"), 0o600))

	cfg := &config.Config{
		StateDir: dir,
		Accounts: []config.Account{{
			Name: "personal", Provider: config.ProviderIMAP,
			IMAP: &config.IMAPConfig{Host: "h", Username: "u", PasswordCmd: "true"},
		}},
		Rules: []config.Rule{rule(t, "nothing", "", "{from_regex_file: "+empty+"}")},
	}

	ps := config.Validate(cfg)
	require.NoError(t, ps.Err())
	require.Contains(t, problemText(ps.Warnings()), "lists no usable patterns")
}

// TestFromRegexFileThroughTheLoader: `~` expansion and the missing-file warning
// both hang off config's filePathPredicates set, and a predicate left out of it
// silently loses both.
func TestFromRegexFileThroughTheLoader(t *testing.T) {
	captureLogs(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(home, "companies.txt"), []byte("wagepoint\n"), 0o600))

	path := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(`
accounts:
  - name: personal
    provider: gmail
    gmail: {credentials_file: /c, token_file: /t}
rules:
  - name: job-search
    match:
      from_regex_file: ~/companies.txt
    dest: ~/Mail/job-search
`), 0o600))

	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, "companies.txt"), cfg.Rules[0].Match.Content[1].Value,
		"~ must be expanded, which only happens for predicates listed in config's filePathPredicates set")

	e, err := NewEngine(cfg.Rules)
	require.NoError(t, err)
	e.BeginCycle()
	require.Equal(t, "job-search", e.Evaluate(msg(func(x *model.Message) {
		x.From = addrs("no-reply@wagepoint.teamtailor.com")
	})).Name)
}
