package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/pipeline"
	"github.com/craigjmidwinter/mail-muncher/internal/provider"
	"github.com/craigjmidwinter/mail-muncher/internal/state"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// matchNodeForTest parses a YAML match tree into the node config.Rule holds.
func matchNodeForTest(t *testing.T, src string) yaml.Node {
	t.Helper()
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(src), &doc))
	require.NotEmpty(t, doc.Content, "match tree %q decoded to nothing", src)
	return *doc.Content[0]
}

// syncBuffer is a bytes.Buffer safe to read from the test goroutine while the
// daemon writes to it from another.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// daemonCLI runs the command tree with a cancellable context, stdout and
// stderr kept apart. It returns as soon as the command does; the caller drives
// shutdown by cancelling.
func daemonCLI(t *testing.T, args ...string) (stdout, stderr *syncBuffer, cancel context.CancelFunc, done <-chan error) {
	t.Helper()

	stdout, stderr = &syncBuffer{}, &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())

	root := newRootCommand()
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args)

	ch := make(chan error, 1)
	go func() { ch <- root.ExecuteContext(ctx) }()

	t.Cleanup(cancel)
	return stdout, stderr, cancel, ch
}

// TestDaemonFlags pins the flags the docs publish.
func TestDaemonFlags(t *testing.T) {
	root := newRootCommand()
	for _, c := range root.Commands() {
		if c.Name() != "daemon" {
			continue
		}
		require.NotNil(t, c.Flags().Lookup("interval"), "daemon must expose --interval")
		require.NotNil(t, c.Flags().Lookup("dry-run"), "daemon must expose --dry-run")
		require.NotNil(t, c.Flags().Lookup("json"), "daemon must expose --json")

		// README publishes 5m; cobra renders a duration's DefValue canonically.
		require.Equal(t, defaultInterval.String(), c.Flags().Lookup("interval").DefValue)
		require.Equal(t, 5*time.Minute, defaultInterval, "README documents a 5m default")
		require.Equal(t, 30*time.Second, minInterval, "README documents a 30s minimum")

		require.Equal(t, "false", c.Flags().Lookup("dry-run").DefValue)
		require.Equal(t, "false", c.Flags().Lookup("json").DefValue)

		// Exit code 3 is part of the published contract and must be in --help.
		require.Contains(t, c.Long, "3 another instance")
		return
	}
	t.Fatal("daemon command not registered")
}

// TestDaemonIntervalBelowMinimumIsRejected: --interval 10s is a config error,
// and it is rejected before anything is loaded or fetched.
func TestDaemonIntervalBelowMinimumIsRejected(t *testing.T) {
	configPath, _, _ := writeRunConfig(t)

	_, _, err := runCLI(t, "daemon", "--interval", "10s", "--config", configPath)
	require.Error(t, err)
	require.Equal(t, pipeline.ExitConfig, exitCodeOf(t, err))
	require.Contains(t, err.Error(), "10s")
	require.Contains(t, err.Error(), "30s")

	// 29s is still too fast; the boundary itself is accepted, which we prove by
	// showing it gets far enough to complain about a missing config instead.
	_, _, err = runCLI(t, "daemon", "--interval", "29s", "--config", configPath)
	require.ErrorContains(t, err, "minimum")

	missing := filepath.Join(t.TempDir(), "nope.yml")
	_, _, err = runCLI(t, "daemon", "--interval", "30s", "--config", missing)
	require.Error(t, err)
	require.Equal(t, pipeline.ExitConfig, exitCodeOf(t, err))
	require.NotContains(t, err.Error(), "minimum", "30s is the accepted floor, not below it")
}

// TestDaemonLockHeldAtStartupExitsThree is the lock-contention acceptance
// criterion, with a real held lock: a second instance bails immediately rather
// than looping forever against a state dir it will never own.
func TestDaemonLockHeldAtStartupExitsThree(t *testing.T) {
	configPath, stateDir, _ := writeRunConfig(t)

	// Stand in for a concurrently running instance.
	lock, err := state.NewStore(stateDir).TryLock()
	require.NoError(t, err)
	defer func() { _ = lock.Unlock() }()

	stdout, stderr, _, done := daemonCLI(t, "daemon", "--interval", "30s", "--config", configPath)

	var daemonErr error
	select {
	case daemonErr = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not exit while the lock was held")
	}

	require.Error(t, daemonErr)
	require.Equal(t, pipeline.ExitLocked, exitCodeOf(t, daemonErr))
	require.ErrorIs(t, daemonErr, state.ErrLocked)
	require.Empty(t, stdout.String(), "a locked-out daemon has no manifest to report")
	require.Contains(t, stderr.String(), "holds the cycle lock")
}

// TestDaemonLockHeldMidLoopSkipsTheTick documents the other half of the
// decision: once the daemon owns the loop, a tick that loses the lock to a
// cron `run` waits for the next tick instead of dying.
func TestDaemonLockHeldMidLoopSkipsTheTick(t *testing.T) {
	logs := &bytes.Buffer{}
	locked := fmt.Errorf("%w: /tmp/mail-muncher.lock", state.ErrLocked)

	errs := []error{nil, locked, locked, nil}
	var cycles int

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	loop := &daemonLoop{
		interval: time.Minute,
		log:      slog.New(slog.NewTextHandler(logs, nil)),
		report:   func([]pipeline.Manifest) {},
		jitter:   func(d time.Duration) time.Duration { return d },
		sleep:    func(context.Context, time.Duration) bool { return true },
		cycle: func(context.Context, <-chan struct{}) ([]pipeline.Manifest, error) {
			err := errs[cycles]
			cycles++
			if cycles == len(errs) {
				cancel()
			}
			return nil, err
		},
	}

	require.NoError(t, loop.run(ctx), "a lock held mid-loop must not end the daemon")
	require.Equal(t, len(errs), cycles, "the loop kept ticking through the contention")

	out := logs.String()
	require.Contains(t, out, "skipping this tick")
	require.Contains(t, out, "consecutive_skips=1")
	require.Contains(t, out, "consecutive_skips=2")
	require.NotContains(t, out, "not starting", "only the first tick is fatal")
}

// TestDaemonCycleFailureDoesNotStopTheLoop: a provider error is logged with a
// running count, the loop continues, and a success resets the count.
func TestDaemonCycleFailureDoesNotStopTheLoop(t *testing.T) {
	logs := &bytes.Buffer{}
	boom := &pipeline.ProviderError{Account: "personal", Err: errors.New("token expired")}

	// fail, fail, fail, succeed (resets), fail again, succeed. The final entry
	// exists only so the one before it is graded before the loop stops.
	errs := []error{boom, boom, boom, nil, boom, nil}
	var cycles int

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var reported int
	loop := &daemonLoop{
		interval: time.Minute,
		log:      slog.New(slog.NewTextHandler(logs, nil)),
		report:   func(m []pipeline.Manifest) { reported += len(m) },
		jitter:   func(d time.Duration) time.Duration { return d },
		sleep:    func(context.Context, time.Duration) bool { return true },
		cycle: func(context.Context, <-chan struct{}) ([]pipeline.Manifest, error) {
			err := errs[cycles]
			cycles++
			if cycles == len(errs) {
				cancel()
			}
			return []pipeline.Manifest{{Account: "personal", Summary: pipeline.Summary{Fetched: 2, Stored: 1}}}, err
		},
	}

	require.NoError(t, loop.run(ctx), "a failing cycle must never end the daemon")
	require.Equal(t, len(errs), cycles)
	require.Equal(t, len(errs), reported, "every tick reports its manifest, failures included")

	out := logs.String()
	require.Contains(t, out, "consecutive_failures=2")
	require.Contains(t, out, "consecutive_failures=3")
	require.NotContains(t, out, "consecutive_failures=4",
		"the successful cycle must reset the counter")
	require.Equal(t, 2, strings.Count(out, "consecutive_failures=1"),
		"the failure after the successful cycle must start counting from 1 again")

	// Running totals are accumulated across ticks, not per cycle.
	require.Contains(t, out, "cycles=6")
	require.Contains(t, out, "fetched=12")
	require.Contains(t, out, "stored=6")
}

// blockingProvider replays messages one at a time, pausing after the first so a
// test can land a signal squarely in the middle of a cycle. It is a real
// provider.Provider, so the Runner under test takes the real path: real parse,
// real filter, real sinks, real state save.
type blockingProvider struct {
	msgs []provider.RawMessage
	// afterFirst is closed once the first message has been fully delivered.
	afterFirst chan struct{}
	// resume gates the second message, so the test controls when the cycle
	// looks at the stop channel again.
	resume chan struct{}
}

func (p *blockingProvider) Name() string { return "blocking" }

func (p *blockingProvider) Fetch(ctx context.Context, st provider.SyncState,
	fn func(provider.RawMessage) error) (provider.SyncState, error) {

	out := st.Clone()
	for i, msg := range p.msgs {
		if i == 1 {
			close(p.afterFirst)
			select {
			case <-p.resume:
			case <-ctx.Done():
				return out, ctx.Err()
			}
		}
		if err := fn(msg); err != nil {
			// Every provider does this: a callback error stops the fetch and
			// hands back the state reached so far, cursor unadvanced.
			return out, err
		}
		out.MarkSeen(msg.ID)
	}
	out.LastSyncTime = time.Now().UTC()
	return out, nil
}

// rawTestMessage builds a minimal RFC822 message the real parser accepts.
func rawTestMessage(from, subject string) []byte {
	return []byte("From: " + from + "\r\n" +
		"To: craig@example.com\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: Tue, 28 Jul 2026 09:15:00 +0000\r\n" +
		"Message-ID: <" + subject + "@mail.example.com>\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" + subject + " body\r\n")
}

// TestDaemonShutdownDuringCycleSavesStateViaTheRealRunner is the SIGTERM
// acceptance criterion, exercised through pipeline.Runner rather than a stand-in
// that sets a flag for itself.
//
// The documented promise is "the in-flight cycle finishes the message it is on,
// state is saved, and the process exits 0". The signal used to be handed
// straight to the cycle's context, which made the provider return a
// cancellation, which made the runner refuse to save anything — so the promise
// was never kept, and the next start redid the work.
func TestDaemonShutdownDuringCycleSavesStateViaTheRealRunner(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	dest := filepath.Join(root, "mail")

	cfg := &config.Config{
		StateDir: stateDir,
		Accounts: []config.Account{{Name: "personal", Provider: config.ProviderGmail}},
		Rules: []config.Rule{{
			Name:    "acme",
			Match:   matchNodeForTest(t, "{from_domains: [acme.com]}"),
			Dest:    dest,
			Formats: []config.Format{config.FormatEML},
		}},
	}

	prov := &blockingProvider{
		msgs: []provider.RawMessage{
			{ID: "first", Raw: rawTestMessage("one@acme.com", "First"), InternalDate: time.Now().UTC()},
			{ID: "second", Raw: rawTestMessage("two@acme.com", "Second"), InternalDate: time.Now().UTC()},
		},
		afterFirst: make(chan struct{}),
		resume:     make(chan struct{}),
	}

	runner, err := pipeline.NewRunner(pipeline.Options{
		Config: cfg,
		Providers: func(context.Context, *config.Account) (provider.Provider, error) {
			return prov, nil
		},
		Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		reported []pipeline.Manifest
		slept    bool
	)
	loop := &daemonLoop{
		interval: time.Minute,
		log:      slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		report:   func(m []pipeline.Manifest) { reported = append(reported, m...) },
		jitter:   func(d time.Duration) time.Duration { return d },
		sleep: func(context.Context, time.Duration) bool {
			slept = true
			return true
		},
		cycle: runner.CycleWithStop,
	}

	done := make(chan error, 1)
	go func() { done <- loop.run(ctx) }()

	// The first message is stored; the cycle is now parked before the second.
	<-prov.afterFirst
	cancel() // the SIGTERM
	close(prov.resume)

	select {
	case err := <-done:
		require.NoError(t, err, "a signalled shutdown is exit 0")
	case <-time.After(10 * time.Second):
		t.Fatal("loop did not return after cancellation")
	}

	require.Len(t, reported, 1, "the interrupted cycle's manifest is still reported")
	require.True(t, reported[0].Stopped, "the manifest must say the cycle was stopped")
	require.Equal(t, 1, reported[0].Summary.Stored, "the message in flight was finished")
	require.False(t, slept, "a cancelled cycle must not start another interval")

	// The whole point: the work the cycle completed is persisted, so the next
	// start does not redo it.
	st, err := state.NewStore(stateDir).Load("personal")
	require.NoError(t, err)
	require.True(t, st.Seen("first"), "the delivered message must be recorded in the saved state")
	require.False(t, st.Seen("second"), "the message never fetched must stay ahead of the cursor")

	require.FileExists(t, filepath.Join(stateDir, "personal.json"), "a graceful shutdown must save state")
}

// TestSecondDaemonExitsThreeWhileTheFirstSleeps is the singleton guarantee.
// The per-cycle lock cannot provide it: it is released between ticks, so a
// second daemon started during the first one's sleep used to complete its first
// cycle happily and then poll forever alongside it.
func TestSecondDaemonExitsThreeWhileTheFirstSleeps(t *testing.T) {
	configPath, _, _ := writeRunConfig(t)

	_, firstErrOut, cancelFirst, firstDone := daemonCLI(t, "daemon", "--interval", "30s", "--config", configPath)

	// The fixture has no credentials, so the first tick fails at the provider
	// and the daemon settles into its interval — lock released, sleeping.
	require.Eventually(t, func() bool {
		return strings.Contains(firstErrOut.String(), "consecutive_failures=1")
	}, 10*time.Second, 10*time.Millisecond, "the first daemon must reach its sleep")

	_, secondErrOut, _, secondDone := daemonCLI(t, "daemon", "--interval", "30s", "--config", configPath)

	select {
	case err := <-secondDone:
		require.Error(t, err, "a second daemon must not start")
		require.Equal(t, pipeline.ExitLocked, exitCodeOf(t, err))
		require.ErrorIs(t, err, state.ErrLocked)
		require.Contains(t, secondErrOut.String(), "already running")
	case <-time.After(10 * time.Second):
		t.Fatal("the second daemon kept running alongside the first")
	}

	// The first is unaffected and still shuts down cleanly.
	select {
	case err := <-firstDone:
		t.Fatalf("the first daemon exited when the second collided with it: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	cancelFirst()
	select {
	case err := <-firstDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the first daemon did not stop after cancellation")
	}
}

// TestDaemonSleepsBetweenCyclesWithinJitterBounds proves the loop actually
// waits an interval between ticks, and that every wait is inside ±10%. The
// clock is injected, so the test takes microseconds rather than minutes.
func TestDaemonSleepsBetweenCyclesWithinJitterBounds(t *testing.T) {
	const (
		interval = 5 * time.Minute
		ticks    = 200
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		delays []time.Duration
		cycles int
	)
	loop := &daemonLoop{
		interval: interval,
		log:      slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		report:   func([]pipeline.Manifest) {},
		sleep: func(_ context.Context, d time.Duration) bool {
			delays = append(delays, d)
			return true
		},
		cycle: func(context.Context, <-chan struct{}) ([]pipeline.Manifest, error) {
			cycles++
			if cycles == ticks {
				cancel()
			}
			return nil, nil
		},
	}

	require.NoError(t, loop.run(ctx))
	require.Equal(t, ticks, cycles)
	require.Len(t, delays, ticks-1, "one sleep between each pair of cycles")

	lo := time.Duration(float64(interval) * (1 - jitterFraction))
	hi := time.Duration(float64(interval) * (1 + jitterFraction))
	distinct := map[time.Duration]struct{}{}
	for _, d := range delays {
		require.GreaterOrEqual(t, d, lo, "jittered delay below the -10%% bound")
		require.LessOrEqual(t, d, hi, "jittered delay above the +10%% bound")
		distinct[d] = struct{}{}
	}
	require.Greater(t, len(distinct), ticks/2, "the delay must actually be jittered, not constant")
}

// TestJitterIntervalBounds pins the jitter arithmetic at its extremes rather
// than sampling it.
func TestJitterIntervalBounds(t *testing.T) {
	const d = 5 * time.Minute

	require.Equal(t, 4*time.Minute+30*time.Second, jitterIntervalWith(d, func() float64 { return 0 }),
		"the lowest draw is exactly -10%%")
	require.Equal(t, 5*time.Minute+30*time.Second, jitterIntervalWith(d, func() float64 { return 1 }),
		"the highest draw is exactly +10%%")
	require.Equal(t, d, jitterIntervalWith(d, func() float64 { return 0.5 }),
		"the midpoint draw is the interval itself")

	// The unseeded production path stays inside the same bounds.
	lo := time.Duration(float64(d) * (1 - jitterFraction))
	hi := time.Duration(float64(d) * (1 + jitterFraction))
	for range 10000 {
		got := jitterInterval(d)
		require.GreaterOrEqual(t, got, lo)
		require.LessOrEqual(t, got, hi)
	}

	require.Zero(t, jitterInterval(0), "a zero interval jitters to zero, not to a negative delay")
}

// TestSleepContext covers the real sleeper both ways round.
func TestSleepContext(t *testing.T) {
	require.True(t, sleepContext(context.Background(), time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, sleepContext(ctx, time.Hour), "a cancelled context must not wait out the interval")
}

// TestDaemonDryRunWritesNothing: the flag must be wired through to the Runner.
// A --dry-run daemon that silently writes files is the worst failure this tool
// has, and a daemon repeats it every five minutes.
func TestDaemonDryRunWritesNothing(t *testing.T) {
	configPath, stateDir, destDir := writeRunConfig(t)

	stdout, _, cancel, done := daemonCLI(t, "daemon", "--dry-run", "--interval", "30s", "--config", configPath)

	// The fixture fails at the provider, so the first tick reports and the
	// daemon settles into its sleep instead of exiting.
	require.Eventually(t, func() bool {
		return strings.Contains(stdout.String(), "personal")
	}, 10*time.Second, 10*time.Millisecond, "the first cycle must report a manifest")

	require.Contains(t, stdout.String(), "(dry-run)", "the summary must say it was a dry run")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "a cancelled daemon exits 0 even after failing cycles")
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not stop after cancellation")
	}

	require.NoDirExists(t, destDir, "a dry run must not create the destination tree")
	require.NoFileExists(t, filepath.Join(stateDir, "personal.json"), "a dry run must not save state")
}

// TestDaemonKeepsRunningAfterProviderFailure is the in-process end-to-end
// version: the fixture has no credentials, so every cycle fails at the
// provider, and the daemon must still be alive after several of them.
func TestDaemonKeepsRunningAfterProviderFailure(t *testing.T) {
	configPath, _, _ := writeRunConfig(t)

	stdout, stderr, cancel, done := daemonCLI(t, "daemon", "--interval", "30s", "--config", configPath)

	require.Eventually(t, func() bool {
		return strings.Contains(stderr.String(), "consecutive_failures=1")
	}, 10*time.Second, 10*time.Millisecond, "the failing cycle must be logged and counted")

	select {
	case err := <-done:
		t.Fatalf("daemon exited on a failing cycle: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	require.Contains(t, stdout.String(), "personal:", "the failing cycle still reports its manifest")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not stop after cancellation")
	}
	require.Contains(t, stderr.String(), "daemon stopped")
}

// TestDaemonJSONStdoutIsPureNDJSONUnderSIGTERM is the acceptance criterion
// written the way it is specified: a real binary, a real SIGTERM, stderr sent
// to /dev/null, exit 0, and stdout that is nothing but manifest lines.
func TestDaemonJSONStdoutIsPureNDJSONUnderSIGTERM(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}

	bin := filepath.Join(t.TempDir(), "mail-muncher")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	require.NoError(t, build.Run(), "building the CLI")

	configPath, _, _ := writeRunConfig(t)

	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	require.NoError(t, err)
	defer func() { _ = devNull.Close() }()

	stdout := &syncBuffer{}
	cmd := exec.Command(bin, "daemon", "--json", "--log-level", "debug", "--interval", "30s", "--config", configPath)
	cmd.Stdout = stdout
	cmd.Stderr = devNull // the literal `2>/dev/null` of the acceptance criterion
	require.NoError(t, cmd.Start())

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }()

	// Wait for the first tick's manifest, so the signal lands on a daemon that
	// has genuinely started looping.
	require.Eventually(t, func() bool {
		return strings.Contains(stdout.String(), "\n")
	}, 30*time.Second, 20*time.Millisecond, "the first cycle must emit a manifest line")

	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))

	select {
	case err := <-waited:
		require.NoError(t, err, "SIGTERM must be a clean exit 0")
	case <-time.After(30 * time.Second):
		t.Fatal("daemon did not exit on SIGTERM")
	}

	out := stdout.String()
	require.NotEmpty(t, out)

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.NotEmpty(t, lines)
	for _, line := range lines {
		var m pipeline.Manifest
		require.NoError(t, json.Unmarshal([]byte(line), &m),
			"stdout line is not valid JSON: %q", line)
		require.Equal(t, "personal", m.Account)
	}
	require.NotContains(t, out, "level=", "slog output leaked into stdout")
}
