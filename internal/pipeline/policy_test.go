package pipeline

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/craigmidwinter/mail-muncher/internal/config"
	"github.com/craigmidwinter/mail-muncher/internal/provider"
	"github.com/craigmidwinter/mail-muncher/internal/state"
	"github.com/stretchr/testify/require"
)

// policyConfig builds a one-account config with one rule, plus the knobs this
// file is about. It returns the config and its destination directory.
func policyConfig(t *testing.T, rule config.Rule) (*config.Config, string) {
	t.Helper()
	cfg, dest := testConfig(t, rule)
	return cfg, dest
}

// quarantineFiles lists the quarantine tree, relative to its root.
func quarantineFiles(t *testing.T, cfg *config.Config) []string {
	t.Helper()
	return walkFiles(t, cfg.QuarantineRoot())
}

// TestQuarantineKeepsUnparseableMailAndAdvances is the default policy end to
// end. An unparseable message used to be counted, logged at warning, and
// dropped on the floor while the cursor sailed past it — the message existed
// nowhere afterwards and nothing would ever fetch it again.
func TestQuarantineKeepsUnparseableMailAndAdvances(t *testing.T) {
	cfg, _ := policyConfig(t, config.Rule{
		Name:  "acme",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})
	require.Equal(t, config.MessageFailureQuarantine, cfg.MessageFailure(), "quarantine is the default")

	broken := []byte("this is not a message at all")
	fake := provider.NewFake("gmail",
		provider.RawMessage{ID: "broken", ThreadID: "t-1", Raw: broken, InternalDate: fixedNow},
		provider.RawMessage{ID: "good", Raw: rawMessage("x@acme.com", "Hello", "hi"), InternalDate: fixedNow},
	)
	fake.Now = fixedNow
	fake.NextHistoryID = 4242

	manifests, err := newTestRunner(t, cfg, fake, false).Cycle(context.Background())
	require.NoError(t, err, "quarantining a message must not change the exit status")

	m := manifests[0]
	require.Equal(t, 1, m.Summary.ParseErrors)
	require.Equal(t, 1, m.Summary.Quarantined)
	require.Equal(t, 1, m.Summary.Stored, "the message after the broken one still landed")

	require.Len(t, m.Quarantined, 1)
	entry := m.Quarantined[0]
	require.Equal(t, "broken", entry.ID)
	require.Equal(t, "parse", entry.Reason)
	require.NotEmpty(t, entry.Error)

	// The raw bytes are there, byte for byte, with a sidecar explaining them.
	stored, readErr := os.ReadFile(entry.Path)
	require.NoError(t, readErr, "the quarantined message must be on disk at the path the manifest names")
	require.Equal(t, broken, stored, "quarantine must keep the raw RFC822 bytes verbatim")

	sidecar := strings.TrimSuffix(entry.Path, ".eml") + ".json"
	data, readErr := os.ReadFile(sidecar)
	require.NoError(t, readErr)
	var record map[string]any
	require.NoError(t, json.Unmarshal(data, &record))
	require.Equal(t, "broken", record["id"])
	require.Equal(t, "personal", record["account"])
	require.Equal(t, "t-1", record["thread_id"])
	require.Equal(t, "parse", record["reason"])
	require.NotEmpty(t, record["error"])
	require.NotEmpty(t, record["quarantined_at"])

	require.Equal(t, []string{filepath.Join("personal", "broken.eml"), filepath.Join("personal", "broken.json")},
		quarantineFiles(t, cfg))

	// And the cursor advanced: a poison message cannot wedge the pipeline.
	st, err := state.NewStore(cfg.StateDir).Load("personal")
	require.NoError(t, err)
	require.Equal(t, uint64(4242), st.HistoryID)
	require.True(t, st.Seen("broken"), "a quarantined message is accounted for, not retried forever")
}

// TestQuarantineKeepsMailWhenEverySinkFails covers the other half: a message
// that matched but could not be written anywhere.
func TestQuarantineKeepsMailWhenEverySinkFails(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	require.NoError(t, os.WriteFile(blocked, []byte("not a directory"), 0o644))

	cfg := &config.Config{
		StateDir: filepath.Join(root, "state"),
		Accounts: []config.Account{{Name: "personal", Provider: config.ProviderGmail}},
		Rules: []config.Rule{{
			Name:    "blocked",
			Match:   matchNode(t, "{from_domains: [acme.com]}"),
			Dest:    blocked,
			Formats: []config.Format{config.FormatEML},
		}},
	}

	raw := rawMessage("x@acme.com", "Hello", "hi")
	fake := provider.NewFake("gmail", provider.RawMessage{ID: "msg-1", Raw: raw, InternalDate: fixedNow})
	fake.Now = fixedNow

	manifests, err := newTestRunner(t, cfg, fake, false).Cycle(context.Background())
	require.NoError(t, err)

	m := manifests[0]
	require.Equal(t, 1, m.Summary.SinkErrors)
	require.Equal(t, 1, m.Summary.Quarantined)
	require.Len(t, m.Quarantined, 1)
	require.Equal(t, "sink", m.Quarantined[0].Reason)
	require.Equal(t, "blocked", m.Quarantined[0].Rule, "the sidecar names the rule that wanted the message")

	stored, readErr := os.ReadFile(m.Quarantined[0].Path)
	require.NoError(t, readErr)
	require.Equal(t, raw, stored)
}

// TestQuarantineCoversAPartiallyDeliveredMessage: eml wrote, markdown did not.
// The message is on disk in one format, so nothing is lost — but it is missing
// a rendering its rule asked for, and no later run would ever supply it.
func TestQuarantineCoversAPartiallyDeliveredMessage(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "mail")

	cfg := &config.Config{
		StateDir: filepath.Join(root, "state"),
		Accounts: []config.Account{{Name: "personal", Provider: config.ProviderGmail}},
		Rules: []config.Rule{{
			Name:    "acme",
			Match:   matchNode(t, "{from_domains: [acme.com]}"),
			Dest:    dest,
			Formats: []config.Format{config.FormatEML, config.FormatMarkdown},
		}},
	}

	msg := provider.RawMessage{ID: "msg-1", Raw: rawMessage("x@acme.com", "Hello", "hi"), InternalDate: fixedNow}

	// A dry run names the exact paths a real run would write, which is how the
	// test can break precisely one of the two renderings.
	planFake := provider.NewFake("gmail", msg)
	planFake.Now = fixedNow
	planned, err := newTestRunner(t, cfg, planFake, true).Cycle(context.Background())
	require.NoError(t, err)
	require.Len(t, planned[0].Stored, 2)

	var markdownPath string
	for _, e := range planned[0].Stored {
		if e.Format == config.FormatMarkdown {
			markdownPath = e.Path
		}
	}
	require.NotEmpty(t, markdownPath)

	// A symlink where the markdown file goes: the sink refuses to write through
	// it, while the eml rendering beside it succeeds.
	require.NoError(t, os.MkdirAll(filepath.Dir(markdownPath), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "elsewhere.md"), markdownPath))

	fake := provider.NewFake("gmail", msg)
	fake.Now = fixedNow
	fake.NextHistoryID = 31337

	manifests, err := newTestRunner(t, cfg, fake, false).Cycle(context.Background())
	require.NoError(t, err)

	m := manifests[0]
	require.Equal(t, 1, m.Summary.SinkErrors, "only the markdown rendering failed")
	require.Equal(t, 1, m.Summary.Stored, "the eml rendering still landed")
	require.Equal(t, 1, m.Summary.Quarantined,
		"a message missing a rendering its rule asked for must not be silently passed over")
	require.FileExists(t, m.Quarantined[0].Path)
	require.Contains(t, m.Quarantined[0].Error, "markdown")

	require.NotEmpty(t, dest)
}

// TestOnMessageFailureAbortHoldsTheCursor: the opposite trade-off, chosen
// explicitly. Nothing advances, so the message comes back next cycle.
func TestOnMessageFailureAbortHoldsTheCursor(t *testing.T) {
	cfg, _ := policyConfig(t, config.Rule{
		Name:  "acme",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})
	cfg.OnMessageFailure = config.MessageFailureAbort

	fake := provider.NewFake("gmail",
		provider.RawMessage{ID: "broken", Raw: []byte("not a message"), InternalDate: fixedNow},
		provider.RawMessage{ID: "good", Raw: rawMessage("x@acme.com", "Hello", "hi"), InternalDate: fixedNow},
	)
	fake.Now = fixedNow
	fake.NextHistoryID = 99

	manifests, err := newTestRunner(t, cfg, fake, false).Cycle(context.Background())
	require.Error(t, err, "abort must surface the failure")

	var msgErr *MessageError
	require.ErrorAs(t, err, &msgErr)
	require.Equal(t, "broken", msgErr.ID)
	require.Equal(t, "personal", msgErr.Account)
	require.False(t, IsProviderError(err), "a message failure must not masquerade as a provider failure")
	require.NotEqual(t, ExitOK, ExitCode(err))

	require.Zero(t, manifests[0].Summary.Quarantined, "abort writes nothing to quarantine")
	require.NoDirExists(t, cfg.QuarantineRoot())

	// Nothing saved: the next cycle re-fetches the message.
	st, loadErr := state.NewStore(cfg.StateDir).Load("personal")
	require.NoError(t, loadErr)
	require.Zero(t, st.HistoryID, "abort must not advance the cursor")
	require.True(t, st.LastSyncTime.IsZero())
}

// TestQuarantineFailureFallsBackToHoldingTheCursor: if the quarantine write
// itself fails there is nowhere safe to put the message, so the cycle refuses
// to move past it rather than losing it.
func TestQuarantineFailureFallsBackToHoldingTheCursor(t *testing.T) {
	cfg, _ := policyConfig(t, config.Rule{
		Name:  "acme",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})

	// A file where the quarantine root must be: every write under it fails.
	blocked := filepath.Join(t.TempDir(), "quarantine")
	require.NoError(t, os.WriteFile(blocked, []byte("in the way"), 0o644))
	cfg.QuarantineDir = blocked

	fake := provider.NewFake("gmail",
		provider.RawMessage{ID: "broken", Raw: []byte("not a message"), InternalDate: fixedNow},
	)
	fake.Now = fixedNow
	fake.NextHistoryID = 7

	manifests, err := newTestRunner(t, cfg, fake, false).Cycle(context.Background())
	require.Error(t, err)
	var msgErr *MessageError
	require.ErrorAs(t, err, &msgErr)
	require.Zero(t, manifests[0].Summary.Quarantined)

	st, loadErr := state.NewStore(cfg.StateDir).Load("personal")
	require.NoError(t, loadErr)
	require.Zero(t, st.HistoryID, "a message that could not be quarantined must not be passed over")
}

// TestDryRunNeverQuarantines: --dry-run writes nothing, quarantine included.
func TestDryRunNeverQuarantines(t *testing.T) {
	cfg, _ := policyConfig(t, config.Rule{
		Name:  "acme",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})

	fake := provider.NewFake("gmail",
		provider.RawMessage{ID: "broken", Raw: []byte("not a message"), InternalDate: fixedNow},
	)
	fake.Now = fixedNow

	manifests, err := newTestRunner(t, cfg, fake, true).Cycle(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, manifests[0].Summary.Quarantined, "a dry run still reports what it would park")
	require.NoDirExists(t, cfg.QuarantineRoot(), "a dry run must not write a quarantine file")
}

// degradedConfig builds a config whose only rule points at a domain file that
// does not exist — the real-world state this whole policy exists for, since the
// program that owns the file may not have been written yet.
func degradedConfig(t *testing.T) (*config.Config, string, string) {
	t.Helper()
	root := t.TempDir()
	missing := filepath.Join(root, "wanted-senders.txt")
	dest := filepath.Join(root, "mail")

	cfg := &config.Config{
		StateDir: filepath.Join(root, "state"),
		Accounts: []config.Account{{Name: "personal", Provider: config.ProviderGmail}},
		Rules: []config.Rule{
			{
				Name:    "wanted",
				Match:   matchNode(t, "{from_domains_file: "+missing+"}"),
				Dest:    dest,
				Formats: []config.Format{config.FormatEML},
			},
			{
				Name:    "acme",
				Match:   matchNode(t, "{from_domains: [acme.com]}"),
				Dest:    dest,
				Formats: []config.Format{config.FormatEML},
			},
		},
	}
	return cfg, missing, dest
}

func degradedFake() *provider.Fake {
	f := provider.NewFake("gmail",
		provider.RawMessage{ID: "wanted", Raw: rawMessage("hr@wanted.example", "Interview", "hi"), InternalDate: fixedNow},
		provider.RawMessage{ID: "acme", Raw: rawMessage("x@acme.com", "Hello", "hi"), InternalDate: fixedNow},
	)
	f.Now = fixedNow
	f.NextHistoryID = 5150
	return f
}

// TestOnDegradedFilterHoldStoresMatchesButHoldsState is the default policy for
// bug 2. A domain list that could not be read makes "no match" a guess, so the
// cycle stores what it could and refuses to advance past what it could not
// really evaluate.
func TestOnDegradedFilterHoldStoresMatchesButHoldsState(t *testing.T) {
	cfg, missing, _ := degradedConfig(t)
	require.Equal(t, config.DegradedFilterHold, cfg.DegradedFilter(), "hold is the default")

	fake := degradedFake()

	var logs strings.Builder
	runner, err := NewRunner(Options{
		Config:    cfg,
		Providers: func(context.Context, *config.Account) (provider.Provider, error) { return fake, nil },
		Logger:    slog.New(slog.NewTextHandler(&logs, nil)),
		Now:       func() time.Time { return fixedNow },
	})
	require.NoError(t, err)

	manifests, err := runner.Cycle(context.Background())
	require.NoError(t, err, "hold is not a failed run")

	m := manifests[0]
	require.True(t, m.Degraded, "the manifest must say the cycle was degraded")
	require.True(t, m.StateHeld, "the manifest must say state was held")
	require.Len(t, m.DegradedFiles, 1)
	require.Equal(t, missing, m.DegradedFiles[0].Path)
	require.NotEmpty(t, m.DegradedFiles[0].Error)

	// Everything that did match is still stored.
	require.Equal(t, 1, m.Summary.Matched, "the intact rule still claimed its message")
	require.Equal(t, 1, m.Summary.Stored)
	for _, e := range m.Stored {
		require.FileExists(t, e.Path)
	}

	// But nothing advanced, so the message the unreadable list would have
	// claimed is evaluated again next cycle.
	st, loadErr := state.NewStore(cfg.StateDir).Load("personal")
	require.NoError(t, loadErr)
	require.Zero(t, st.HistoryID, "a degraded cycle must not advance the cursor")
	require.True(t, st.LastSyncTime.IsZero())

	require.Contains(t, logs.String(), "level=ERROR", "degradation must be loud")
	require.Contains(t, logs.String(), "holding sync state")

	// Once the owning program writes the file, the same runner picks the mail up.
	require.NoError(t, os.WriteFile(missing, []byte("wanted.example\n"), 0o600))
	second, err := runner.Cycle(context.Background())
	require.NoError(t, err)
	require.False(t, second[0].Degraded)
	require.False(t, second[0].StateHeld)
	require.Equal(t, 2, second[0].Summary.Matched, "the held mail was re-evaluated, not consumed")
	require.Equal(t, 1, second[0].Summary.Stored, "and the one that was missing is now stored")

	st, loadErr = state.NewStore(cfg.StateDir).Load("personal")
	require.NoError(t, loadErr)
	require.Equal(t, uint64(5150), st.HistoryID, "a clean cycle advances again")
}

// TestOnDegradedFilterFailStoresNothing: the strictest policy stops before a
// single message is fetched, and exits non-zero.
func TestOnDegradedFilterFailStoresNothing(t *testing.T) {
	cfg, missing, dest := degradedConfig(t)
	cfg.OnDegradedFilter = config.DegradedFilterFail

	fake := degradedFake()
	manifests, err := newTestRunner(t, cfg, fake, false).Cycle(context.Background())

	require.Error(t, err)
	require.NotEqual(t, ExitOK, ExitCode(err), "fail must exit non-zero")
	var degErr *DegradedFilterError
	require.ErrorAs(t, err, &degErr)
	require.Len(t, degErr.Files, 1)
	require.Contains(t, err.Error(), missing)

	require.Zero(t, fake.Calls, "fail must not fetch anything")
	require.Empty(t, walkFiles(t, dest), "fail must store nothing")

	require.Len(t, manifests, 1, "a manifest per account is still returned")
	require.True(t, manifests[0].Degraded)
	require.Empty(t, manifests[0].Stored)
	require.Contains(t, manifests[0].Error, missing)

	st, loadErr := state.NewStore(cfg.StateDir).Load("personal")
	require.NoError(t, loadErr)
	require.Zero(t, st.HistoryID)
	require.True(t, st.LastSyncTime.IsZero())
}

// TestOnDegradedFilterProceedKeepsTodaysBehaviour: the escape hatch, which
// documents itself as accepting the loss.
func TestOnDegradedFilterProceedKeepsTodaysBehaviour(t *testing.T) {
	cfg, _, _ := degradedConfig(t)
	cfg.OnDegradedFilter = config.DegradedFilterProceed

	fake := degradedFake()
	manifests, err := newTestRunner(t, cfg, fake, false).Cycle(context.Background())
	require.NoError(t, err)

	m := manifests[0]
	require.True(t, m.Degraded, "even proceeding, the manifest reports the degradation")
	require.False(t, m.StateHeld)
	require.Equal(t, 1, m.Summary.Stored)

	st, loadErr := state.NewStore(cfg.StateDir).Load("personal")
	require.NoError(t, loadErr)
	require.Equal(t, uint64(5150), st.HistoryID, "proceed advances past mail it could not evaluate")
}

// TestDegradedCycleIsVisibleInTheManifestJSON: an agent consuming --json must
// be able to tell a quiet cycle from one that could not do its job.
func TestDegradedCycleIsVisibleInTheManifestJSON(t *testing.T) {
	cfg, missing, _ := degradedConfig(t)
	fake := degradedFake()

	manifests, err := newTestRunner(t, cfg, fake, false).Cycle(context.Background())
	require.NoError(t, err)

	var buf strings.Builder
	require.NoError(t, WriteJSON(&buf, manifests[0]))

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(buf.String()), &got))
	require.Equal(t, true, got["degraded"])
	require.Equal(t, true, got["state_held"])

	files := got["degraded_files"].([]any)
	require.Len(t, files, 1)
	require.Equal(t, missing, files[0].(map[string]any)["path"])
	require.NotEmpty(t, files[0].(map[string]any)["error"])

	// A clean cycle carries none of these keys, so the shape an agent already
	// parses is unchanged.
	require.NoError(t, os.WriteFile(missing, []byte("wanted.example\n"), 0o600))
	clean, err := newTestRunner(t, cfg, provider.NewFake("gmail"), false).Cycle(context.Background())
	require.NoError(t, err)

	buf.Reset()
	require.NoError(t, WriteJSON(&buf, clean[0]))
	var cleanJSON map[string]any
	require.NoError(t, json.Unmarshal([]byte(buf.String()), &cleanJSON))
	require.NotContains(t, keysOf(cleanJSON), "degraded")
	require.NotContains(t, keysOf(cleanJSON), "state_held")
	require.NotContains(t, keysOf(cleanJSON), "degraded_files")
}

// TestQuarantinedMessageIsVisibleInTheManifestJSON pins the shape an agent
// reads to find mail that needs a human.
func TestQuarantinedMessageIsVisibleInTheManifestJSON(t *testing.T) {
	cfg, _ := policyConfig(t, config.Rule{
		Name:  "acme",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})

	fake := provider.NewFake("gmail",
		provider.RawMessage{ID: "broken", Raw: []byte("not a message"), InternalDate: fixedNow},
	)
	fake.Now = fixedNow

	manifests, err := newTestRunner(t, cfg, fake, false).Cycle(context.Background())
	require.NoError(t, err)

	var buf strings.Builder
	require.NoError(t, WriteJSON(&buf, manifests[0]))

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(buf.String()), &got))

	entries := got["quarantined"].([]any)
	require.Len(t, entries, 1)
	entry := entries[0].(map[string]any)
	require.ElementsMatch(t,
		[]string{"path", "id", "reason", "error", "quarantined_at"},
		keysOf(entry))
	require.Equal(t, "broken", entry["id"])
	require.Equal(t, "parse", entry["reason"])
	require.Equal(t, float64(1), got["summary"].(map[string]any)["quarantined"])
}

// TestCycleWithStopFinishesTheMessageInFlightAndSaves is bug 3 at the pipeline
// level: a graceful stop is not a failure, and the state it reached is safe to
// keep — unlike a cancelled context, which abandons the fetch mid-flight.
func TestCycleWithStopFinishesTheMessageInFlightAndSaves(t *testing.T) {
	cfg, _ := policyConfig(t, config.Rule{
		Name:  "acme",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})

	fake := provider.NewFake("gmail",
		provider.RawMessage{ID: "a", Raw: rawMessage("one@acme.com", "First", "hi"), InternalDate: fixedNow},
		provider.RawMessage{ID: "b", Raw: rawMessage("two@acme.com", "Second", "hi"), InternalDate: fixedNow},
	)
	fake.Now = fixedNow

	// Already closed: the stop is noticed on the very first message, so nothing
	// is fetched at all and the cycle still succeeds.
	stop := make(chan struct{})
	close(stop)

	manifests, err := newTestRunner(t, cfg, fake, false).CycleWithStop(context.Background(), stop)
	require.NoError(t, err, "a requested stop is not a failure")

	m := manifests[0]
	require.True(t, m.Stopped)
	require.Zero(t, m.Summary.Fetched)

	require.FileExists(t, filepath.Join(cfg.StateDir, "personal.json"),
		"a stopped cycle saves the state it legitimately reached")
}

// TestCycleWithStopIsNotACancelledContext keeps the two apart: a hard
// cancellation must still refuse to save.
func TestCycleWithStopIsNotACancelledContext(t *testing.T) {
	cfg, _ := policyConfig(t, config.Rule{
		Name:  "acme",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})

	fake := provider.NewFake("gmail",
		provider.RawMessage{ID: "a", Raw: rawMessage("one@acme.com", "First", "hi"), InternalDate: fixedNow},
	)
	fake.Now = fixedNow

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newTestRunner(t, cfg, fake, false).CycleWithStop(ctx, nil)
	require.Error(t, err)
	require.True(t, IsProviderError(err), "an abandoned fetch is still a provider failure")
	require.NoFileExists(t, filepath.Join(cfg.StateDir, "personal.json"),
		"a cancelled fetch must not save state")
}

// TestInstanceLockIsSeparateFromTheCycleLock is bug 4's mechanism: the two
// locks are different files, so holding one says nothing about the other.
func TestInstanceLockIsSeparateFromTheCycleLock(t *testing.T) {
	cfg, _ := policyConfig(t, config.Rule{
		Name:  "acme",
		Match: matchNode(t, "{from_domains: [acme.com]}"),
	})
	runner := newTestRunner(t, cfg, provider.NewFake("gmail"), false)

	require.NotEqual(t, runner.StateStore().LockPath(), runner.InstanceLockPath())

	instance, err := runner.TryInstanceLock()
	require.NoError(t, err)
	t.Cleanup(func() { _ = instance.Unlock() })

	// Holding the instance lock does not block cycles — a daemon holds it for
	// its whole life and still runs a cycle every tick.
	_, err = runner.Cycle(context.Background())
	require.NoError(t, err)

	// But it does block a second instance.
	_, err = runner.TryInstanceLock()
	require.ErrorIs(t, err, state.ErrLocked)
	require.Equal(t, ExitLocked, ExitCode(err))

	require.NoError(t, instance.Unlock())
	again, err := runner.TryInstanceLock()
	require.NoError(t, err, "the lock is released when the holder exits")
	require.NoError(t, again.Unlock())
}
