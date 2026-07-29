package filter

import (
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/craigjmidwinter/mail-muncher/internal/model"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// node parses a YAML fragment into the kind of node config hands to Compile.
func node(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var n yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(src), &n))
	return &n
}

// addrs turns bare addr-specs into mail.Address values.
func addrs(specs ...string) []mail.Address {
	out := make([]mail.Address, 0, len(specs))
	for _, s := range specs {
		out = append(out, mail.Address{Address: s})
	}
	return out
}

// testClock is the fixed "now" the age predicates are tested against.
var testClock = func() time.Time { return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC) }

// msg builds a message with sensible defaults; fn tweaks it.
func msg(fn func(*model.Message)) *model.Message {
	m := &model.Message{
		ID:      "m1",
		Account: "personal",
		From:    addrs("careers@mail.example.com"),
		To:      addrs("me@home.test"),
		Subject: "Your application",
		Date:    testClock().Add(-24 * time.Hour),
		Headers: textproto.MIMEHeader{},
	}
	if fn != nil {
		fn(m)
	}
	return m
}

func TestCompilePredicates(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		msg  *model.Message
		want bool
	}{
		// from_domains
		{"from_domains exact", "{from_domains: [mail.example.com]}", msg(nil), true},
		{"from_domains subdomain", "{from_domains: [example.com]}", msg(nil), true},
		{"from_domains case insensitive", "{from_domains: [EXAMPLE.COM]}", msg(nil), true},
		{"from_domains leading at", "{from_domains: ['@example.com']}", msg(nil), true},
		{"from_domains no match", "{from_domains: [other.com]}", msg(nil), false},
		{"from_domains suffix is not subdomain", "{from_domains: [ample.com]}", msg(nil), false},
		{"from_domains second entry", "{from_domains: [a.test, example.com]}", msg(nil), true},
		{"from_domains no from header", "{from_domains: [example.com]}", msg(func(m *model.Message) { m.From = nil }), false},

		// from_regex
		{"from_regex matches addr", "{from_regex: '^careers@'}", msg(nil), true},
		{"from_regex ignores display name", "{from_regex: 'Recruiting'}",
			msg(func(m *model.Message) {
				m.From = []mail.Address{{Name: "Recruiting", Address: "careers@mail.example.com"}}
			}), false},
		{"from_regex no match", "{from_regex: 'billing@'}", msg(nil), false},
		{"from_regex any of several", "{from_regex: 'second@'}",
			msg(func(m *model.Message) { m.From = addrs("first@a.test", "second@b.test") }), true},

		// to_regex
		{"to_regex matches to", "{to_regex: 'me@home\\.test'}", msg(nil), true},
		{"to_regex matches cc", "{to_regex: 'boss@work\\.test'}",
			msg(func(m *model.Message) { m.Cc = addrs("boss@work.test") }), true},
		{"to_regex no match", "{to_regex: 'nobody@'}", msg(nil), false},

		// subject_regex
		{"subject_regex case insensitive flag", "{subject_regex: '(?i)your APPLICATION'}", msg(nil), true},
		{"subject_regex no match", "{subject_regex: '^Invoice'}", msg(nil), false},
		{"subject_regex empty subject", "{subject_regex: 'x'}", msg(func(m *model.Message) { m.Subject = "" }), false},

		// header
		{"header matches", "{header: {name: X-Mailer, regex: 'greenhouse'}}",
			msg(func(m *model.Message) { m.Headers.Set("X-Mailer", "greenhouse-io") }), true},
		{"header name is case insensitive", "{header: {name: x-mailer, regex: 'greenhouse'}}",
			msg(func(m *model.Message) { m.Headers.Set("X-Mailer", "greenhouse-io") }), true},
		{"header matches any value", "{header: {name: Received, regex: 'by mx2'}}",
			msg(func(m *model.Message) {
				m.Headers.Add("Received", "by mx1.example.com")
				m.Headers.Add("Received", "by mx2.example.com")
			}), true},
		{"header absent", "{header: {name: X-Nope, regex: '.'}}", msg(nil), false},

		// has_attachment
		{"has_attachment true with attachment", "{has_attachment: true}",
			msg(func(m *model.Message) { m.Attachments = []model.Attachment{{Filename: "a.pdf"}} }), true},
		{"has_attachment true without", "{has_attachment: true}", msg(nil), false},
		{"has_attachment false without", "{has_attachment: false}", msg(nil), true},
		{"has_attachment false with attachment", "{has_attachment: false}",
			msg(func(m *model.Message) { m.Attachments = []model.Attachment{{Filename: "a.pdf"}} }), false},

		// label
		{"label exact", "{label: Jobs}", msg(func(m *model.Message) { m.Labels = []string{"INBOX", "Jobs"} }), true},
		{"label is case sensitive", "{label: jobs}", msg(func(m *model.Message) { m.Labels = []string{"Jobs"} }), false},
		{"label absent", "{label: Jobs}", msg(nil), false},

		// older_than / newer_than
		{"older_than matches old message", "{older_than: 12h}", msg(nil), true},
		{"older_than does not match recent", "{older_than: 720h}", msg(nil), false},
		{"newer_than matches recent", "{newer_than: 720h}", msg(nil), true},
		{"newer_than does not match old", "{newer_than: 12h}", msg(nil), false},
		{"age predicates ignore undated messages", "{older_than: 1h}",
			msg(func(m *model.Message) { m.Date = time.Time{} }), false},

		// combinators
		{"all both true", "{all: [{has_attachment: false}, {from_domains: [example.com]}]}", msg(nil), true},
		{"all one false", "{all: [{has_attachment: true}, {from_domains: [example.com]}]}", msg(nil), false},
		{"any one true", "{any: [{has_attachment: true}, {from_domains: [example.com]}]}", msg(nil), true},
		{"any none true", "{any: [{has_attachment: true}, {label: Jobs}]}", msg(nil), false},
		{"not inverts", "{not: {has_attachment: true}}", msg(nil), true},
		{"nested all any not", `
all:
  - any:
      - from_domains: [nope.test]
      - subject_regex: '(?i)^your application$'
  - not:
      any:
        - has_attachment: true
        - label: Spam
`, msg(nil), true},
		{"nested tree fails on the not branch", `
all:
  - any:
      - from_domains: [example.com]
  - not:
      any:
        - has_attachment: true
`, msg(func(m *model.Message) { m.Attachments = []model.Attachment{{Filename: "a.pdf"}} }), false},
		{"deeply nested", "{not: {not: {not: {from_domains: [example.com]}}}}", msg(nil), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Compile(node(t, tc.yaml), WithClock(testClock))
			require.NoError(t, err)
			require.Equal(t, tc.want, m.Match(tc.msg))
		})
	}
}

func TestCompileErrors(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		// contains are substrings the error must mention: the location inside
		// the tree and what is wrong with it.
		contains []string
	}{
		{"unknown key", "{from_domian: [a.test]}", []string{"unknown match key", "from_domian", "from_domains"}},
		{"unknown nested key", "{all: [{subject_rgx: x}]}", []string{"all[0]", "unknown match key"}},
		{"multiple keys", "{from_domains: [a.test], has_attachment: true}",
			[]string{"exactly one key", "got 2", "from_domains, has_attachment"}},
		{"multiple keys nested", "{any: [{label: a, has_attachment: true}]}", []string{"any[0]", "exactly one key"}},
		{"empty mapping", "{}", []string{"empty mapping"}},
		{"null match", "~", []string{"empty"}},
		{"scalar match", "hello", []string{"expected a mapping"}},
		{"list match", "[{label: a}]", []string{"expected a mapping", "a list"}},
		{"empty all", "{all: []}", []string{"all", "at least one match node"}},
		{"empty any", "{any: []}", []string{"any", "at least one match node"}},
		{"all is not a list", "{all: {label: a}}", []string{"all", "expected a list of match nodes"}},
		{"not without a node", "{not: ~}", []string{"not", "empty"}},
		{"invalid regex", "{subject_regex: '('}", []string{"subject_regex", "invalid regular expression"}},
		{"invalid nested regex", "{any: [{label: a}, {from_regex: '[z-a]'}]}",
			[]string{"any[1].from_regex", "invalid regular expression"}},
		{"regex is a list", "{subject_regex: [a, b]}", []string{"subject_regex", "expected a single value"}},
		{"invalid duration", "{older_than: 3 fortnights}", []string{"older_than", "invalid duration"}},
		{"negative duration", "{newer_than: -1h}", []string{"newer_than", "must be positive"}},
		{"empty domain list", "{from_domains: []}", []string{"from_domains", "at least one domain"}},
		{"domain list is a scalar", "{from_domains: example.com}", []string{"from_domains", "expected a list of domains"}},
		{"empty domain entry", "{from_domains: ['', a.test]}", []string{"from_domains[0]", "must not be empty"}},
		{"empty domains file", "{from_domains_file: ''}", []string{"from_domains_file", "must not be empty"}},
		{"empty label", "{label: ''}", []string{"label", "must not be empty"}},
		{"has_attachment is not a bool", "{has_attachment: maybe}", []string{"has_attachment", "true or false"}},
		{"header is a scalar", "{header: X-Foo}", []string{"header", "expected a mapping"}},
		{"header missing regex", "{header: {name: X-Foo}}", []string{"header", "missing key regex"}},
		{"header missing name", "{header: {regex: x}}", []string{"header", "missing key name"}},
		{"header empty name", "{header: {name: '', regex: x}}", []string{"header.name", "must not be empty"}},
		{"header unknown key", "{header: {name: X-Foo, regex: x, mode: fuzzy}}", []string{"header", `unknown key "mode"`}},
		{"header invalid regex", "{header: {name: X-Foo, regex: '('}}", []string{"header.regex", "invalid regular expression"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(node(t, tc.yaml))
			require.Error(t, err)
			for _, want := range tc.contains {
				require.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestCompileNilNode(t *testing.T) {
	_, err := Compile(nil)
	require.ErrorContains(t, err, "empty")
}

func TestCompileZeroNode(t *testing.T) {
	// A rule with no `match:` key at all decodes to the zero yaml.Node.
	var zero yaml.Node
	_, err := Compile(&zero)
	require.ErrorContains(t, err, "empty")
}

func TestCompileFollowsAnchorsAndAliases(t *testing.T) {
	// A shared sub-tree written once with an anchor must compile through the
	// alias rather than blowing up on an unexpected node kind.
	src := `
all:
  - &jobs {subject_regex: '(?i)application'}
  - not: {has_attachment: true}
  - *jobs
`
	m, err := Compile(node(t, src))
	require.NoError(t, err)
	require.True(t, m.Match(msg(nil)))
}

func TestCompileReusesTheDomainFileCache(t *testing.T) {
	// Two predicates pointing at the same file must share one read per cycle.
	dir := t.TempDir()
	path := filepath.Join(dir, "domains.txt")
	require.NoError(t, os.WriteFile(path, []byte("example.com\n"), 0o600))

	files := NewDomainFiles()
	m, err := Compile(node(t, "{any: [{from_domains_file: "+path+"}, {from_domains_file: "+path+"}]}"),
		WithDomainFiles(files))
	require.NoError(t, err)
	require.True(t, m.Match(msg(nil)))

	// Removing the file mid-cycle changes nothing: the cache holds until the
	// next BeginCycle.
	require.NoError(t, os.Remove(path))
	require.True(t, m.Match(msg(nil)))

	files.BeginCycle()
	require.False(t, m.Match(msg(nil)))
}

func TestNormalizeDomain(t *testing.T) {
	cases := map[string]string{
		"  Example.COM ": "example.com",
		"@example.com":   "example.com",
		"example.com.":   "example.com",
		"@ Example.com":  "example.com",
		"":               "",
		"@":              "",
	}
	for in, want := range cases {
		require.Equal(t, want, NormalizeDomain(in), in)
	}
}
