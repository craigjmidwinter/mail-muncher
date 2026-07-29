package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/filter"
	"github.com/craigjmidwinter/mail-muncher/internal/model"
	"github.com/craigjmidwinter/mail-muncher/internal/provider"
	"github.com/craigjmidwinter/mail-muncher/internal/provider/gmail"
	"github.com/craigjmidwinter/mail-muncher/internal/provider/imap"
	"github.com/craigjmidwinter/mail-muncher/internal/sink"
	"github.com/craigjmidwinter/mail-muncher/internal/state"
)

// ProviderFactory builds the fetch backend for one configured account.
//
// It is the seam that keeps the pipeline provider-agnostic: the pipeline never
// names Gmail, so adding IMAP is a new case in DefaultProviderFactory and no
// change here. Tests inject a factory returning provider.NewFake.
//
// ctx bounds the lifetime of whatever transport the provider builds, so it must
// outlive the Fetch calls made with the returned Provider — pass the cycle's
// context straight through.
type ProviderFactory func(ctx context.Context, account *config.Account) (provider.Provider, error)

// DefaultProviderFactory dispatches on the account's `provider:` key. Gmail is
// the default when the key is omitted.
//
// This switch is the only place outside internal/provider that names a
// backend, and adding one must stay a single case here — see
// docs/architecture.md on the provider seam.
func DefaultProviderFactory(ctx context.Context, account *config.Account) (provider.Provider, error) {
	if account == nil {
		return nil, errors.New("pipeline: nil account")
	}
	switch account.Provider {
	case config.ProviderGmail, "":
		return gmail.NewFromAccount(ctx, account)
	case config.ProviderIMAP:
		return imap.NewFromAccount(ctx, account)
	default:
		return nil, fmt.Errorf("pipeline: account %q: unsupported provider %q", account.Name, account.Provider)
	}
}

// vanishedReporter is implemented by providers that can tell the difference
// between "nothing was there" and "something was listed and had been deleted by
// the time we asked for it".
//
// The seam is a structural interface rather than a method on provider.Provider
// because not every backend can know: it is the provider's business whether a
// listed message disappearing is even observable. Implementing it is how a skip
// that must never be silent reaches Summary.Vanished; not implementing it costs
// nothing. Declared here, so the pipeline still names no backend.
type vanishedReporter interface {
	// VanishedCount reports the messages the most recent Fetch listed and then
	// found no longer existed. It is read once, after Fetch returns.
	VanishedCount() int
}

// ProviderError wraps a failure to reach or authenticate against a provider, as
// opposed to a failure concerning one message. Callers distinguish it to pick
// an exit status: the CLI maps it to exit 2, so cron can tell "your token
// expired" apart from "your config is wrong".
//
// The wrapped error is returned verbatim by errors.Unwrap, so the actionable
// text on gmail.ErrNoToken and friends survives to the user.
type ProviderError struct {
	// Account is the configured account the failure belongs to.
	Account string
	// Err is the underlying provider or auth error.
	Err error
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("account %q: %v", e.Account, e.Err)
}

func (e *ProviderError) Unwrap() error { return e.Err }

// IsProviderError reports whether err is (or wraps) a ProviderError.
func IsProviderError(err error) bool {
	var pe *ProviderError
	return errors.As(err, &pe)
}

// MessageError is one message the cycle refused to move past: it would not
// parse, or a sink failed on it, and `on_message_failure: abort` says that must
// stop the account's cycle rather than advance the cursor.
//
// The cursor is not saved when it comes back, so the message is re-fetched next
// cycle. That is the point — and its cost: a message that will never parse
// wedges the account until an operator intervenes, which is exactly why
// `quarantine` is the default.
type MessageError struct {
	// Account is the account the message belongs to.
	Account string
	// ID is the provider-scoped message id.
	ID string
	// Rule is the rule that claimed the message, if it got that far.
	Rule string
	// Err is the parse or sink failure.
	Err error
}

func (e *MessageError) Error() string {
	if e.Rule != "" {
		return fmt.Sprintf("account %q: message %s (rule %q): %v", e.Account, e.ID, e.Rule, e.Err)
	}
	return fmt.Sprintf("account %q: message %s: %v", e.Account, e.ID, e.Err)
}

func (e *MessageError) Unwrap() error { return e.Err }

// DegradedFilterError ends a cycle that could not read a rule's externally
// owned domain list, under `on_degraded_filter: fail`. Nothing was fetched,
// stored, or advanced.
type DegradedFilterError struct {
	// Files are the domain lists that could not be read.
	Files []filter.DegradedFile
}

func (e *DegradedFilterError) Error() string {
	parts := make([]string, 0, len(e.Files))
	for _, f := range e.Files {
		parts = append(parts, f.String())
	}
	return "pipeline: filter degraded, nothing fetched (on_degraded_filter: fail): " + strings.Join(parts, "; ")
}

// errStopRequested is what the fetch callback returns once a shutdown has been
// requested. It travels back out through the provider — every provider treats a
// callback error as "stop feeding me messages" and hands back the state it
// reached — where cycleAccount recognizes it.
//
// It is deliberately not a cancelled context: a provider that is cancelled has
// abandoned work mid-flight and its state cannot be trusted, whereas this is an
// orderly stop between two messages, after which the state reached really is
// the state completed. Telling those apart is the whole reason a signalled
// daemon can save anything at all.
var errStopRequested = errors.New("pipeline: stop requested")

// Options configures a Runner. Config is required; everything else has a
// working default.
type Options struct {
	// Config is the loaded, validated configuration. The Runner keeps a
	// reference to it — and to its Rules slice in particular, which the filter
	// engine points into — so it must not be reordered or re-sliced afterwards.
	Config *config.Config

	// DryRun evaluates everything but writes nothing: sinks report the paths
	// they would have written (via Plan) and no state is saved.
	DryRun bool

	// Providers builds each account's fetch backend. Nil means
	// DefaultProviderFactory.
	Providers ProviderFactory

	// Logger receives every human-facing message. Nil means slog.Default().
	// The CLI points slog at stderr, which is what keeps `--json` stdout clean.
	Logger *slog.Logger

	// Now overrides the clock used for manifest timestamps and durations. Nil
	// means time.Now.
	Now func() time.Time

	// FilterOptions are passed through to filter.NewEngine, for tests that
	// need to pin the clock or share a domain-file cache.
	FilterOptions []filter.Option
}

// Runner executes fetch/filter/store cycles for a configuration.
//
// It is built once and reused: `run` calls Cycle once, `daemon` calls it on
// every tick, and the MCP server calls it per sync request. Everything
// expensive or stateful — the compiled rules, the per-rule sinks, the
// domain-list cache — lives on the Runner and is refreshed at the top of each
// Cycle, so repeated cycles neither leak nor go stale.
//
// A Runner is not safe for concurrent Cycle calls; the cycle lock it takes is
// cross-process, not in-process.
type Runner struct {
	cfg        *config.Config
	engine     *filter.Engine
	store      *state.Store
	sinks      map[*config.Rule][]sink.Sink
	providers  ProviderFactory
	log        *slog.Logger
	now        func() time.Time
	dryRun     bool
	quarantine *quarantine
}

// NewRunner compiles the config's rules and prepares the sinks. A rule whose
// match tree or format list is unusable is an error here, before any mail is
// fetched.
func NewRunner(opts Options) (*Runner, error) {
	if opts.Config == nil {
		return nil, errors.New("pipeline: no config")
	}

	engine, err := filter.NewEngine(opts.Config.Rules, opts.FilterOptions...)
	if err != nil {
		return nil, fmt.Errorf("pipeline: compile rules: %w", err)
	}

	// Sinks are stateless and reusable across messages, so build one set per
	// rule up front rather than per matched message. Keys are pointers into
	// cfg.Rules, which is exactly what Engine.Evaluate hands back.
	sinks := make(map[*config.Rule][]sink.Sink, len(opts.Config.Rules))
	for i := range opts.Config.Rules {
		rule := &opts.Config.Rules[i]
		s, err := sink.ForRule(rule)
		if err != nil {
			return nil, fmt.Errorf("pipeline: %w", err)
		}
		sinks[rule] = s
	}

	r := &Runner{
		cfg:       opts.Config,
		engine:    engine,
		store:     state.NewStore(opts.Config.StateDir),
		sinks:     sinks,
		providers: opts.Providers,
		log:       opts.Logger,
		now:       opts.Now,
		dryRun:    opts.DryRun,
	}
	if r.providers == nil {
		r.providers = DefaultProviderFactory
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	if r.now == nil {
		r.now = time.Now
	}
	r.quarantine = &quarantine{dir: opts.Config.QuarantineRoot(), now: r.now}
	return r, nil
}

// DryRun reports whether this Runner writes anything.
func (r *Runner) DryRun() bool { return r.dryRun }

// StateStore exposes the state store the Runner locks and saves through, for
// callers that want the lock path or an account's cursor.
func (r *Runner) StateStore() *state.Store { return r.store }

// InstanceLockDirName is the sub-directory of the state dir holding the
// long-lived instance lock. It is a directory of its own so the lock file
// inside it cannot be confused with the per-cycle lock at the state dir's root.
const InstanceLockDirName = "instance"

// InstanceLockPath is where TryInstanceLock takes its lock.
func (r *Runner) InstanceLockPath() string {
	return r.instanceStore().LockPath()
}

// TryInstanceLock takes a lock meant to be held for a whole process lifetime,
// and returns an error matching state.ErrLocked when another process holds it.
//
// It is deliberately *not* the cycle lock. The cycle lock is taken and released
// around each cycle, which is what lets a cron `run` and a daemon tick take
// turns; it says nothing about whether someone else is also polling. Two
// daemons sharing a state directory pass that test easily — the second one
// starts while the first is asleep — and then both poll forever, doubling the
// API traffic against the same cursors.
//
// A daemon therefore holds this for as long as it runs, on top of the per-cycle
// lock it takes for each tick. Release it with Unlock on shutdown; a crashed
// process releases it when the OS closes the file.
func (r *Runner) TryInstanceLock() (*state.Lock, error) {
	return r.instanceStore().TryLock()
}

// instanceStore is a Store rooted at the instance-lock directory. Only its lock
// is used: no cursors are ever written there.
func (r *Runner) instanceStore() *state.Store {
	return state.NewStore(filepath.Join(r.store.Dir(), InstanceLockDirName))
}

// Cycle runs one complete fetch/filter/store pass over every configured
// account and returns one Manifest per account, in config order.
//
// The whole cycle is guarded by the state directory's cross-process lock, so a
// cron `run` and a running daemon can never race on the same cursors. When
// another instance holds it, Cycle returns an error matching state.ErrLocked
// and no manifests.
//
// Errors are graded, and the manifests are always returned regardless:
//
//   - A message that will not parse, or one where a sink failed, is handled by
//     the config's `on_message_failure`. Under the default (`quarantine`) the
//     raw bytes are parked under the quarantine directory, counted, and the
//     cycle continues, so the failure never shows up in the returned error;
//     under `abort` it comes back as a *MessageError and that account's state
//     is not saved.
//   - A rule whose externally-owned domain list could not be read is handled by
//     `on_degraded_filter`. Under the default (`hold`) the cycle runs and
//     stores every match, but no account's state is saved — the manifests say
//     so — and the returned error is nil. Under `fail` nothing is fetched at
//     all and a *DegradedFilterError comes back.
//   - A provider or auth failure ends that account's cycle — its state is not
//     saved, so the next run re-fetches — but the remaining accounts still run.
//     Such failures come back as a joined error for which IsProviderError is
//     true, alongside a manifest per account listing what did land.
//
// Cycle is the single entry point shared by `run`, `daemon`, and the MCP sync
// tool; none of them may reimplement any of the above.
func (r *Runner) Cycle(ctx context.Context) ([]Manifest, error) {
	return r.CycleWithStop(ctx, nil)
}

// CycleWithStop is Cycle with a graceful-stop channel.
//
// Closing stop asks the cycle to wind down: the message being delivered is
// finished, no further message is fetched, and the state actually reached is
// saved — so a signalled daemon exits without redoing work, and without the
// half-finished cursor a hard cancellation would leave. Cancelling ctx remains
// the hard stop: it abandons the fetch mid-flight and saves nothing.
//
// A caller with nothing to stop for passes nil, which is exactly Cycle.
func (r *Runner) CycleWithStop(ctx context.Context, stop <-chan struct{}) ([]Manifest, error) {
	lock, err := r.store.TryLock()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := lock.Unlock(); err != nil {
			r.log.Warn("releasing cycle lock", "error", err)
		}
	}()

	// Re-read every externally-managed domain list. An agent that rewrote its
	// wanted-senders file between cycles gets the new list from here on.
	//
	// Preload does the reading up front rather than on the first message that
	// needs it, so the degradation check below is authoritative before a single
	// message has been fetched — `fail` promises to store nothing, and it can
	// only keep that promise if it knows first.
	r.engine.BeginCycle()
	r.engine.Preload()

	cyc := &cycle{stop: stop, degraded: r.engine.Degraded(), policy: r.cfg.DegradedFilter()}
	if len(cyc.degraded) > 0 {
		for _, d := range cyc.degraded {
			r.log.Error("filter degraded: an externally-owned domain list could not be read; messages cannot be evaluated against it",
				"path", d.Path, "error", d.Err, "on_degraded_filter", string(cyc.policy))
		}
		if cyc.policy == config.DegradedFilterFail {
			err := &DegradedFilterError{Files: cyc.degraded}
			r.log.Error("refusing to run a degraded cycle", "error", err)
			return r.degradedManifests(cyc, err), err
		}
	}

	manifests := make([]Manifest, 0, len(r.cfg.Accounts))
	var errs []error
	for i := range r.cfg.Accounts {
		account := &r.cfg.Accounts[i]

		manifest, err := r.cycleAccount(ctx, account, cyc)
		manifests = append(manifests, manifest)
		if err != nil {
			errs = append(errs, err)
		}
		if ctx.Err() != nil || stopRequested(cyc.stop) {
			break
		}
	}

	return manifests, errors.Join(errs...)
}

// cycle is the per-cycle state shared by every account in one pass.
type cycle struct {
	// stop is closed when a graceful shutdown has been requested. Nil means
	// nothing will ever ask.
	stop <-chan struct{}
	// degraded is the domain lists this cycle could not read.
	degraded []filter.DegradedFile
	// policy is what to do about them.
	policy config.DegradedFilterPolicy
}

// hold reports whether this cycle must keep its accounts' sync state where it
// is, so that mail evaluated against an unreadable list is evaluated again.
func (c *cycle) hold() bool {
	return len(c.degraded) > 0 && c.policy == config.DegradedFilterHold
}

// manifestFiles renders the degraded list for a manifest.
func (c *cycle) manifestFiles() []DegradedFile {
	if len(c.degraded) == 0 {
		return nil
	}
	out := make([]DegradedFile, 0, len(c.degraded))
	for _, f := range c.degraded {
		df := DegradedFile{Path: f.Path}
		if f.Err != nil {
			df.Error = f.Err.Error()
		}
		out = append(out, df)
	}
	return out
}

// degradedManifests builds the empty-but-explanatory manifests a `fail` cycle
// returns: one per account, so a caller that indexes by account still finds
// every account, each saying why it did nothing.
func (r *Runner) degradedManifests(cyc *cycle, err error) []Manifest {
	now := r.now()
	manifests := make([]Manifest, 0, len(r.cfg.Accounts))
	for i := range r.cfg.Accounts {
		m := newManifest(r.cfg.Accounts[i].Name, now, r.dryRun)
		m.Degraded = true
		m.DegradedFiles = cyc.manifestFiles()
		m.Error = err.Error()
		manifests = append(manifests, m)
	}
	return manifests
}

// stopRequested reports whether a graceful stop has been asked for.
func stopRequested(stop <-chan struct{}) bool {
	if stop == nil {
		return false
	}
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// cycleAccount runs one account end to end. It returns a manifest in every
// case, including the failure cases, so a caller never loses track of files
// that now exist on disk.
func (r *Runner) cycleAccount(ctx context.Context, account *config.Account, cyc *cycle) (Manifest, error) {
	started := r.now()
	manifest := newManifest(account.Name, started, r.dryRun)
	manifest.Degraded = len(cyc.degraded) > 0
	manifest.DegradedFiles = cyc.manifestFiles()
	log := r.log.With("account", account.Name)

	// finish stamps the duration, records any failure on the manifest, and
	// emits the run summary. Every return path goes through it.
	finish := func(err error) (Manifest, error) {
		manifest.DurationMS = r.now().Sub(started).Milliseconds()
		if err != nil {
			manifest.Error = err.Error()
		}
		args := append([]any{"account", account.Name}, manifest.Summary.LogArgs()...)
		args = append(args, "duration", (time.Duration(manifest.DurationMS) * time.Millisecond).String())
		if r.dryRun {
			args = append(args, "dry_run", true)
		}
		if err != nil {
			args = append(args, "error", err)
			log.Error("cycle failed", args...)
		} else {
			log.Info("cycle complete", args...)
		}
		return manifest, err
	}

	syncState, err := r.store.Load(account.Name)
	if err != nil {
		return finish(fmt.Errorf("account %q: load state: %w", account.Name, err))
	}

	prov, err := r.providers(ctx, account)
	if err != nil {
		return finish(&ProviderError{Account: account.Name, Err: err})
	}
	log = log.With("provider", prov.Name())
	log.Debug("fetching", "history_id", syncState.HistoryID, "last_sync", syncState.LastSyncTime, "seen_ids", len(syncState.SeenIDs))

	// Ids of messages this cycle accounted for on disk. They are folded into
	// the seen set only after a clean fetch, since a failed fetch's state is
	// never saved.
	var accounted []string

	newState, fetchErr := prov.Fetch(ctx, syncState, func(raw provider.RawMessage) error {
		// Checked between messages, never inside one: a shutdown stops the
		// *next* message, so whatever is already being written finishes.
		if stopRequested(cyc.stop) {
			return errStopRequested
		}
		manifest.Summary.Fetched++

		msg, err := model.Parse(raw.ID, account.Name, raw.Raw, raw.InternalDate, raw.Labels,
			model.WithThreadID(raw.ThreadID))
		if err != nil {
			// One unparseable message must never cost us the rest of the run —
			// and must never be silently consumed either.
			manifest.Summary.ParseErrors++
			log.Warn("message will not parse", "id", raw.ID, "error", err)
			return r.messageFailed(log, &manifest, account, raw, nil, quarantineReasonParse,
				fmt.Errorf("parse message: %w", err), &accounted)
		}

		rule := r.engine.Evaluate(msg)
		if rule == nil {
			// Not counted as skipped: `skipped` counts renderings that were
			// already on disk, and an unmatched message has no rendering.
			// Unmatched is fetched - matched - parse_errors.
			log.Debug("evaluated", "id", msg.ID, "subject", msg.Subject, "rule", "no match")
			return nil
		}
		log.Debug("evaluated", "id", msg.ID, "subject", msg.Subject, "rule", rule.Name)
		manifest.Summary.Matched++

		stored, deliverErr := r.deliver(log, &manifest, msg, rule)
		if deliverErr != nil {
			// Some rendering the rule asked for is missing. Advancing now would
			// mean the message never gets it: nothing re-fetches a message the
			// cursor has passed.
			return r.messageFailed(log, &manifest, account, raw, rule, quarantineReasonSink,
				deliverErr, &accounted)
		}
		if stored {
			accounted = append(accounted, msg.ID)
		}
		return nil
	})

	// Read before the error switch below, so a cycle that vanished a message and
	// *then* failed still reports the skip. A provider that cannot tell simply
	// does not implement the interface, and the counter stays zero.
	if vr, ok := prov.(vanishedReporter); ok {
		manifest.Summary.Vanished = vr.VanishedCount()
	}

	var msgErr *MessageError
	switch {
	case fetchErr == nil:
	case errors.Is(fetchErr, errStopRequested):
		// An orderly stop between two messages: everything counted above really
		// is complete, so the state below is safe to save. The mail not fetched
		// is still ahead of the cursor and arrives next run.
		manifest.Stopped = true
		log.Info("shutdown requested; finished the message in flight and saving progress",
			"fetched", manifest.Summary.Fetched)
	case errors.As(fetchErr, &msgErr):
		// on_message_failure: abort. Not a provider failure — the provider did
		// its job — so it must not grade as one, and no state is saved.
		return finish(msgErr)
	default:
		// The manifest above is already complete for everything that landed
		// before the failure; the cursor is deliberately not advanced.
		return finish(&ProviderError{Account: account.Name, Err: fetchErr})
	}

	for _, id := range accounted {
		newState.MarkSeen(id)
	}

	if r.dryRun {
		log.Debug("dry run: state not saved")
		return finish(nil)
	}

	if cyc.hold() {
		// The cycle could not evaluate every rule, so "no match" was a guess for
		// every message in it. Everything that did match is on disk; holding the
		// cursor is what gets the rest re-evaluated once the list is back.
		manifest.StateHeld = true
		log.Error("filter degraded; holding sync state so this mail is evaluated again next cycle",
			"files", len(cyc.degraded), "on_degraded_filter", string(cyc.policy))
		return finish(nil)
	}

	// Saved even when nothing matched: the provider cursor still advanced, and
	// dropping it would make the next run re-scan everything.
	if err := r.store.Save(account.Name, newState); err != nil {
		return finish(fmt.Errorf("account %q: save state: %w", account.Name, err))
	}
	return finish(nil)
}

// messageFailed applies `on_message_failure` to one message the cycle could not
// deliver, and returns what the fetch callback should do about it.
//
// The two outcomes are the two halves of the same promise — no message is ever
// both undelivered and forgotten:
//
//   - quarantine (default): the raw bytes are written under the quarantine
//     directory, loudly, and the message is marked accounted so the cursor may
//     advance past it. A poison message costs one file, not the pipeline.
//   - abort: the failure is returned, which stops the fetch and leaves the
//     cursor where it was, so the message comes back next cycle.
//
// A quarantine write that itself fails falls back to abort: refusing to advance
// is recoverable, and losing the message is not.
func (r *Runner) messageFailed(log *slog.Logger, manifest *Manifest, account *config.Account,
	raw provider.RawMessage, rule *config.Rule, reason string, cause error, accounted *[]string) error {

	ruleName := ""
	if rule != nil {
		ruleName = rule.Name
	}
	abort := func(err error) error {
		return &MessageError{Account: account.Name, ID: raw.ID, Rule: ruleName, Err: err}
	}

	if r.cfg.MessageFailure() == config.MessageFailureAbort {
		log.Error("message could not be delivered; stopping the cycle so it is retried",
			"id", raw.ID, "rule", ruleName, "reason", reason, "error", cause,
			"on_message_failure", string(config.MessageFailureAbort))
		return abort(cause)
	}

	path := r.quarantine.path(account.Name, raw.ID)
	at := r.now().UTC()
	if r.dryRun {
		log.Error("dry run: message would be quarantined",
			"id", raw.ID, "rule", ruleName, "reason", reason, "path", path, "error", cause)
	} else {
		written, writtenAt, err := r.quarantine.write(account.Name, raw, ruleName, reason, cause)
		if err != nil {
			log.Error("could not quarantine message; stopping the cycle rather than losing it",
				"id", raw.ID, "rule", ruleName, "reason", reason, "error", err, "cause", cause)
			return abort(errors.Join(cause, err))
		}
		path, at = written, writtenAt
		log.Error("message quarantined; the cursor advances past it and nothing will re-fetch it",
			"id", raw.ID, "rule", ruleName, "reason", reason, "path", path, "error", cause)
	}

	manifest.Summary.Quarantined++
	manifest.Quarantined = append(manifest.Quarantined, QuarantineEntry{
		Path:          path,
		ID:            raw.ID,
		Rule:          ruleName,
		Reason:        reason,
		Error:         cause.Error(),
		QuarantinedAt: at,
	})
	// Accounted for: the message exists on disk, just not where it was wanted.
	*accounted = append(*accounted, raw.ID)
	return nil
}

// deliver fans one matched message out to every format its rule asks for,
// recording each result on the manifest.
//
// It returns (true, nil) when every rendering the rule asked for is on disk —
// written by this cycle or already present — which is what makes the message
// eligible for the seen set and for the cursor to move past it.
//
// A sink failure concerns exactly one message and one format: it is logged,
// counted, and the remaining formats still run, so a message whose markdown
// fails still gets its `.eml`. But it is also *returned*, joined across
// formats, because a message with a missing rendering is not finished: if the
// cursor advanced anyway, that rendering would never be written by anything.
// What to do about it is `on_message_failure`'s decision, not this function's.
func (r *Runner) deliver(log *slog.Logger, manifest *Manifest, msg *model.Message, rule *config.Rule) (bool, error) {
	var (
		accounted bool
		failures  []error
	)

	for _, s := range r.sinks[rule] {
		var (
			path    string
			skipped bool
			err     error
		)
		if r.dryRun {
			// Plan stats and creates nothing, so a dry run leaves the
			// destination tree exactly as it found it.
			path, skipped, err = s.Plan(msg, rule)
		} else {
			path, skipped, err = s.Store(msg, rule)
		}
		if err != nil {
			manifest.Summary.SinkErrors++
			log.Error("sink failed", "id", msg.ID, "rule", rule.Name, "format", string(s.Format()), "error", err)
			failures = append(failures, fmt.Errorf("%s sink: %w", string(s.Format()), err))
			continue
		}

		entry := entryFor(msg, rule, s.Format(), path)
		accounted = true

		if skipped {
			manifest.Skipped = append(manifest.Skipped, entry)
			manifest.Summary.Skipped++
			log.Debug("already stored", "path", path, "rule", rule.Name, "format", string(s.Format()))
			continue
		}

		manifest.Stored = append(manifest.Stored, entry)
		manifest.Summary.Stored++
		if r.dryRun {
			log.Info("would store", "path", path, "rule", rule.Name, "format", string(s.Format()))
		} else {
			log.Info("stored", "path", path, "rule", rule.Name, "format", string(s.Format()))
		}
	}

	return accounted, errors.Join(failures...)
}
