package mcpserver

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestListRulesDescribesEveryRule: shape, ordering, and the derived fields.
func TestListRulesDescribesEveryRule(t *testing.T) {
	f := seedArchive(t)
	s := f.server(nil)

	got, err := s.listRules()
	require.NoError(t, err)
	require.Len(t, got.Rules, 3)

	jobs := got.Rules[0]
	require.Equal(t, "job-search", jobs.Name)
	require.Equal(t, "personal", jobs.Account)
	require.Equal(t, []string{"personal"}, jobs.Accounts)
	require.Equal(t, f.dest("job-search"), jobs.Dest)
	require.Equal(t, []string{"eml", "markdown"}, jobs.Formats)
	require.Equal(t, 3, jobs.StoredMessages)

	news := got.Rules[1]
	require.Equal(t, []string{"eml"}, news.Formats)
	require.Equal(t, 1, news.StoredMessages)
	require.Empty(t, news.DomainFiles, "this rule references no domain file")
	require.Empty(t, news.PatternFiles, "this rule references no pattern file")

	// A rule with no `account:` applies to every configured account, and the
	// resolved list says so rather than leaving the caller to infer it.
	all := got.Rules[2]
	require.Equal(t, "everything", all.Name)
	require.Empty(t, all.Account)
	require.Equal(t, []string{"personal", "work"}, all.Accounts)
	require.Zero(t, all.StoredMessages)
}

// TestListRulesResolvesDomainFileLive is the point of the tool: an agent that
// rewrote its wanted-senders file sees the new list on the very next call, with
// no restart and no cache in between.
func TestListRulesResolvesDomainFileLive(t *testing.T) {
	f := newFixture(t)
	s := f.server(nil)

	// Before the file exists: an empty list and a note, never an error.
	got, err := s.listRules()
	require.NoError(t, err)
	require.Len(t, got.Rules[0].DomainFiles, 1)

	missing := got.Rules[0].DomainFiles[0]
	require.Equal(t, f.domainFile, missing.Path)
	require.False(t, missing.Exists)
	require.NotNil(t, missing.Domains)
	require.Empty(t, missing.Domains)
	require.Zero(t, missing.Count)
	require.Contains(t, missing.Note, "does not exist")

	// The owning program writes the file.
	require.NoError(t, os.WriteFile(f.domainFile, []byte(
		"# senders I am waiting to hear from\n"+
			"@Acme.example.\n"+
			"beta.test\n"+
			"acme.example\n"+ // duplicate, collapses
			"\n"), 0o644))

	got, err = s.listRules()
	require.NoError(t, err)
	resolved := got.Rules[0].DomainFiles[0]
	require.True(t, resolved.Exists)
	require.Equal(t, []string{"acme.example", "beta.test"}, resolved.Domains,
		"normalized and de-duplicated exactly as the filter engine would")
	require.Equal(t, 2, resolved.Count)
	require.Empty(t, resolved.Note)
	require.False(t, resolved.ModifiedAt.IsZero())

	// And an edit is picked up immediately.
	require.NoError(t, os.WriteFile(f.domainFile, []byte("newco.test\n"), 0o644))
	got, err = s.listRules()
	require.NoError(t, err)
	require.Equal(t, []string{"newco.test"}, got.Rules[0].DomainFiles[0].Domains)

	// An emptied file is still not an error.
	require.NoError(t, os.WriteFile(f.domainFile, []byte("# nothing yet\n"), 0o644))
	got, err = s.listRules()
	require.NoError(t, err)
	empty := got.Rules[0].DomainFiles[0]
	require.True(t, empty.Exists)
	require.Empty(t, empty.Domains)
	require.Contains(t, empty.Note, "no usable domains")
}

// TestResolveDomainFileDirectory: a path that is a directory is a note, not a
// crash.
func TestResolveDomainFileDirectory(t *testing.T) {
	dir := t.TempDir()
	got := resolveDomainFile(dir)
	require.False(t, got.Exists)
	require.Empty(t, got.Domains)
	require.Contains(t, got.Note, "directory")
}

// TestListRulesResolvesPatternFileLive is TestListRulesResolvesDomainFileLive
// for the other externally-owned file. Same guarantee — read at call time, a
// missing file is a note rather than an error — plus the one thing a pattern
// list has that a domain list cannot: lines the breadth guards refuse.
func TestListRulesResolvesPatternFileLive(t *testing.T) {
	f := newFixture(t)
	s := f.server(nil)

	// Before the file exists: an empty list and a note, never an error.
	got, err := s.listRules()
	require.NoError(t, err)
	require.Len(t, got.Rules[0].PatternFiles, 1)

	missing := got.Rules[0].PatternFiles[0]
	require.Equal(t, f.patternFile, missing.Path)
	require.False(t, missing.Exists)
	require.NotNil(t, missing.Patterns)
	require.Empty(t, missing.Patterns)
	require.Zero(t, missing.Count)
	require.Zero(t, missing.Rejected)
	require.Contains(t, missing.Note, "does not exist")

	// The owning program writes the file.
	require.NoError(t, os.WriteFile(f.patternFile, []byte(
		"# tracked companies\n"+
			"wagepoint\n"+
			`(?i)^careers@acme\.io$`+"\n"+
			"teamtailor\\.com$\n"+
			"wagepoint\n"+ // duplicate, collapses
			"\n"), 0o644))

	got, err = s.listRules()
	require.NoError(t, err)
	resolved := got.Rules[0].PatternFiles[0]
	require.True(t, resolved.Exists)
	require.Equal(t, []string{"wagepoint", `(?i)^careers@acme\.io$`, `teamtailor\.com$`}, resolved.Patterns,
		"the source text of every pattern in force, de-duplicated exactly as the filter engine would")
	require.Equal(t, 3, resolved.Count)
	require.Zero(t, resolved.Rejected)
	require.Empty(t, resolved.Note)
	require.False(t, resolved.ModifiedAt.IsZero())

	// And an edit is picked up immediately.
	require.NoError(t, os.WriteFile(f.patternFile, []byte("newco\n"), 0o644))
	got, err = s.listRules()
	require.NoError(t, err)
	require.Equal(t, []string{"newco"}, got.Rules[0].PatternFiles[0].Patterns)

	// An emptied file is still not an error.
	require.NoError(t, os.WriteFile(f.patternFile, []byte("# nothing yet\n"), 0o644))
	got, err = s.listRules()
	require.NoError(t, err)
	empty := got.Rules[0].PatternFiles[0]
	require.True(t, empty.Exists)
	require.Empty(t, empty.Patterns)
	require.Zero(t, empty.Rejected)
	require.Contains(t, empty.Note, "no usable patterns")
}

// TestListRulesCountsRejectedPatterns is why `rejected` exists. A generator
// that emitted mostly junk must be visible as a number an agent can report,
// not inferred from a list that is quietly shorter than the file.
func TestListRulesCountsRejectedPatterns(t *testing.T) {
	f := newFixture(t)
	s := f.server(nil)

	require.NoError(t, os.WriteFile(f.patternFile, []byte(
		"# tracked companies\n"+
			"wagepoint\n"+
			"x?\n"+ // matches the empty string: refused
			"teamtailor\\.com$\n"+
			"foo(\n"+ // does not compile: refused
			".*\n"+ // matches everything: refused
			"wagepoint\n"+ // duplicate of a loaded pattern: not a rejection
			"x?\n"), 0o644)) // duplicate of a refused line: refused again

	got, err := s.listRules()
	require.NoError(t, err)
	info := got.Rules[0].PatternFiles[0]

	require.True(t, info.Exists)
	require.Equal(t, []string{"wagepoint", `teamtailor\.com$`}, info.Patterns)
	require.Equal(t, 2, info.Count)
	require.Equal(t, 4, info.Rejected,
		"three bad lines plus the repeat of one; the repeat of a good line is not a rejection")
	require.Contains(t, info.Note, "4 lines rejected")
	require.Contains(t, info.Note, "2 patterns")

	// The rejected lines' own text is never returned: the count and the note
	// are the whole report, so a file of prose cannot bloat a tool result.
	blob := render(t, got)
	require.NotContains(t, blob, "foo(")
	require.NotContains(t, blob, `.*`)

	// Every line refused is still an answer, not an error.
	require.NoError(t, os.WriteFile(f.patternFile, []byte("^\n"), 0o644))
	got, err = s.listRules()
	require.NoError(t, err)
	none := got.Rules[0].PatternFiles[0]
	require.True(t, none.Exists)
	require.Empty(t, none.Patterns)
	require.Zero(t, none.Count)
	require.Equal(t, 1, none.Rejected)
	require.Contains(t, none.Note, "1 line rejected")
	require.Contains(t, none.Note, "matches nothing")
}

// TestListRulesSurfacesBothFileKinds: a rule whose subscription is split
// across a domain list and a pattern list reports both. Reporting one of them
// is worse than reporting neither — it reads as a complete answer.
func TestListRulesSurfacesBothFileKinds(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.WriteFile(f.domainFile, []byte("acme.example\n"), 0o644))
	require.NoError(t, os.WriteFile(f.patternFile, []byte("wagepoint\n"), 0o644))

	got, err := f.server(nil).listRules()
	require.NoError(t, err)

	jobs := got.Rules[0]
	require.Len(t, jobs.DomainFiles, 1)
	require.Len(t, jobs.PatternFiles, 1)
	require.Equal(t, []string{"acme.example"}, jobs.DomainFiles[0].Domains)
	require.Equal(t, []string{"wagepoint"}, jobs.PatternFiles[0].Patterns)

	blob := render(t, got)
	require.Contains(t, blob, `"domain_files"`)
	require.Contains(t, blob, `"pattern_files"`,
		"pattern_files is additive; domain_files keeps the name every existing caller reads")
}

// TestResolvePatternFileDirectory: a path that is a directory is a note, not a
// crash.
func TestResolvePatternFileDirectory(t *testing.T) {
	dir := t.TempDir()
	got := resolvePatternFile(dir)
	require.False(t, got.Exists)
	require.Empty(t, got.Patterns)
	require.Zero(t, got.Rejected)
	require.Contains(t, got.Note, "directory")
}

// TestDomainFilePaths finds every file-valued predicate wherever it sits in a
// match tree, including under combinators, negation and YAML anchors, and
// keeps the two kinds apart.
func TestDomainFilePaths(t *testing.T) {
	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(`
all:
  - any:
      - from_domains_file: /lists/wanted.txt
      - from_regex_file: /lists/companies.txt
      - from_domains: [acme.example]
  - not:
      from_domains_file: /lists/blocked.txt
  - from_domains_file: /lists/wanted.txt
  - from_regex_file: /lists/companies.txt
`), &node))

	require.Equal(t, []string{"/lists/wanted.txt", "/lists/blocked.txt"}, domainFilePaths(&node),
		"in document order, without duplicates")
	require.Equal(t, []string{"/lists/companies.txt"}, regexFilePaths(&node),
		"pattern files come back in their own bucket, not mixed into the domain lists")

	var none yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("from_domains: [acme.example]\n"), &none))
	require.Empty(t, domainFilePaths(&none))
	require.Empty(t, regexFilePaths(&none))

	require.Empty(t, domainFilePaths(nil))
	require.Empty(t, regexFilePaths(nil))
	require.Empty(t, domainFilePaths(&yaml.Node{}))
	require.Empty(t, regexFilePaths(&yaml.Node{}))
}

// TestMatchFilePathsSameFileBothKinds: one file read by two parsers is two
// subscriptions, so deduplication is per predicate and not per path.
func TestMatchFilePathsSameFileBothKinds(t *testing.T) {
	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(`
any:
  - from_domains_file: /lists/shared.txt
  - from_regex_file: /lists/shared.txt
`), &node))

	domains, patterns := matchFilePaths(&node)
	require.Equal(t, []string{"/lists/shared.txt"}, domains)
	require.Equal(t, []string{"/lists/shared.txt"}, patterns)
}

// TestDomainFilePathsSurvivesAliasCycle: a YAML alias can point backwards, so
// the walk must terminate on a tree that refers to itself.
func TestDomainFilePathsSurvivesAliasCycle(t *testing.T) {
	inner := &yaml.Node{Kind: yaml.MappingNode}
	alias := &yaml.Node{Kind: yaml.AliasNode, Alias: inner}
	inner.Content = []*yaml.Node{
		{Kind: yaml.ScalarNode, Value: "any"},
		{Kind: yaml.SequenceNode, Content: []*yaml.Node{alias}},
	}

	done := make(chan []string, 1)
	go func() { done <- domainFilePaths(inner) }()
	select {
	case got := <-done:
		require.Empty(t, got)
	case <-time.After(5 * time.Second):
		t.Fatal("the match-tree walk did not terminate")
	}
}

// TestListRulesExposesNoSecrets: the tool names the dest and the agent's own
// domain and pattern files, and nothing else about the configuration.
func TestListRulesExposesNoSecrets(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.WriteFile(f.domainFile, []byte("acme.example\n"), 0o644))
	require.NoError(t, os.WriteFile(f.patternFile, []byte("wagepoint\n"), 0o644))

	got, err := f.server(nil).listRules()
	require.NoError(t, err)

	blob := render(t, got)
	require.Contains(t, blob, f.domainFile, "the agent's own list is exactly what it asked for")
	require.Contains(t, blob, f.patternFile, "and so is its pattern list")
	require.Contains(t, blob, f.dest("job-search"))

	for _, secret := range []string{
		filepath.Join(f.root, "credentials.json"),
		filepath.Join(f.root, "token.json"),
		f.cfg.Path,
		f.cfg.StateDir,
		"gmail",
		"initial_lookback",
	} {
		require.NotContains(t, blob, secret)
	}
}
