package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/craigmidwinter/mail-muncher/internal/pipeline"
	"github.com/craigmidwinter/mail-muncher/internal/state"
)

// fakeSyncer stands in for a pipeline.Runner. Cycle blocks on release when it
// is non-nil, which is how the concurrency guard is tested without a mail
// provider.
type fakeSyncer struct {
	mu        sync.Mutex
	calls     int
	manifests []pipeline.Manifest
	err       error
	entered   chan struct{}
	release   chan struct{}
}

func (f *fakeSyncer) Cycle(ctx context.Context) ([]pipeline.Manifest, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()

	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.manifests, f.err
}

func (f *fakeSyncer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestSyncReturnsManifests: the tool hands back the pipeline's manifest
// unchanged, which is the shape `run --json` publishes.
func TestSyncReturnsManifests(t *testing.T) {
	runner := &fakeSyncer{manifests: []pipeline.Manifest{{
		Account: "personal",
		Stored:  []pipeline.Entry{{Path: "/mail/jobs/2026/07/a.eml", ID: "x", ThreadID: "t", Subject: "Hi"}},
		Skipped: []pipeline.Entry{},
		Summary: pipeline.Summary{Fetched: 3, Matched: 1, Stored: 1},
	}}}
	s := newFixture(t).server(runner)

	got, err := s.sync(context.Background(), SyncInput{})
	require.NoError(t, err)
	require.False(t, got.DryRun)
	require.Empty(t, got.Error)
	require.Len(t, got.Manifests, 1)
	require.Equal(t, "personal", got.Manifests[0].Account)
	require.Equal(t, 1, got.Manifests[0].Summary.Stored)
	require.Equal(t, 1, runner.callCount())
}

// TestSyncDryRunUsesItsOwnRunner: dry-run is a property of the Runner, so the
// flag must reach the constructor rather than being applied afterwards.
func TestSyncDryRunUsesItsOwnRunner(t *testing.T) {
	f := newFixture(t)

	var asked []bool
	live := &fakeSyncer{manifests: []pipeline.Manifest{{Account: "personal"}}}
	dry := &fakeSyncer{manifests: []pipeline.Manifest{{Account: "personal", DryRun: true}}}

	s, err := New(Options{
		Config: f.cfg,
		Logger: discardLogger(),
		NewRunner: func(dryRun bool) (Syncer, error) {
			asked = append(asked, dryRun)
			if dryRun {
				return dry, nil
			}
			return live, nil
		},
	})
	require.NoError(t, err)

	got, err := s.sync(context.Background(), SyncInput{DryRun: true})
	require.NoError(t, err)
	require.True(t, got.DryRun)
	require.True(t, got.Manifests[0].DryRun)
	require.Equal(t, []bool{true}, asked)

	_, err = s.sync(context.Background(), SyncInput{})
	require.NoError(t, err)
	require.Equal(t, []bool{true, false}, asked)

	// Each runner is built once and then reused, as the daemon does.
	_, err = s.sync(context.Background(), SyncInput{DryRun: true})
	require.NoError(t, err)
	require.Equal(t, []bool{true, false}, asked, "runners are cached")
	require.Equal(t, 2, dry.callCount())
	require.Equal(t, 1, live.callCount())
}

// TestSyncLockHeldElsewhereIsACleanToolError is the acceptance criterion: a
// cron `run` or a daemon tick holding the cross-process lock must produce a
// readable refusal, never a hang and never a crash.
func TestSyncLockHeldElsewhereIsACleanToolError(t *testing.T) {
	s := newFixture(t).server(&fakeSyncer{
		err: fmt.Errorf("account %q: %w", "personal", state.ErrLocked),
	})

	got, err := s.sync(context.Background(), SyncInput{})
	require.ErrorIs(t, err, ErrSyncInProgress)
	require.Contains(t, err.Error(), "already in progress")
	require.Empty(t, got.Manifests)
}

// TestSyncRefusesConcurrentCalls: a second call while one is running is
// refused immediately rather than queued behind a mail fetch.
func TestSyncRefusesConcurrentCalls(t *testing.T) {
	runner := &fakeSyncer{
		entered:   make(chan struct{}),
		release:   make(chan struct{}),
		manifests: []pipeline.Manifest{{Account: "personal"}},
	}
	s := newFixture(t).server(runner)

	done := make(chan error, 1)
	go func() {
		_, err := s.sync(context.Background(), SyncInput{})
		done <- err
	}()

	select {
	case <-runner.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first sync never started")
	}

	_, err := s.sync(context.Background(), SyncInput{})
	require.ErrorIs(t, err, ErrSyncInProgress)

	close(runner.release)
	require.NoError(t, <-done)

	// And the slot is released, so the next call goes through.
	runner.entered, runner.release = nil, nil
	_, err = s.sync(context.Background(), SyncInput{})
	require.NoError(t, err)
}

// TestSyncReportsProviderFailureWithoutDiscardingManifests: a cycle that died
// partway still created files, and the caller must not lose track of them. So
// a provider failure is recorded alongside the manifests, not raised in place
// of them.
func TestSyncReportsProviderFailureWithoutDiscardingManifests(t *testing.T) {
	s := newFixture(t).server(&fakeSyncer{
		manifests: []pipeline.Manifest{{
			Account: "personal",
			Stored:  []pipeline.Entry{{Path: "/mail/jobs/2026/07/a.eml"}},
			Error:   "token expired",
		}},
		err: errors.New("account \"personal\": token expired"),
	})

	got, err := s.sync(context.Background(), SyncInput{})
	require.NoError(t, err, "a provider failure is data, not a tool error")
	require.Contains(t, got.Error, "token expired")
	require.Len(t, got.Manifests, 1)
	require.Len(t, got.Manifests[0].Stored, 1)
}

// TestSyncWithoutARunnerFails cleanly rather than panicking.
func TestSyncWithoutARunnerFails(t *testing.T) {
	f := newFixture(t)
	s, err := New(Options{
		Config:    f.cfg,
		Logger:    discardLogger(),
		NewRunner: func(bool) (Syncer, error) { return nil, nil },
	})
	require.NoError(t, err)

	_, err = s.sync(context.Background(), SyncInput{})
	require.ErrorContains(t, err, "cannot start a sync")
}

// TestSyncNeverReturnsNilManifests: the field is an array on the wire even
// when nothing ran, so a caller can iterate it unconditionally.
func TestSyncNeverReturnsNilManifests(t *testing.T) {
	s := newFixture(t).server(&fakeSyncer{})

	got, err := s.sync(context.Background(), SyncInput{})
	require.NoError(t, err)
	require.NotNil(t, got.Manifests)
	require.Contains(t, render(t, got), `"manifests":[]`)
}

// TestDefaultRunnerIsAPipelineRunner: with no NewRunner override the server
// builds the real thing over the config, so `sync` in production goes through
// exactly the cycle `run` does.
func TestDefaultRunnerIsAPipelineRunner(t *testing.T) {
	f := newFixture(t)
	s, err := New(Options{Config: f.cfg, Logger: discardLogger()})
	require.NoError(t, err)

	runner, err := s.runner(true)
	require.NoError(t, err)
	real, ok := runner.(*pipeline.Runner)
	require.True(t, ok, "the default runner must be a *pipeline.Runner")
	require.True(t, real.DryRun())

	live, err := s.runner(false)
	require.NoError(t, err)
	require.False(t, live.(*pipeline.Runner).DryRun())
}
