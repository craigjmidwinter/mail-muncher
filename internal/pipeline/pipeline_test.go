package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/provider"
	"github.com/craigjmidwinter/mail-muncher/internal/state"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// fixedNow is the clock every test pins to, so manifests and state files are
// byte-comparable.
var fixedNow = time.Date(2026, 7, 28, 9, 15, 0, 0, time.UTC)

// rawMessage builds a minimal but real RFC822 message. The pipeline runs it
// through the actual model parser, so it has to be genuinely well-formed.
func rawMessage(from, subject, body string) []byte {
	return []byte("From: " + from + "\r\n" +
		"To: craig@example.com\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: Tue, 28 Jul 2026 09:15:00 +0000\r\n" +
		"Message-ID: <" + subject + "@mail.example.com>\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" + body + "\r\n")
}

// matchNode parses a YAML match tree into the yaml.Node config.Rule holds.
func matchNode(t *testing.T, src string) yaml.Node {
	t.Helper()
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(src), &doc))
	require.NotEmpty(t, doc.Content, "match tree %q decoded to nothing", src)
	return *doc.Content[0]
}

// testConfig builds a config rooted in t.TempDir with one account and the
// given rules, and returns it alongside the destination directory.
func testConfig(t *testing.T, rules ...config.Rule) (*config.Config, string) {
	t.Helper()
	root := t.TempDir()
	dest := filepath.Join(root, "mail")

	for i := range rules {
		if rules[i].Dest == "" {
			rules[i].Dest = dest
		}
		if len(rules[i].Formats) == 0 {
			rules[i].Formats = []config.Format{config.FormatEML}
		}
	}

	return &config.Config{
		StateDir: filepath.Join(root, "state"),
		Accounts: []config.Account{{Name: "personal", Provider: config.ProviderGmail}},
		Rules:    rules,
		Path:     filepath.Join(root, "config.yml"),
	}, dest
}

// newTestRunner wires a Runner onto a fake provider and a pinned clock.
func newTestRunner(t *testing.T, cfg *config.Config, fake *provider.Fake, dryRun bool) *Runner {
	t.Helper()
	r, err := NewRunner(Options{
		Config: cfg,
		DryRun: dryRun,
		Providers: func(context.Context, *config.Account) (provider.Provider, error) {
			return fake, nil
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return fixedNow },
	})
	require.NoError(t, err)
	return r
}

// walkFiles lists every regular file under dir, relative to it.
func walkFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	require.NoError(t, err)
	return out
}

// TestCycleStoresMatchesEndToEnd is the integration test the task calls for:
// fake provider, temp dirs, the real filter engine and the real sinks, then
// assertions against the filesystem and the state file.
func TestCycleStoresMatchesEndToEnd(t *testing.T) {
	cfg, dest := testConfig(t, config.Rule{
		Name:    "job-search",
		Match:   matchNode(t, "{from_domains: [acme.com]}"),
		Formats: []config.Format{config.FormatEML, config.FormatMarkdown},
	})

	fake := provider.NewFake("gmail",
		provider.RawMessage{
			ID:           "msg-match",
			Raw:          rawMessage("recruiting@acme.com", "Re: your application", "We would like to talk."),
			InternalDate: fixedNow,
			Labels:       []string{"INBOX"},
		},
		provider.RawMessage{
			ID:           "msg-nomatch",
			Raw:          rawMessage("newsletter@example.org", "Weekly digest", "Nothing to see."),
			InternalDate: fixedNow,
		},
	)
	fake.Now = fixedNow
	fake.NextHistoryID = 991234

	runner := newTestRunner(t, cfg, fake, false)

	manifests, err := runner.Cycle(context.Background())
	require.NoError(t, err)
	require.Len(t, manifests, 1)

	m := manifests[0]
	require.Equal(t, "personal", m.Account)
	require.False(t, m.DryRun)
	require.Empty(t, m.Error)

	// Two fetched, one matched, and the match fanned out to two formats.
	require.Equal(t, Summary{Fetched: 2, Matched: 1, Stored: 2}, m.Summary)
	require.Len(t, m.Stored, 2)
	require.Empty(t, m.Skipped)

	// One entry per (message x format), sharing an id, in the rule's format
	// order.
	require.Equal(t, config.FormatEML, m.Stored[0].Format)
	require.Equal(t, config.FormatMarkdown, m.Stored[1].Format)
	require.Equal(t, m.Stored[0].ID, m.Stored[1].ID)
	require.Equal(t, "job-search", m.Stored[0].Rule)
	require.Equal(t, "recruiting@acme.com", m.Stored[0].From)
	require.Equal(t, "Re: your application", m.Stored[0].Subject)
	require.False(t, m.Stored[0].HasAttachment)

	// The files the manifest names really exist, with real content.
	for _, entry := range m.Stored {
		data, err := os.ReadFile(entry.Path)
		require.NoError(t, err, "manifest names a path that is not on disk: %s", entry.Path)
		require.NotEmpty(t, data)
	}
	require.Contains(t, m.Stored[0].Path, filepath.Join(dest, "2026", "07"))

	// Exactly the two renderings, nothing for the unmatched message.
	require.Len(t, walkFiles(t, dest), 2)

	// State advanced, and the stored message is in the seen set.
	st, err := state.NewStore(cfg.StateDir).Load("personal")
	require.NoError(t, err)
	require.Equal(t, uint64(991234), st.HistoryID)
	require.Equal(t, fixedNow, st.LastSyncTime.UTC())
	require.True(t, st.Seen("msg-match"), "stored message should be marked seen")
}

// TestCycleIsIdempotent runs the same cycle twice: the second run matches the
// same message but writes nothing, which is the whole idempotency story.
func TestCycleIsIdempotent(t *testing.T) {
	cfg, dest := testConfig(t, config.Rule{
		Name:  "acme",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})

	fake := provider.NewFake("gmail", provider.RawMessage{
		ID:           "msg-1",
		Raw:          rawMessage("recruiting@acme.com", "Hello", "hi"),
		InternalDate: fixedNow,
	})
	fake.Now = fixedNow

	runner := newTestRunner(t, cfg, fake, false)

	first, err := runner.Cycle(context.Background())
	require.NoError(t, err)
	require.Equal(t, Summary{Fetched: 1, Matched: 1, Stored: 1}, first[0].Summary)

	second, err := runner.Cycle(context.Background())
	require.NoError(t, err)

	// matched=1 stored=0 skipped=1 — the README's idempotency signature.
	require.Equal(t, Summary{Fetched: 1, Matched: 1, Stored: 0, Skipped: 1}, second[0].Summary)
	require.Empty(t, second[0].Stored)
	require.Len(t, second[0].Skipped, 1)
	require.Equal(t, first[0].Stored[0].Path, second[0].Skipped[0].Path)

	// Still exactly one file on disk.
	require.Len(t, walkFiles(t, dest), 1)
}

// TestDryRunWritesNothing is the acceptance criterion that matters most: a
// dry run must report precise paths and leave the filesystem untouched.
func TestDryRunWritesNothing(t *testing.T) {
	cfg, dest := testConfig(t, config.Rule{
		Name:    "acme",
		Match:   matchNode(t, "{from_domains: [acme.com]}"),
		Formats: []config.Format{config.FormatEML, config.FormatMarkdown},
	})

	fake := provider.NewFake("gmail", provider.RawMessage{
		ID:           "msg-1",
		Raw:          rawMessage("recruiting@acme.com", "Hello", "hi"),
		InternalDate: fixedNow,
	})
	fake.Now = fixedNow
	fake.NextHistoryID = 42

	runner := newTestRunner(t, cfg, fake, true)
	require.True(t, runner.DryRun())

	manifests, err := runner.Cycle(context.Background())
	require.NoError(t, err)

	m := manifests[0]
	require.True(t, m.DryRun, "dry_run must be set on the manifest")
	require.Equal(t, Summary{Fetched: 1, Matched: 1, Stored: 2}, m.Summary)
	require.Len(t, m.Stored, 2)

	// Not one byte written to the destination tree.
	require.Empty(t, walkFiles(t, dest), "dry run wrote files")
	for _, entry := range m.Stored {
		_, err := os.Stat(entry.Path)
		require.True(t, os.IsNotExist(err), "dry run created %s", entry.Path)
	}

	// And no state saved: the cursor must not advance.
	st, err := state.NewStore(cfg.StateDir).Load("personal")
	require.NoError(t, err)
	require.Zero(t, st.HistoryID, "dry run advanced the sync cursor")
	require.True(t, st.LastSyncTime.IsZero(), "dry run saved state")

	// A real run afterwards must still store everything the dry run promised.
	real := newTestRunner(t, cfg, fake, false)
	realManifests, err := real.Cycle(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, realManifests[0].Summary.Stored)

	dryPaths := []string{m.Stored[0].Path, m.Stored[1].Path}
	realPaths := []string{realManifests[0].Stored[0].Path, realManifests[0].Stored[1].Path}
	require.Equal(t, dryPaths, realPaths, "dry run must predict the exact paths a real run writes")
}

// TestParseErrorsDoNotAbortCycle: a malformed message is counted and skipped,
// and the messages after it still land.
func TestParseErrorsDoNotAbortCycle(t *testing.T) {
	cfg, _ := testConfig(t, config.Rule{
		Name:  "acme",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})

	fake := provider.NewFake("gmail",
		provider.RawMessage{ID: "broken", Raw: nil, InternalDate: fixedNow},
		provider.RawMessage{
			ID:           "good",
			Raw:          rawMessage("recruiting@acme.com", "Hello", "hi"),
			InternalDate: fixedNow,
		},
	)
	fake.Now = fixedNow

	manifests, err := newTestRunner(t, cfg, fake, false).Cycle(context.Background())
	require.NoError(t, err, "a parse failure must not fail the run")

	m := manifests[0]
	require.Equal(t, 2, m.Summary.Fetched)
	require.Equal(t, 1, m.Summary.ParseErrors)
	require.Equal(t, 1, m.Summary.Stored, "the message after the broken one still stored")
}

// TestStateSavedWhenNothingMatched: the cursor advances even on an empty
// cycle, or every run would re-scan the same window forever.
func TestStateSavedWhenNothingMatched(t *testing.T) {
	cfg, _ := testConfig(t, config.Rule{
		Name:  "acme",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})

	fake := provider.NewFake("gmail", provider.RawMessage{
		ID:           "msg-1",
		Raw:          rawMessage("someone@elsewhere.org", "Hello", "hi"),
		InternalDate: fixedNow,
	})
	fake.Now = fixedNow
	fake.NextHistoryID = 777

	manifests, err := newTestRunner(t, cfg, fake, false).Cycle(context.Background())
	require.NoError(t, err)
	require.Equal(t, Summary{Fetched: 1}, manifests[0].Summary)
	require.Empty(t, manifests[0].Stored)

	st, err := state.NewStore(cfg.StateDir).Load("personal")
	require.NoError(t, err)
	require.Equal(t, uint64(777), st.HistoryID, "cursor must advance even with zero matches")
}

// TestProviderErrorYieldsPartialManifest: a fetch that dies partway still
// reports the files that reached disk, and does not advance the cursor.
func TestProviderErrorYieldsPartialManifest(t *testing.T) {
	cfg, dest := testConfig(t, config.Rule{
		Name:  "acme",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})

	fake := provider.NewFake("gmail",
		provider.RawMessage{ID: "a", Raw: rawMessage("one@acme.com", "First", "hi"), InternalDate: fixedNow},
		provider.RawMessage{ID: "b", Raw: rawMessage("two@acme.com", "Second", "hi"), InternalDate: fixedNow},
		provider.RawMessage{ID: "c", Raw: rawMessage("three@acme.com", "Third", "hi"), InternalDate: fixedNow},
	)
	fake.Now = fixedNow
	fake.FailEarly = true
	fake.FailAfter = 2
	fake.FetchErr = errors.New("gmail: API unreachable")

	manifests, err := newTestRunner(t, cfg, fake, false).Cycle(context.Background())

	require.Error(t, err)
	require.True(t, IsProviderError(err), "a fetch failure must grade as a provider error")
	require.Equal(t, ExitProvider, ExitCode(err))

	require.Len(t, manifests, 1)
	m := manifests[0]

	// The two that made it through are still reported and still on disk.
	require.Len(t, m.Stored, 2, "partial manifest must list what landed before the failure")
	require.Equal(t, 2, m.Summary.Stored)
	require.Contains(t, m.Error, "API unreachable")
	require.Len(t, walkFiles(t, dest), 2)
	for _, entry := range m.Stored {
		require.FileExists(t, entry.Path)
	}

	// The cursor did not advance: the next run re-fetches, and the sinks skip.
	st, err2 := state.NewStore(cfg.StateDir).Load("personal")
	require.NoError(t, err2)
	require.True(t, st.LastSyncTime.IsZero(), "a failed fetch must not save state")
}

// TestProviderConstructionErrorGradesAsProviderError covers the auth path:
// a missing token is exit 2, not exit 1, and the actionable text survives.
func TestProviderConstructionErrorGradesAsProviderError(t *testing.T) {
	cfg, _ := testConfig(t, config.Rule{
		Name:  "acme",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})

	authErr := errors.New("gmail: no cached token: run `mail-muncher auth --account personal`")
	runner, err := NewRunner(Options{
		Config: cfg,
		Providers: func(context.Context, *config.Account) (provider.Provider, error) {
			return nil, authErr
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return fixedNow },
	})
	require.NoError(t, err)

	manifests, cycleErr := runner.Cycle(context.Background())
	require.Error(t, cycleErr)
	require.Equal(t, ExitProvider, ExitCode(cycleErr))
	require.ErrorIs(t, cycleErr, authErr, "the actionable auth text must survive verbatim")

	require.Len(t, manifests, 1, "a manifest is emitted even when the provider never built")
	require.Contains(t, manifests[0].Error, "mail-muncher auth --account personal")
}

// TestSinkErrorsDoNotAbortCycle: one unwritable destination is counted, and
// the other rule's messages still land.
func TestSinkErrorsDoNotAbortCycle(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "good")

	// A regular file where a rule wants a directory: every write under it
	// fails with ENOTDIR.
	blocked := filepath.Join(root, "blocked")
	require.NoError(t, os.WriteFile(blocked, []byte("not a directory"), 0o644))

	cfg := &config.Config{
		StateDir: filepath.Join(root, "state"),
		Accounts: []config.Account{{Name: "personal", Provider: config.ProviderGmail}},
		Rules: []config.Rule{
			{
				Name:    "blocked",
				Match:   matchNode(t, "{from_domains: [blocked.com]}"),
				Dest:    blocked,
				Formats: []config.Format{config.FormatEML},
			},
			{
				Name:    "good",
				Match:   matchNode(t, "{from_domains: [acme.com]}"),
				Dest:    good,
				Formats: []config.Format{config.FormatEML},
			},
		},
	}

	fake := provider.NewFake("gmail",
		provider.RawMessage{ID: "a", Raw: rawMessage("x@blocked.com", "Nope", "hi"), InternalDate: fixedNow},
		provider.RawMessage{ID: "b", Raw: rawMessage("y@acme.com", "Yes", "hi"), InternalDate: fixedNow},
	)
	fake.Now = fixedNow

	manifests, err := newTestRunner(t, cfg, fake, false).Cycle(context.Background())
	require.NoError(t, err, "a sink failure is message-level and must not fail the run")

	m := manifests[0]
	require.Equal(t, 2, m.Summary.Matched)
	require.Equal(t, 1, m.Summary.SinkErrors)
	require.Equal(t, 1, m.Summary.Stored)
	require.Len(t, walkFiles(t, good), 1)
}

// TestCycleLockContention: a second cycle while the lock is held returns
// state.ErrLocked, which grades to exit 3.
func TestCycleLockContention(t *testing.T) {
	cfg, _ := testConfig(t, config.Rule{
		Name:  "acme",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})

	// Hold the lock the way a concurrently running instance would.
	holder := state.NewStore(cfg.StateDir)
	lock, err := holder.TryLock()
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Unlock() })

	fake := provider.NewFake("gmail")
	manifests, cycleErr := newTestRunner(t, cfg, fake, false).Cycle(context.Background())

	require.Error(t, cycleErr)
	require.ErrorIs(t, cycleErr, state.ErrLocked)
	require.Equal(t, ExitLocked, ExitCode(cycleErr))
	require.Nil(t, manifests)
	require.Zero(t, fake.Calls, "a locked-out cycle must not fetch anything")

	// Once released, the same runner succeeds.
	require.NoError(t, lock.Unlock())
	_, err = newTestRunner(t, cfg, fake, false).Cycle(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, fake.Calls)
}

// TestCycleIsReusableAcrossTicks is the daemon's contract: the same Runner is
// called repeatedly, and each Cycle re-reads externally-managed domain files.
func TestCycleIsReusableAcrossTicks(t *testing.T) {
	root := t.TempDir()
	domains := filepath.Join(root, "wanted.txt")
	require.NoError(t, os.WriteFile(domains, []byte("first.com\n"), 0o644))

	cfg := &config.Config{
		StateDir: filepath.Join(root, "state"),
		Accounts: []config.Account{{Name: "personal", Provider: config.ProviderGmail}},
		Rules: []config.Rule{{
			Name:    "wanted",
			Match:   matchNode(t, "{from_domains_file: "+domains+"}"),
			Dest:    filepath.Join(root, "mail"),
			Formats: []config.Format{config.FormatEML},
		}},
	}

	fake := provider.NewFake("gmail",
		provider.RawMessage{ID: "a", Raw: rawMessage("x@first.com", "One", "hi"), InternalDate: fixedNow},
		provider.RawMessage{ID: "b", Raw: rawMessage("y@second.com", "Two", "hi"), InternalDate: fixedNow},
	)
	fake.Now = fixedNow

	runner := newTestRunner(t, cfg, fake, false)

	first, err := runner.Cycle(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, first[0].Summary.Matched, "only first.com matches initially")

	// An agent rewrites its wanted-senders file between ticks.
	require.NoError(t, os.WriteFile(domains, []byte("first.com\nsecond.com\n"), 0o644))

	second, err := runner.Cycle(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, second[0].Summary.Matched, "BeginCycle must re-read the domain file")
	require.Equal(t, 1, second[0].Summary.Stored, "the newly matching message stored")
	require.Equal(t, 1, second[0].Summary.Skipped, "the already-stored one skipped")

	require.Equal(t, 2, fake.Calls, "the runner drove two independent cycles")
}

// TestCycleCoversEveryAccount: one broken account must not stop the others.
func TestCycleCoversEveryAccount(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		StateDir: filepath.Join(root, "state"),
		Accounts: []config.Account{
			{Name: "broken", Provider: config.ProviderGmail},
			{Name: "working", Provider: config.ProviderGmail},
		},
		Rules: []config.Rule{{
			Name:    "acme",
			Match:   matchNode(t, "{from_domains: [acme.com]}"),
			Dest:    filepath.Join(root, "mail"),
			Formats: []config.Format{config.FormatEML},
		}},
	}

	working := provider.NewFake("gmail", provider.RawMessage{
		ID: "a", Raw: rawMessage("x@acme.com", "Hello", "hi"), InternalDate: fixedNow,
	})
	working.Now = fixedNow

	runner, err := NewRunner(Options{
		Config: cfg,
		Providers: func(_ context.Context, account *config.Account) (provider.Provider, error) {
			if account.Name == "broken" {
				return nil, errors.New("gmail: stored token was rejected")
			}
			return working, nil
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return fixedNow },
	})
	require.NoError(t, err)

	manifests, cycleErr := runner.Cycle(context.Background())
	require.Error(t, cycleErr)
	require.Equal(t, ExitProvider, ExitCode(cycleErr))

	require.Len(t, manifests, 2, "one manifest per account, in config order")
	require.Equal(t, "broken", manifests[0].Account)
	require.NotEmpty(t, manifests[0].Error)
	require.Equal(t, "working", manifests[1].Account)
	require.Empty(t, manifests[1].Error)
	require.Equal(t, 1, manifests[1].Summary.Stored, "the healthy account still ran")
}

// TestRuleAccountScoping: a rule bound to one account never claims another's
// mail.
func TestRuleAccountScoping(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		StateDir: filepath.Join(root, "state"),
		Accounts: []config.Account{{Name: "personal"}, {Name: "work"}},
		Rules: []config.Rule{{
			Name:    "work-only",
			Account: "work",
			Match:   matchNode(t, "{from_domains: [acme.com]}"),
			Dest:    filepath.Join(root, "mail"),
			Formats: []config.Format{config.FormatEML},
		}},
	}

	msg := provider.RawMessage{ID: "a", Raw: rawMessage("x@acme.com", "Hello", "hi"), InternalDate: fixedNow}
	fakes := map[string]*provider.Fake{
		"personal": provider.NewFake("gmail", msg),
		"work":     provider.NewFake("gmail", msg),
	}
	for _, f := range fakes {
		f.Now = fixedNow
	}

	runner, err := NewRunner(Options{
		Config: cfg,
		Providers: func(_ context.Context, a *config.Account) (provider.Provider, error) {
			return fakes[a.Name], nil
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return fixedNow },
	})
	require.NoError(t, err)

	manifests, err := runner.Cycle(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, manifests[0].Summary.Matched, "personal is out of the rule's scope")
	require.Equal(t, 1, manifests[1].Summary.Matched, "work is in scope")
}

// TestNewRunnerRejectsBadConfig covers the two startup failures.
func TestNewRunnerRejectsBadConfig(t *testing.T) {
	t.Run("no config", func(t *testing.T) {
		_, err := NewRunner(Options{})
		require.Error(t, err)
	})

	t.Run("uncompilable rule", func(t *testing.T) {
		cfg, _ := testConfig(t, config.Rule{
			Name:  "bad",
			Match: matchNode(t, "{no_such_predicate: nope}"),
		})
		_, err := NewRunner(Options{Config: cfg})
		require.Error(t, err)
		require.Contains(t, err.Error(), "bad")
	})

	t.Run("unknown format", func(t *testing.T) {
		cfg, _ := testConfig(t, config.Rule{
			Name:    "bad-format",
			Match:   matchNode(t, "{from_domains: [acme.com]}"),
			Formats: []config.Format{"pdf"},
		})
		_, err := NewRunner(Options{Config: cfg})
		require.Error(t, err)
	})
}

// TestDebugLoggingRecordsEvaluateDecisions: the spec requires a per-message
// rule decision at debug level, since it is how a user debugs a rule that will
// not fire.
func TestDebugLoggingRecordsEvaluateDecisions(t *testing.T) {
	cfg, _ := testConfig(t, config.Rule{
		Name:  "acme",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})

	fake := provider.NewFake("gmail",
		provider.RawMessage{ID: "a", Raw: rawMessage("x@acme.com", "Matches", "hi"), InternalDate: fixedNow},
		provider.RawMessage{ID: "b", Raw: rawMessage("y@other.org", "Misses", "hi"), InternalDate: fixedNow},
	)
	fake.Now = fixedNow

	var logs strings.Builder
	runner, err := NewRunner(Options{
		Config: cfg,
		Providers: func(context.Context, *config.Account) (provider.Provider, error) {
			return fake, nil
		},
		Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:    func() time.Time { return fixedNow },
	})
	require.NoError(t, err)

	_, err = runner.Cycle(context.Background())
	require.NoError(t, err)

	out := logs.String()
	require.Contains(t, out, `rule=acme`, "the winning rule name must be logged")
	require.Contains(t, out, `rule="no match"`, "an unmatched message must be logged as no match")
	require.Contains(t, out, "id=a")
	require.Contains(t, out, "id=b")
}

// TestDefaultProviderFactoryRejectsUnknownProvider keeps the seam honest: an
// unrecognized provider fails at the factory, not somewhere downstream.
func TestDefaultProviderFactoryRejectsUnknownProvider(t *testing.T) {
	_, err := DefaultProviderFactory(context.Background(), &config.Account{Name: "x", Provider: "pigeon"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "pigeon")

	_, err = DefaultProviderFactory(context.Background(), nil)
	require.Error(t, err)
}

// TestDefaultProviderFactoryBuildsIMAP: the whole cost of a second provider,
// at this layer, is one case in the switch. Nothing else in this package —
// and nothing in internal/filter or internal/sink — knows IMAP exists.
func TestDefaultProviderFactoryBuildsIMAP(t *testing.T) {
	p, err := DefaultProviderFactory(context.Background(), &config.Account{
		Name:     "fastmail",
		Provider: config.ProviderIMAP,
		IMAP: &config.IMAPConfig{
			Host:        "imap.example.test",
			Username:    "someone@example.test",
			PasswordCmd: "true",
		},
	})
	require.NoError(t, err)
	require.Equal(t, config.ProviderIMAP, p.Name())

	// An imap account with no `imap:` block is a config error that Validate
	// catches; the factory refuses it too rather than dialling nowhere.
	_, err = DefaultProviderFactory(context.Background(), &config.Account{Name: "x", Provider: config.ProviderIMAP})
	require.Error(t, err)
}

// TestMatchValidatorIsInstalledByPipeline: importing internal/pipeline must be
// enough to make config.LoadAndValidate reject a malformed match tree — the
// CLI's validate.go must not be the only thing that installs the hook.
func TestMatchValidatorIsInstalledByPipeline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	body := fmt.Sprintf(`
state_dir: %s
accounts:
  - name: personal
    provider: gmail
    gmail:
      credentials_file: %s/creds.json
      token_file: %s/token.json
rules:
  - name: bad
    dest: %s/mail
    match:
      no_such_predicate: nope
`, dir, dir, dir, dir)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	_, problems, err := config.LoadAndValidate(path)
	require.Error(t, err, "a malformed match tree must fail validation")

	var found bool
	for _, p := range problems.Errors() {
		if strings.Contains(p.Message, "no_such_predicate") {
			found = true
		}
	}
	require.True(t, found, "expected the compile failure to be reported as a problem, got %v", problems)
}

// TestManifestJSONShape golden-checks the manifest an agent parses. The shape
// is API surface: the MCP server returns this same struct.
func TestManifestJSONShape(t *testing.T) {
	cfg, _ := testConfig(t, config.Rule{
		Name:    "job-search",
		Match:   matchNode(t, "{from_domains: [acme.com]}"),
		Formats: []config.Format{config.FormatEML, config.FormatMarkdown},
	})

	fake := provider.NewFake("gmail", provider.RawMessage{
		ID:           "18f2a",
		ThreadID:     "18f29",
		Raw:          rawMessage("recruiting@acme.com", "Re: your application", "We would like to talk."),
		InternalDate: fixedNow,
	})
	fake.Now = fixedNow

	manifests, err := newTestRunner(t, cfg, fake, false).Cycle(context.Background())
	require.NoError(t, err)

	var buf strings.Builder
	require.NoError(t, WriteJSON(&buf, manifests[0]))

	// One object, one line, newline-terminated: the NDJSON stream contract.
	require.True(t, strings.HasSuffix(buf.String(), "\n"))
	require.Equal(t, 1, strings.Count(buf.String(), "\n"), "each manifest must be exactly one line")

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(buf.String()), &got))

	// Top level: exactly the documented keys, and dry_run absent on a real run.
	require.ElementsMatch(t,
		[]string{"account", "started_at", "duration_ms", "stored", "skipped", "summary"},
		keysOf(got))

	require.Equal(t, "personal", got["account"])
	require.Equal(t, "2026-07-28T09:15:00Z", got["started_at"])

	summary := got["summary"].(map[string]any)
	require.ElementsMatch(t,
		[]string{"fetched", "matched", "stored", "skipped", "parse_errors", "sink_errors", "quarantined"},
		keysOf(summary))
	require.Equal(t, float64(1), summary["fetched"])
	require.Equal(t, float64(1), summary["matched"])
	require.Equal(t, float64(2), summary["stored"])

	stored := got["stored"].([]any)
	require.Len(t, stored, 2, "one entry per (message x format)")

	entry := stored[0].(map[string]any)
	require.ElementsMatch(t,
		[]string{"path", "format", "rule", "id", "message_id", "thread_id", "thread_id_source",
			"from", "subject", "date", "has_attachment"},
		keysOf(entry))
	require.Equal(t, "eml", entry["format"])
	require.Equal(t, "job-search", entry["rule"])
	require.Equal(t, "18f2a", entry["id"])
	require.Equal(t, "recruiting@acme.com", entry["from"])
	require.Equal(t, "Re: your application", entry["subject"])
	require.Equal(t, "2026-07-28T09:15:00Z", entry["date"])
	require.Equal(t, false, entry["has_attachment"])
	require.Contains(t, entry["message_id"], "@mail.example.com")

	// The provider's thread id reaches the manifest verbatim, labelled as the
	// provider's, so an agent can group a cycle's deliveries without opening a
	// file and knows the grouping is authoritative.
	require.Equal(t, "18f29", entry["thread_id"])
	require.Equal(t, "provider", entry["thread_id_source"])

	// The markdown entry shares the id and the thread.
	require.Equal(t, "markdown", stored[1].(map[string]any)["format"])
	require.Equal(t, entry["id"], stored[1].(map[string]any)["id"])
	require.Equal(t, entry["thread_id"], stored[1].(map[string]any)["thread_id"])

	// Empty collections marshal as [], never null — an agent can range over
	// them without a nil check.
	require.Equal(t, []any{}, got["skipped"])
}

// TestManifestGroupsAReplyChainByThread is the whole point of the thread id: a
// consumer asking "everything about the Acme thread" gets one join key for a
// conversation, even from a provider that supplies no thread id of its own.
func TestManifestGroupsAReplyChainByThread(t *testing.T) {
	cfg, _ := testConfig(t, config.Rule{
		Name:  "job-search",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})

	// A three-message chain, threaded the way a mail client threads it, with no
	// provider thread id anywhere — the IMAP-shaped case.
	chain := func(id, subject, inReplyTo, references string) []byte {
		raw := "From: recruiting@acme.com\r\n" +
			"To: craig@example.com\r\n" +
			"Subject: " + subject + "\r\n" +
			"Date: Tue, 28 Jul 2026 09:15:00 +0000\r\n" +
			"Message-ID: " + id + "\r\n"
		if inReplyTo != "" {
			raw += "In-Reply-To: " + inReplyTo + "\r\n"
		}
		if references != "" {
			raw += "References: " + references + "\r\n"
		}
		return []byte(raw + "\r\n" + subject + " body\r\n")
	}

	root := "<thread-001@acme.com>"
	second := "<thread-002@acme.com>"
	fake := provider.NewFake("imap",
		provider.RawMessage{ID: "m1", Raw: chain(root, "Acme application", "", ""), InternalDate: fixedNow},
		provider.RawMessage{ID: "m2", Raw: chain(second, "Re: Acme application", root, root), InternalDate: fixedNow},
		provider.RawMessage{ID: "m3", Raw: chain("<thread-003@acme.com>", "Re: Acme application",
			second, root+" "+second), InternalDate: fixedNow},
	)
	fake.Now = fixedNow

	manifests, err := newTestRunner(t, cfg, fake, false).Cycle(context.Background())
	require.NoError(t, err)
	require.Len(t, manifests[0].Stored, 3)

	for _, e := range manifests[0].Stored {
		require.Equal(t, root, e.ThreadID, "every message of the chain shares one key: %s", e.ID)
	}
	// The root is keyed by itself; the replies by the root of their chain. Both
	// are reconstructions, and say so.
	require.Equal(t, "self", manifests[0].Stored[0].ThreadIDSource)
	require.Equal(t, "references", manifests[0].Stored[1].ThreadIDSource)
	require.Equal(t, "references", manifests[0].Stored[2].ThreadIDSource)
}

// TestManifestJSONDryRunFlag: --dry-run --json sets the top-level flag.
func TestManifestJSONDryRunFlag(t *testing.T) {
	cfg, _ := testConfig(t, config.Rule{
		Name:  "acme",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})
	fake := provider.NewFake("gmail", provider.RawMessage{
		ID: "a", Raw: rawMessage("x@acme.com", "Hi", "hi"), InternalDate: fixedNow,
	})
	fake.Now = fixedNow

	manifests, err := newTestRunner(t, cfg, fake, true).Cycle(context.Background())
	require.NoError(t, err)

	var buf strings.Builder
	require.NoError(t, WriteJSON(&buf, manifests[0]))

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(buf.String()), &got))
	require.Equal(t, true, got["dry_run"])
}

// TestManifestJSONPartialOnProviderError: an agent must still learn about the
// files that exist, plus why the cycle stopped.
func TestManifestJSONPartialOnProviderError(t *testing.T) {
	cfg, _ := testConfig(t, config.Rule{
		Name:  "acme",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})

	fake := provider.NewFake("gmail",
		provider.RawMessage{ID: "a", Raw: rawMessage("x@acme.com", "First", "hi"), InternalDate: fixedNow},
		provider.RawMessage{ID: "b", Raw: rawMessage("y@acme.com", "Second", "hi"), InternalDate: fixedNow},
	)
	fake.Now = fixedNow
	fake.FailEarly = true
	fake.FailAfter = 1
	fake.FetchErr = errors.New("gmail: API unreachable")

	manifests, cycleErr := newTestRunner(t, cfg, fake, false).Cycle(context.Background())
	require.Error(t, cycleErr)

	var buf strings.Builder
	require.NoError(t, WriteJSON(&buf, manifests[0]))

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(buf.String()), &got))
	require.Len(t, got["stored"].([]any), 1, "the file that landed is still reported")
	require.Contains(t, got["error"], "API unreachable")
}

// TestSummaryString pins the published field list.
func TestSummaryString(t *testing.T) {
	s := Summary{Fetched: 128, Matched: 6, Stored: 6, Skipped: 0, ParseErrors: 0, SinkErrors: 0}
	require.Equal(t, "fetched=128 matched=6 stored=6 skipped=0 parse_errors=0 sink_errors=0 quarantined=0", s.String())

	m := Manifest{Account: "personal", Summary: s, DurationMS: 4100}
	require.Equal(t,
		"personal: fetched=128 matched=6 stored=6 skipped=0 parse_errors=0 sink_errors=0 quarantined=0 duration=4.1s",
		m.Line())

	m.DryRun = true
	require.Contains(t, m.Line(), "personal (dry-run): fetched=128")

	// A held cycle says so on the label, where the dry-run marker lives, so the
	// published counter list stays contiguous.
	held := Manifest{Account: "personal", Degraded: true, StateHeld: true}
	require.Contains(t, held.Line(), "personal (degraded, state held): fetched=0")
	stopped := Manifest{Account: "personal", Stopped: true}
	require.Contains(t, stopped.Line(), "personal (stopped): fetched=0")

	// `vanished=` appears only when a message was listed and then found to be
	// gone, so the published field list is unchanged for every other run.
	vanished := Summary{Fetched: 2, Matched: 2, Stored: 2, Vanished: 1}
	require.Equal(t,
		"fetched=2 matched=2 stored=2 skipped=0 parse_errors=0 sink_errors=0 quarantined=0 vanished=1",
		vanished.String())
	require.Contains(t, Manifest{Account: "personal", Summary: vanished}.Line(), "quarantined=0 vanished=1 duration=")
}

// TestSummaryAdd covers the daemon's multi-cycle totals.
func TestSummaryAdd(t *testing.T) {
	s := Summary{Fetched: 1, Matched: 2, Stored: 3, Skipped: 4, ParseErrors: 5, SinkErrors: 6, Quarantined: 7, Vanished: 8}
	s.Add(Summary{Fetched: 10, Matched: 20, Stored: 30, Skipped: 40, ParseErrors: 50, SinkErrors: 60, Quarantined: 70, Vanished: 80})
	require.Equal(t,
		Summary{Fetched: 11, Matched: 22, Stored: 33, Skipped: 44, ParseErrors: 55, SinkErrors: 66, Quarantined: 77, Vanished: 88},
		s)
}

// vanishingFake is a provider that both delivers mail and reports messages it
// listed and then found gone — what the Gmail provider does when a message is
// deleted between the history listing and the download.
type vanishingFake struct {
	*provider.Fake
	vanished int
}

func (f *vanishingFake) VanishedCount() int { return f.vanished }

// TestVanishedMessagesReachTheSummary: a skip the provider made must be visible
// in the run's output, and must not stop the cursor being saved — the cycle
// completing is the whole point of skipping.
func TestVanishedMessagesReachTheSummary(t *testing.T) {
	cfg, _ := testConfig(t, config.Rule{
		Name:  "everything",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})

	fake := provider.NewFake("gmail", provider.RawMessage{
		ID:           "msg-1",
		Raw:          rawMessage("recruiting@acme.com", "Still here", "hello"),
		InternalDate: fixedNow,
	})
	fake.Now = fixedNow
	fake.NextHistoryID = 1001
	prov := &vanishingFake{Fake: fake, vanished: 2}

	r, err := NewRunner(Options{
		Config: cfg,
		Providers: func(context.Context, *config.Account) (provider.Provider, error) {
			return prov, nil
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return fixedNow },
	})
	require.NoError(t, err)

	manifests, err := r.Cycle(context.Background())
	require.NoError(t, err)
	require.Len(t, manifests, 1)

	m := manifests[0]
	require.Equal(t, 2, m.Summary.Vanished, "the skip is reported, never silent")
	require.Equal(t, 1, m.Summary.Fetched, "a message that no longer exists was never fetched")
	require.Contains(t, m.Line(), "vanished=2")

	var buf strings.Builder
	require.NoError(t, WriteJSON(&buf, m))
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(buf.String()), &got))
	require.Equal(t, float64(2), got["summary"].(map[string]any)["vanished"])

	// The cursor advanced and was saved: the cycle that contained a vanished
	// message is a *complete* cycle.
	saved, err := state.NewStore(cfg.StateDir).Load("personal")
	require.NoError(t, err)
	require.Equal(t, uint64(1001), saved.HistoryID)
}

// TestSummaryOmitsVanishedWhenZero pins the published JSON contract: the new key
// is absent from every manifest of a run in which nothing disappeared, so an
// existing consumer sees byte-identical output.
func TestSummaryOmitsVanishedWhenZero(t *testing.T) {
	var buf strings.Builder
	require.NoError(t, WriteJSON(&buf, Manifest{Account: "personal", Summary: Summary{Fetched: 3}}))

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(buf.String()), &got))
	summary := got["summary"].(map[string]any)
	require.NotContains(t, summary, "vanished")
	for _, key := range []string{"fetched", "matched", "stored", "skipped", "parse_errors", "sink_errors", "quarantined"} {
		require.Contains(t, summary, key, "every existing key is still always present")
	}
}

// TestExitCodeGrading pins the contract cron branches on.
func TestExitCodeGrading(t *testing.T) {
	require.Equal(t, ExitOK, ExitCode(nil))
	require.Equal(t, ExitConfig, ExitCode(errors.New("boom")))
	require.Equal(t, ExitProvider, ExitCode(&ProviderError{Account: "a", Err: errors.New("no token")}))
	require.Equal(t, ExitLocked, ExitCode(fmt.Errorf("wrapped: %w", state.ErrLocked)))

	// Grading survives errors.Join, which is how Cycle reports several
	// accounts failing at once.
	joined := errors.Join(&ProviderError{Account: "a", Err: errors.New("x")})
	require.Equal(t, ExitProvider, ExitCode(joined))

	require.Nil(t, Exit(nil))

	var coded *ExitCodeError
	require.ErrorAs(t, Exit(fmt.Errorf("wrapped: %w", state.ErrLocked)), &coded)
	require.Equal(t, ExitLocked, coded.Code)
	require.ErrorIs(t, coded, state.ErrLocked)
	require.Contains(t, coded.Error(), "lock held")

	require.Equal(t, "exit status 7", (&ExitCodeError{Code: 7}).Error())
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
