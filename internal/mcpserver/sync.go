package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/craigmidwinter/mail-muncher/internal/pipeline"
	"github.com/craigmidwinter/mail-muncher/internal/state"
)

// ErrSyncInProgress is the answer to "run a cycle" when one is already
// running, here or in another process. It is a tool error, not a protocol
// error: the agent should see it, wait, and ask again.
var ErrSyncInProgress = errors.New("a sync is already in progress; try again shortly")

// Syncer runs one fetch/filter/store cycle. *pipeline.Runner implements it.
//
// The interface exists so the server can be exercised without a mail provider,
// and so nothing here can reach past Cycle into the pipeline's internals.
type Syncer interface {
	Cycle(ctx context.Context) ([]pipeline.Manifest, error)
}

// SyncInput are the arguments of sync.
type SyncInput struct {
	DryRun bool `json:"dry_run,omitempty" jsonschema:"evaluate rules and report what would be written without writing anything"`
}

// SyncOutput is what sync returns: the same manifest shape `mail-muncher run
// --json` writes, one per configured account.
type SyncOutput struct {
	DryRun    bool                `json:"dry_run"`
	Manifests []pipeline.Manifest `json:"manifests" jsonschema:"one manifest per configured account, in config order"`
	Error     string              `json:"error,omitempty" jsonschema:"why the cycle ended early, if it did; the manifests still list everything that reached disk first"`
}

// sync runs one pipeline cycle.
//
// It is the only tool that changes anything, and it can only ever add files:
// the cycle it runs is the same one `run` and `daemon` run, cross-process lock
// included. Two guards keep it well-behaved as a server:
//
//   - Only one sync runs at a time in this process. A second concurrent call
//     is refused immediately rather than queued, because a tool call that
//     blocks for the length of a mail fetch is indistinguishable, to an agent,
//     from a hung server.
//   - A lock held by another process (a cron `run`, a daemon tick) comes back
//     as ErrSyncInProgress. It is never waited on and never retried here.
//
// A provider failure is deliberately not a tool error: the manifests still
// describe everything that landed before it, so they are returned with the
// failure recorded alongside in Error.
func (s *Server) sync(ctx context.Context, in SyncInput) (SyncOutput, error) {
	select {
	case s.syncSlot <- struct{}{}:
		defer func() { <-s.syncSlot }()
	default:
		return SyncOutput{}, ErrSyncInProgress
	}

	runner, err := s.runner(in.DryRun)
	if err != nil {
		return SyncOutput{}, err
	}

	s.log.Info("sync requested", "dry_run", in.DryRun)
	manifests, cycleErr := runner.Cycle(ctx)
	if errors.Is(cycleErr, state.ErrLocked) {
		return SyncOutput{}, ErrSyncInProgress
	}

	out := SyncOutput{DryRun: in.DryRun, Manifests: manifests}
	if out.Manifests == nil {
		out.Manifests = []pipeline.Manifest{}
	}
	if cycleErr != nil {
		out.Error = cycleErr.Error()
		s.log.Warn("sync cycle ended with an error", "error", cycleErr)
	}
	return out, nil
}

// runner returns the pipeline runner for a dry or live cycle, building it on
// first use.
//
// Runners are built once and reused, exactly as the daemon does: everything
// expensive or stateful on one — the compiled rules, the per-rule sinks, the
// domain-list cache — is refreshed by Cycle itself, so rebuilding per call
// would recompile every rule for no gain.
func (s *Server) runner(dryRun bool) (Syncer, error) {
	s.runnerMu.Lock()
	defer s.runnerMu.Unlock()

	if r, ok := s.runners[dryRun]; ok && r != nil {
		return r, nil
	}
	r, err := s.newRunner(dryRun)
	if err != nil {
		return nil, fmt.Errorf("cannot start a sync: %w", err)
	}
	if r == nil {
		return nil, errors.New("cannot start a sync: no pipeline runner is configured")
	}
	s.runners[dryRun] = r
	return r, nil
}
