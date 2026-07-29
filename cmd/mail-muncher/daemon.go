package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/craigjmidwinter/mail-muncher/internal/pipeline"
	"github.com/craigjmidwinter/mail-muncher/internal/state"
	"github.com/spf13/cobra"
)

// Polling schedule. These are published in README's flag table, so changing
// them is a documentation change too.
const (
	// defaultInterval is the gap between cycles when --interval is omitted.
	defaultInterval = 5 * time.Minute
	// minInterval is the floor --interval accepts. Anything faster is a way to
	// burn a provider's rate limit without seeing mail any sooner: Gmail's
	// history cursor makes a steady-state cycle nearly free, but the request
	// still costs quota.
	minInterval = 30 * time.Second
	// jitterFraction spreads each sleep over ±10% of the interval, so several
	// machines started from the same script do not converge on the same tick.
	jitterFraction = 0.10
	// defaultDrainTimeout bounds a graceful shutdown: after a signal the
	// in-flight cycle is asked to stop between messages and given this long to
	// finish the one it is on and save its state. Past that the cycle context is
	// cancelled outright, which loses the cycle's state but not the files it
	// already wrote — a shutdown must always terminate.
	defaultDrainTimeout = 30 * time.Second
)

// newDaemonCommand builds the `daemon` subcommand: repeated run cycles on an
// interval.
func newDaemonCommand() *cobra.Command {
	var (
		interval time.Duration
		dryRun   bool
		jsonOut  bool
	)

	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run fetch/filter/store cycles repeatedly on an interval",
		Long: "Run fetch/filter/store cycles repeatedly, sleeping --interval between\n" +
			"them (jittered by up to ±10%). This is the always-on alternative to a\n" +
			"cron `run`.\n\n" +
			"A failing cycle does not end the daemon: the failure is logged with a\n" +
			"running count of consecutive failures and the next tick tries again.\n" +
			"SIGINT or SIGTERM stops it cleanly — the in-flight cycle finishes the\n" +
			"message it is on, state is saved, and the process exits 0.\n\n" +
			"Only one daemon may poll a state directory: an instance lock is held for\n" +
			"the whole process lifetime, so a second daemon exits immediately rather\n" +
			"than doubling the API traffic against the same cursors.\n\n" +
			"Exit status: 0 stopped by a signal, 1 config or validation error,\n" +
			"3 another instance already held the instance or cycle lock at startup. A\n" +
			"lock held by a later tick — a cron `run` overlapping a daemon tick — is\n" +
			"logged and skipped, not fatal.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Nothing below is a usage problem, so never answer with usage.
			cmd.SilenceUsage = true

			if interval < minInterval {
				return &pipeline.ExitCodeError{
					Code: pipeline.ExitConfig,
					Err:  fmt.Errorf("--interval %s is below the %s minimum", interval, minInterval),
				}
			}

			configPath, err := cmd.Flags().GetString("config")
			if err != nil {
				return err
			}

			// The signal context is the whole shutdown mechanism, but it is not
			// handed to the cycle: cancelling a cycle's context abandons the
			// fetch mid-flight, and a cycle that was abandoned has nothing it can
			// safely persist. It is passed as the loop's stop *request* instead —
			// see drainContext — so the in-flight cycle finishes the message it
			// is on and saves. stop() restores the default disposition, so a
			// second signal after a wedged shutdown still kills the process.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			return runDaemon(ctx, cmd, configPath, daemonOptions{
				interval: interval,
				dryRun:   dryRun,
				jsonOut:  jsonOut,
			})
		},
	}

	cmd.Flags().DurationVar(&interval, "interval", defaultInterval, "time between cycles, minimum 30s; each sleep is jittered by up to ±10%")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "evaluate rules and report what would be written without writing anything")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "write a machine-readable manifest to stdout after every cycle, one JSON object per account")

	return cmd
}

// daemonOptions are the daemon's own flags, separated from the cobra plumbing
// so runDaemon can be exercised without a command tree.
type daemonOptions struct {
	interval time.Duration
	dryRun   bool
	jsonOut  bool
}

// runDaemon loads the config once, builds one Runner, and loops.
//
// The Runner is deliberately built once and reused for every tick: the
// compiled rules, the per-rule sinks and the domain-list cache live on it, and
// Cycle refreshes the externally-managed parts itself (it calls BeginCycle and
// takes the cross-process lock). Rebuilding it per tick would recompile every
// rule and re-read every domain file for no gain.
//
// The config, by contrast, is read once: editing config.yml needs a restart,
// while the domain files it points at are hot-reloaded every cycle. That split
// is the documented contract.
func runDaemon(ctx context.Context, cmd *cobra.Command, configPath string, opts daemonOptions) error {
	cfg, runner, err := loadRunnerFor(cmd, configPath, opts.dryRun)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	log := slog.Default()

	// Taken before anything else and held until the process exits. The cycle
	// lock cannot do this job: it is released between ticks, so a second daemon
	// starting while the first sleeps would sail past it and then poll forever
	// alongside it.
	instance, err := runner.TryInstanceLock()
	if err != nil {
		if errors.Is(err, state.ErrLocked) {
			log.Error("another mail-muncher daemon is already running against this state directory; not starting",
				"lock", runner.InstanceLockPath())
		}
		return pipeline.Exit(err)
	}
	defer func() {
		if err := instance.Unlock(); err != nil {
			log.Warn("releasing instance lock", "error", err)
		}
	}()

	log.Info("daemon starting",
		"interval", opts.interval.String(),
		"accounts", len(cfg.Accounts),
		"rules", len(cfg.Rules),
		"dry_run", opts.dryRun,
		"lock", runner.StateStore().LockPath(),
		"instance_lock", runner.InstanceLockPath(),
	)

	loop := &daemonLoop{
		interval: opts.interval,
		cycle:    runner.CycleWithStop,
		report:   func(manifests []pipeline.Manifest) { writeManifests(out, manifests, opts.jsonOut) },
		log:      log,
	}
	return loop.run(ctx)
}

// daemonLoop is the polling loop, with every source of real time and real work
// behind a field so a test can run a hundred ticks instantly.
type daemonLoop struct {
	// interval is the base gap between cycles, before jitter.
	interval time.Duration
	// cycle runs one pass over every account. In production this is
	// (*pipeline.Runner).CycleWithStop, which takes the cross-process lock
	// itself. The stop channel asks it to wind down between messages; its
	// context is the hard deadline behind that request.
	cycle func(ctx context.Context, stop <-chan struct{}) ([]pipeline.Manifest, error)
	// report renders one tick's manifests to stdout.
	report func([]pipeline.Manifest)
	// log receives every human-facing message. Nil means slog.Default().
	log *slog.Logger

	// sleep waits d or until ctx is done, reporting whether the full wait
	// elapsed. Nil means sleepContext.
	sleep func(ctx context.Context, d time.Duration) bool
	// jitter picks the actual delay for one interval. Nil means jitterInterval.
	jitter func(d time.Duration) time.Duration
	// drainTimeout bounds how long a cycle may keep running after a shutdown
	// has been requested. Zero means defaultDrainTimeout.
	drainTimeout time.Duration
}

// run cycles until the context is cancelled, and returns nil when it was: a
// daemon stopped by SIGINT or SIGTERM succeeded at its job.
//
// Cancellation is a *request*, not a cancellation of the work: the cycle keeps
// its own context and is asked to stop between messages, so it finishes the
// message in flight and saves what it completed. runDaemon holds the process's
// instance lock for the whole of this, which is what stops a second daemon from
// joining the rotation while this one sleeps.
//
// The two error cases it does not simply retry:
//
//   - The very first cycle finding the lock held means another mail-muncher
//     already owns this state directory. Looping would be a silent no-op
//     forever, so this is fatal, exit 3 — the same status a colliding `run`
//     reports, and the one a launchd KeepAlive rule can act on.
//   - A later tick finding the lock held is the ordinary cron-overlaps-daemon
//     case. It is logged and skipped; the next tick will almost certainly get
//     it.
//
// Everything else — a provider failure, an auth token that expired, a state
// file that would not save — is logged with a consecutive-failure count and
// retried on the next tick. A daemon that exited on the first expired token
// would need a human at exactly the wrong moment.
func (l *daemonLoop) run(ctx context.Context) error {
	log := l.log
	if log == nil {
		log = slog.Default()
	}
	sleep := l.sleep
	if sleep == nil {
		sleep = sleepContext
	}
	jitter := l.jitter
	if jitter == nil {
		jitter = jitterInterval
	}
	drain := l.drainTimeout
	if drain <= 0 {
		drain = defaultDrainTimeout
	}

	var (
		totals   pipeline.Summary
		cycles   int
		failures int // consecutive failed cycles, reset by any success
		locked   int // consecutive ticks skipped because the lock was held
		started  = time.Now()
	)

	for {
		// The cycle deliberately does not run on ctx: a signal must not cancel
		// it, or the provider abandons the fetch mid-flight and the cycle has
		// nothing it can safely persist. Instead the cycle is *asked* to stop
		// (ctx.Done() is its stop channel) and given drain to finish the message
		// it is on and save; only if it overruns that does the detached context
		// cancel it for real.
		cycleCtx, endCycle := drainContext(ctx, drain)
		manifests, err := l.cycle(cycleCtx, ctx.Done())
		endCycle()
		cycles++

		// Report before grading: a cycle that died partway still created files,
		// and the caller must not lose track of them. This is also what makes
		// `daemon --json` a stream — one NDJSON line per account per tick.
		for _, m := range manifests {
			totals.Add(m.Summary)
		}
		l.report(manifests)

		// A cancelled context means a signal arrived mid-cycle. The cycle above
		// was asked to stop, finished the message it was on, and saved what it
		// legitimately completed. Stop without grading it as a failure — but do
		// say so when it did fail, because "the drain timed out" and "the
		// shutdown was clean" are different facts and only the log has room for
		// the difference.
		if ctx.Err() != nil {
			if err != nil {
				log.Error("shutdown requested; the in-flight cycle did not finish cleanly", "error", err)
			} else {
				log.Info("shutdown requested; in-flight cycle finished")
			}
			break
		}

		switch {
		case err == nil:
			failures, locked = 0, 0
		case errors.Is(err, state.ErrLocked):
			locked++
			if cycles == 1 {
				log.Error("another mail-muncher instance holds the cycle lock; not starting")
				return pipeline.Exit(err)
			}
			log.Warn("cycle lock held elsewhere; skipping this tick",
				"consecutive_skips", locked, "error", err)
		default:
			failures++
			log.Error("cycle failed; waiting for the next tick",
				"consecutive_failures", failures, "error", err)
		}

		delay := jitter(l.interval)
		log.Debug("waiting for next cycle", "delay", delay.String())
		if !sleep(ctx, delay) {
			log.Info("shutdown requested")
			break
		}
	}

	args := append([]any{"cycles", cycles}, totals.LogArgs()...)
	args = append(args, "uptime", time.Since(started).Round(time.Second).String())
	log.Info("daemon stopped", args...)
	return nil
}

// drainContext returns a context for one cycle that survives the cancellation
// of parent, plus the function that ends it.
//
// This is the mechanism behind the documented "finish the message in progress,
// save state, exit 0". A signal cancels parent; the returned context keeps
// going, so the provider's workers drain and the cycle persists what it
// completed instead of failing on a cancelled fetch. Only if the cycle is still
// running drain later is it cancelled outright, so a wedged shutdown still
// terminates.
//
// The returned cancel function is safe to call exactly once per cycle, and must
// be, or the watchdog goroutine leaks until parent is done.
func drainContext(parent context.Context, drain time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	done := make(chan struct{})

	go func() {
		select {
		case <-done:
			return
		case <-parent.Done():
		}
		timer := time.NewTimer(drain)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			cancel()
		}
	}()

	var once sync.Once
	return ctx, func() {
		once.Do(func() { close(done) })
		cancel()
	}
}

// jitterInterval returns d perturbed by up to ±jitterFraction.
func jitterInterval(d time.Duration) time.Duration {
	return jitterIntervalWith(d, rand.Float64)
}

// jitterIntervalWith is jitterInterval with the randomness injected, so the
// bounds can be tested at the extremes rather than sampled.
//
// float64 returns a value in [0, 1), so the result lies in
// [d*(1-jitterFraction), d*(1+jitterFraction)).
func jitterIntervalWith(d time.Duration, float64s func() float64) time.Duration {
	if d <= 0 {
		return 0
	}
	factor := 1 + jitterFraction*(2*float64s()-1)
	return time.Duration(float64(d) * factor)
}

// sleepContext waits d, or until ctx is done. It reports whether the full wait
// elapsed; false means the context was cancelled and the caller should stop.
func sleepContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
