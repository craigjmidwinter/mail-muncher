package mcpserver

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/model"
	"github.com/craigjmidwinter/mail-muncher/internal/sink"
)

// fixture is a temporary archive: a real config, real dest directories, and
// messages written through internal/sink so the on-disk layout under test is
// the layout the pipeline actually produces. Nothing here hand-rolls a
// filename or a frontmatter block — a change to sink.Basename or to the
// frontmatter must show up as a failure here, not as a silent divergence.
type fixture struct {
	t          *testing.T
	root       string
	cfg        *config.Config
	domainFile string
}

// newFixture builds the config and returns an empty archive.
//
// Three rules, deliberately different from each other:
//
//	job-search   account personal, [eml markdown], driven by a from_domains_file
//	newsletters  account work,     [eml] only — the fallback path
//	everything   no account,       [markdown] only
func newFixture(t *testing.T) *fixture {
	t.Helper()

	root := t.TempDir()
	f := &fixture{t: t, root: root, domainFile: filepath.Join(root, "wanted-senders.txt")}

	body := fmt.Sprintf(`state_dir: %[1]s/state
accounts:
  - name: personal
    provider: gmail
    gmail:
      credentials_file: %[1]s/credentials.json
      token_file: %[1]s/token.json
  - name: work
    provider: gmail
    gmail:
      credentials_file: %[1]s/work-credentials.json
      token_file: %[1]s/work-token.json
rules:
  - name: job-search
    account: personal
    dest: %[1]s/mail/jobs
    formats: [eml, markdown]
    match:
      any:
        - from_domains_file: %[2]s
        - from_domains: [acme.example]
  - name: newsletters
    account: work
    dest: %[1]s/mail/news
    formats: [eml]
    match:
      from_domains: [news.example]
  - name: everything
    dest: %[1]s/mail/all
    formats: [markdown]
    match:
      subject_regex: "."
`, root, f.domainFile)

	path := filepath.Join(root, "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	cfg, err := config.Load(path)
	require.NoError(t, err)
	f.cfg = cfg
	return f
}

// rule returns the named rule, which must exist.
func (f *fixture) rule(name string) *config.Rule {
	f.t.Helper()
	for i := range f.cfg.Rules {
		if f.cfg.Rules[i].Name == name {
			return &f.cfg.Rules[i]
		}
	}
	f.t.Fatalf("no rule named %q in the fixture", name)
	return nil
}

// dest returns a rule's destination directory.
func (f *fixture) dest(rule string) string { return f.rule(rule).Dest }

// archive builds an Archive over the fixture config, with logs discarded.
func (f *fixture) archive() *Archive {
	f.t.Helper()
	a, err := NewArchive(f.cfg, discardLogger())
	require.NoError(f.t, err)
	return a
}

// server builds a Server over the fixture config. syncer may be nil.
func (f *fixture) server(syncer Syncer) *Server {
	f.t.Helper()
	s, err := New(Options{
		Config: f.cfg,
		Logger: discardLogger(),
		NewRunner: func(bool) (Syncer, error) {
			if syncer == nil {
				return nil, fmt.Errorf("this fixture has no runner")
			}
			return syncer, nil
		},
	})
	require.NoError(f.t, err)
	return s
}

// discardLogger is a logger that writes nowhere. No test may let log output
// reach stdout, which the stdio transport owns.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// message describes one message to store.
type message struct {
	id        string
	account   string
	rule      string
	subject   string
	from      string
	to        string
	date      time.Time
	threadID  string
	messageID string
	inReplyTo string
	body      string
	html      string
	labels    []string
	attach    string
}

// store writes one message through the rule's real sinks and returns the
// parsed message, so a test can assert against the same identity the archive
// will derive from the filename.
func (f *fixture) store(m message) *model.Message {
	f.t.Helper()

	rule := f.rule(m.rule)
	raw := m.raw(f.t)

	opts := []model.ParseOption{}
	if m.threadID != "" {
		opts = append(opts, model.WithThreadID(m.threadID))
	}
	msg, err := model.Parse(m.id, m.account, raw, m.date, m.labels, opts...)
	require.NoError(f.t, err)

	sinks, err := sink.ForRule(rule)
	require.NoError(f.t, err)
	require.NotEmpty(f.t, sinks)
	for _, s := range sinks {
		_, _, err := s.Store(msg, rule)
		require.NoError(f.t, err)
	}
	return msg
}

// raw renders the message as RFC822 bytes.
func (m message) raw(t *testing.T) []byte {
	t.Helper()

	messageID := m.messageID
	if messageID == "" {
		messageID = "<" + m.id + "@example.test>"
	}
	to := m.to
	if to == "" {
		to = "Me <me@example.test>"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", m.from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", m.subject)
	fmt.Fprintf(&b, "Date: %s\r\n", m.date.Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: %s\r\n", messageID)
	if m.inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\nReferences: %s\r\n", m.inReplyTo, m.inReplyTo)
	}
	b.WriteString("MIME-Version: 1.0\r\n")

	switch {
	case m.attach != "":
		const boundary = "mmboundary"
		fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)
		fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", boundary, m.body)
		fmt.Fprintf(&b, "--%s\r\nContent-Type: application/pdf\r\n"+
			"Content-Disposition: attachment; filename=%q\r\n"+
			"Content-Transfer-Encoding: base64\r\n\r\nSGVsbG8gYXR0YWNobWVudA==\r\n", boundary, m.attach)
		fmt.Fprintf(&b, "--%s--\r\n", boundary)
	case m.html != "":
		b.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
		b.WriteString(m.html)
	default:
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
		b.WriteString(m.body)
	}
	return []byte(b.String())
}

// day is a terse UTC timestamp for fixture dates.
func day(year int, month time.Month, dayOfMonth, hour int) time.Time {
	return time.Date(year, month, dayOfMonth, hour, 0, 0, 0, time.UTC)
}

// render marshals a tool result to JSON, which is how a test asserts about
// everything a caller would actually receive rather than field by field.
func render(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
