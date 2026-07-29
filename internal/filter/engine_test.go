package filter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/craigmidwinter/mail-muncher/internal/config"
	"github.com/craigmidwinter/mail-muncher/internal/model"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// rule builds a config.Rule whose match tree is parsed from src.
func rule(t *testing.T, name, account, src string) config.Rule {
	t.Helper()
	var n yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(src), &n))
	// config.Rule.Match is the mapping itself, not the document wrapper.
	require.Equal(t, yaml.DocumentNode, n.Kind)
	return config.Rule{Name: name, Account: account, Match: *n.Content[0], Dest: "/tmp/" + name}
}

func TestEngineFirstMatchWins(t *testing.T) {
	rules := []config.Rule{
		rule(t, "attachments", "", "{has_attachment: true}"),
		rule(t, "job-search", "", "{from_domains: [example.com]}"),
		rule(t, "catch-all", "", "{subject_regex: ''}"),
	}
	e, err := NewEngine(rules)
	require.NoError(t, err)
	require.Equal(t, 3, e.Len())

	// Matches rules 1 and 2; the earlier one wins.
	got := e.Evaluate(msg(nil))
	require.NotNil(t, got)
	require.Equal(t, "job-search", got.Name)

	// Matches all three; rule 0 wins.
	withAttachment := msg(func(m *model.Message) { m.Attachments = []model.Attachment{{Filename: "a.pdf"}} })
	require.Equal(t, "attachments", e.Evaluate(withAttachment).Name)

	// Matches only the catch-all.
	other := msg(func(m *model.Message) { m.From = addrs("someone@elsewhere.test") })
	require.Equal(t, "catch-all", e.Evaluate(other).Name)
}

func TestEngineReturnsNilWhenNothingMatches(t *testing.T) {
	e, err := NewEngine([]config.Rule{rule(t, "jobs", "", "{from_domains: [nope.test]}")})
	require.NoError(t, err)
	require.Nil(t, e.Evaluate(msg(nil)))
}

func TestEngineNoRules(t *testing.T) {
	e, err := NewEngine(nil)
	require.NoError(t, err)
	require.Equal(t, 0, e.Len())
	require.Nil(t, e.Evaluate(msg(nil)))
}

func TestEngineNilMessage(t *testing.T) {
	e, err := NewEngine([]config.Rule{rule(t, "any", "", "{subject_regex: ''}")})
	require.NoError(t, err)
	require.Nil(t, e.Evaluate(nil))
}

func TestEngineAccountScoping(t *testing.T) {
	rules := []config.Rule{
		rule(t, "work-only", "work", "{from_domains: [example.com]}"),
		rule(t, "personal-only", "personal", "{from_domains: [example.com]}"),
		rule(t, "all-accounts", "", "{from_domains: [example.com]}"),
	}
	e, err := NewEngine(rules)
	require.NoError(t, err)

	cases := map[string]string{
		"personal": "personal-only",
		"work":     "work-only",
		"other":    "all-accounts",
		"":         "all-accounts",
	}
	for account, want := range cases {
		got := e.Evaluate(msg(func(m *model.Message) { m.Account = account }))
		require.NotNil(t, got, account)
		require.Equal(t, want, got.Name, account)
	}
}

func TestEngineEvaluateReturnsTheConfiguredRule(t *testing.T) {
	// The pointer handed back must be the caller's rule, so the pipeline can
	// read Dest/Formats off it.
	rules := []config.Rule{rule(t, "jobs", "", "{from_domains: [example.com]}")}
	rules[0].Formats = []config.Format{config.FormatEML, config.FormatMarkdown}

	e, err := NewEngine(rules)
	require.NoError(t, err)

	got := e.Evaluate(msg(nil))
	require.Same(t, &rules[0], got)
	require.Equal(t, "/tmp/jobs", got.Dest)
	require.True(t, got.HasFormat(config.FormatMarkdown))
}

func TestNewEngineReportsTheOffendingRule(t *testing.T) {
	rules := []config.Rule{
		rule(t, "ok", "", "{has_attachment: true}"),
		rule(t, "broken", "", "{any: [{label: a}, {subject_regex: '('}]}"),
	}
	_, err := NewEngine(rules)
	require.Error(t, err)
	require.Contains(t, err.Error(), `rule "broken"`)
	require.Contains(t, err.Error(), "any[1].subject_regex")
	require.Contains(t, err.Error(), "invalid regular expression")
}

func TestEngineBeginCycleRefreshesDomainFiles(t *testing.T) {
	captureLogs(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "domains.txt")

	// The file does not exist yet: the rule matches nothing, no error.
	e, err := NewEngine([]config.Rule{rule(t, "jobs", "", "{from_domains_file: "+path+"}")})
	require.NoError(t, err)

	e.BeginCycle()
	require.Nil(t, e.Evaluate(msg(nil)))

	// The job-search tracker creates it between cycles.
	require.NoError(t, os.WriteFile(path, []byte("example.com\n"), 0o600))
	e.BeginCycle()
	got := e.Evaluate(msg(nil))
	require.NotNil(t, got)
	require.Equal(t, "jobs", got.Name)

	// ...and empties it again.
	require.NoError(t, os.WriteFile(path, []byte("\n"), 0o600))
	e.BeginCycle()
	require.Nil(t, e.Evaluate(msg(nil)))
}

func TestEngineSharesOneDomainFileCacheAcrossRules(t *testing.T) {
	captureLogs(t)
	path := filepath.Join(t.TempDir(), "domains.txt")
	require.NoError(t, os.WriteFile(path, []byte("example.com\n"), 0o600))

	e, err := NewEngine([]config.Rule{
		rule(t, "a", "", "{from_domains_file: "+path+"}"),
		rule(t, "b", "", "{from_domains_file: "+path+"}"),
	})
	require.NoError(t, err)
	require.NotNil(t, e.DomainFiles())

	e.BeginCycle()
	require.Equal(t, "a", e.Evaluate(msg(nil)).Name)
	require.Equal(t, []string{"example.com"}, e.DomainFiles().Domains(path))
}

func TestEngineFromLoadedConfig(t *testing.T) {
	// End to end through the real loader: `~` in a from_domains_file path is
	// expanded by config.Load, and the compiled engine routes accordingly.
	captureLogs(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(home, "domains.txt"), []byte("# tracked\nstripe.com\n"), 0o600))

	path := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(`
accounts:
  - name: personal
    gmail: {credentials_file: /c, token_file: /t}
rules:
  - name: job-search
    account: personal
    match:
      any:
        - from_domains_file: ~/domains.txt
        - subject_regex: "(?i)your application"
    dest: ~/Mail/job-search
  - name: receipts
    match:
      all:
        - from_domains: [stripe.com]
        - has_attachment: true
    dest: ~/Mail/receipts
`), 0o600))

	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.NoError(t, config.Validate(cfg).Err())

	e, err := NewEngine(cfg.Rules)
	require.NoError(t, err)
	e.BeginCycle()

	// Matched by subject on the first rule.
	require.Equal(t, "job-search", e.Evaluate(msg(nil)).Name)

	// Matched by the external file — even though rule 2 also matches, the
	// first rule in config order wins.
	receipt := msg(func(m *model.Message) {
		m.From = addrs("receipts@stripe.com")
		m.Subject = "Receipt"
		m.Attachments = []model.Attachment{{Filename: "r.pdf"}}
	})
	require.Equal(t, "job-search", e.Evaluate(receipt).Name)

	// Scoped to another account, so only the unscoped rule can claim it.
	receipt.Account = "work"
	require.Equal(t, "receipts", e.Evaluate(receipt).Name)
}

func TestMatchValidatorHookShape(t *testing.T) {
	// cmd/mail-muncher installs exactly this hook; keep it compiling here so a
	// signature change is caught by this package's tests too.
	config.MatchValidator = func(r *config.Rule) error {
		_, err := Compile(&r.Match)
		return err
	}
	t.Cleanup(func() { config.MatchValidator = nil })

	bad := rule(t, "broken", "", "{subject_regex: '('}")
	require.ErrorContains(t, config.MatchValidator(&bad), "invalid regular expression")

	good := rule(t, "ok", "", "{has_attachment: true}")
	require.NoError(t, config.MatchValidator(&good))
}

func TestEngineNilSafety(t *testing.T) {
	var e *Engine
	e.BeginCycle()
	require.Nil(t, e.Evaluate(msg(nil)))
	require.Equal(t, 0, e.Len())
	require.Nil(t, e.DomainFiles())
}
