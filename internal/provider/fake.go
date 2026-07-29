package provider

import (
	"context"
	"time"
)

var _ Provider = (*Fake)(nil)

// Fake is a Provider that replays a canned slice of RawMessages. It exists so
// other packages (the pipeline, the run command) can be tested end to end
// without a network or a provider SDK.
//
// A zero Fake is usable: it is a provider named "fake" with no messages.
type Fake struct {
	// ProviderName is returned by Name(); defaults to "fake".
	ProviderName string
	// Messages are replayed, in order, on every Fetch.
	Messages []RawMessage

	// FetchErr, when non-nil, is returned after the messages have been
	// replayed — the "fetch died partway through" case, with the state
	// accumulated so far still returned.
	FetchErr error
	// FailEarly makes Fetch stop after delivering FailAfter messages and
	// return FetchErr (or a generic error), with the partial state.
	FailEarly bool
	// FailAfter is the number of messages delivered before FailEarly bites.
	FailAfter int
	// NextHistoryID, when non-zero, is written to the returned state.
	NextHistoryID uint64
	// NextExtra entries are merged into the returned state's Extra.
	NextExtra map[string]string
	// Now, when non-zero, is used as the returned LastSyncTime instead of
	// time.Now(), so tests can assert on the persisted state exactly.
	Now time.Time
	// SkipSeen makes Fetch drop messages whose ID is already in
	// state.SeenIDs, mimicking a real provider's dedup.
	SkipSeen bool

	// Observed by tests after the run.
	Calls     int
	LastState SyncState
	Delivered []string
}

// NewFake returns a Fake named name that replays msgs.
func NewFake(name string, msgs ...RawMessage) *Fake {
	return &Fake{ProviderName: name, Messages: msgs}
}

// Name implements Provider.
func (f *Fake) Name() string {
	if f.ProviderName == "" {
		return "fake"
	}
	return f.ProviderName
}

// Fetch implements Provider: it replays Messages through fn, marking each
// delivered id seen and advancing the state. If fn returns an error the replay
// stops and the state accumulated so far is returned with that error.
func (f *Fake) Fetch(ctx context.Context, state SyncState, fn func(RawMessage) error) (SyncState, error) {
	f.Calls++
	f.LastState = state.Clone()
	out := state.Clone()

	delivered := 0
	for _, msg := range f.Messages {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if f.FailEarly && delivered >= f.FailAfter {
			return out, f.fetchErr()
		}
		if f.SkipSeen && out.Seen(msg.ID) {
			continue
		}
		if err := fn(msg); err != nil {
			return out, err
		}
		delivered++
		f.Delivered = append(f.Delivered, msg.ID)
		out.MarkSeen(msg.ID)
	}

	out.LastSyncTime = f.now()
	if f.NextHistoryID != 0 {
		out.HistoryID = f.NextHistoryID
	}
	for k, v := range f.NextExtra {
		out.SetExtra(k, v)
	}
	if f.FetchErr != nil {
		return out, f.FetchErr
	}
	return out, nil
}

func (f *Fake) now() time.Time {
	if !f.Now.IsZero() {
		return f.Now
	}
	return time.Now().UTC()
}

func (f *Fake) fetchErr() error {
	if f.FetchErr != nil {
		return f.FetchErr
	}
	return errFakeFetch
}

type fakeFetchError struct{}

func (fakeFetchError) Error() string { return "provider: fake fetch failure" }

var errFakeFetch = fakeFetchError{}
