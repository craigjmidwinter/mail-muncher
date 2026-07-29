package imap_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/pipeline"
)

// TestPipelineEndToEndOverIMAP is the test of the provider seam, not of this
// package: a real IMAP server on one end, real .eml and .md files on the other,
// and in between a pipeline, filter engine and sink that were written before
// this provider existed and were not modified for it.
//
// It goes through pipeline.DefaultProviderFactory — the one dispatch point
// outside internal/provider that a new backend is allowed to touch — rather
// than constructing the provider directly, so the wiring is covered too.
func TestPipelineEndToEndOverIMAP(t *testing.T) {
	srv := newTestServer(t)
	srv.CreateMailbox("Archive")
	srv.Append("INBOX", "your application to Acme", time.Now().Add(-2*time.Hour))
	srv.Append("INBOX", "lunch", time.Now().Add(-time.Hour))
	srv.Append("Archive", "old thread", time.Now().Add(-30*time.Minute))

	dest := t.TempDir()
	cfg := &config.Config{
		StateDir: t.TempDir(),
		Accounts: []config.Account{{
			Name:     testAccount,
			Provider: config.ProviderIMAP,
			IMAP: &config.IMAPConfig{
				Host:        srv.Host,
				Port:        srv.Port,
				Username:    testUsername,
				PasswordCmd: passwordCmd,
				Mailboxes:   []string{"INBOX", "Archive"},
				TLS:         boolPtr(false),
			},
		}},
		Rules: []config.Rule{
			{
				Name:    "applications",
				Account: testAccount,
				Match:   matchNode(t, `subject_regex: "(?i)your application"`),
				Dest:    filepath.Join(dest, "applications"),
				Formats: []config.Format{config.FormatEML, config.FormatMarkdown},
			},
			{
				// The mailbox name doubles as the label, exactly as a Gmail
				// label would — this rule is provider-agnostic.
				Name:    "archive-folder",
				Match:   matchNode(t, `label: Archive`),
				Dest:    filepath.Join(dest, "archive"),
				Formats: []config.Format{config.FormatEML},
			},
		},
	}
	require.Empty(t, config.Validate(cfg).Errors())

	runner, err := pipeline.NewRunner(pipeline.Options{Config: cfg})
	require.NoError(t, err)

	manifests, err := runner.Cycle(context.Background())
	require.NoError(t, err)
	require.Len(t, manifests, 1)

	m := manifests[0]
	assert.Equal(t, 3, m.Summary.Fetched)
	assert.Equal(t, 2, m.Summary.Matched, "`lunch` matches no rule")
	assert.Equal(t, 3, m.Summary.Stored, "one eml+md pair and one lone eml")
	assert.Zero(t, m.Summary.ParseErrors)
	assert.Zero(t, m.Summary.SinkErrors)
	assert.Zero(t, m.Summary.Quarantined)

	assert.Len(t, filesUnder(t, filepath.Join(dest, "applications")), 2)
	assert.Len(t, filesUnder(t, filepath.Join(dest, "archive")), 1)

	// Re-running is idempotent even though the state advanced: the cursor
	// skips the mail, and anything the cursor did not skip would be skipped
	// again at the write.
	manifests, err = runner.Cycle(context.Background())
	require.NoError(t, err)
	assert.Zero(t, manifests[0].Summary.Fetched, "an unchanged mailbox must cost nothing")

	// And new mail lands on the next cycle without re-reading the old.
	srv.Append("INBOX", "your application to Globex", time.Now())
	manifests, err = runner.Cycle(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, manifests[0].Summary.Fetched)
	assert.Equal(t, 2, manifests[0].Summary.Stored)
	assert.Len(t, filesUnder(t, filepath.Join(dest, "applications")), 4)

	// Nothing along the way marked a single message read.
	assert.Equal(t, uint32(3), srv.Unseen("INBOX"))
	assert.Equal(t, uint32(1), srv.Unseen("Archive"))

	// The threading id came from the References chain, not from a provider
	// that has no such thing to give.
	md := readFirst(t, filepath.Join(dest, "applications"), ".md")
	assert.Contains(t, md, "thread_id_source: self",
		"IMAP has no conversation id; model.Parse must synthesize one")
}

func boolPtr(b bool) *bool { return &b }

func matchNode(t *testing.T, body string) yaml.Node {
	t.Helper()
	var n yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(body), &n))
	require.NotEmpty(t, n.Content)
	return *n.Content[0]
}

func filesUnder(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	require.NoError(t, err)
	return out
}

func readFirst(t *testing.T, root, ext string) string {
	t.Helper()
	for _, path := range filesUnder(t, root) {
		if strings.HasSuffix(path, ext) {
			b, err := os.ReadFile(path)
			require.NoError(t, err)
			return string(b)
		}
	}
	t.Fatalf("no %s file under %s", ext, root)
	return ""
}
