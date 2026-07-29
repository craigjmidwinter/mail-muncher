package gmail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/provider"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// userID is the Gmail API's alias for "whoever the credentials belong to".
// mail-muncher never impersonates another mailbox, so it is the only user id
// this package ever sends.
const userID = "me"

// DefaultConcurrency is how many users.messages.get calls run at once. Gmail's
// per-user quota is generous but not unlimited, and the downloads are the only
// part of a run that is worth parallelising, so a small fixed pool is enough.
// It is deliberately not configurable yet.
const DefaultConcurrency = 4

// DefaultPageSize is the page size asked of users.messages.list and
// users.history.list. 500 is the API maximum for both.
const DefaultPageSize int64 = 500

// RecoveryOverlap is how far *before* the stored watermark a recovery full scan
// reaches back when the history cursor has expired.
//
// The two watermarks a run persists — HistoryID and LastSyncTime — are both
// sampled before the run starts listing, so LastSyncTime is never later than the
// mailbox state HistoryID describes. That alone closes the window where a
// message arriving mid-run could fall between the two. The overlap is the belt
// to that pair of braces: it absorbs clock skew between the machine and Gmail, a
// wall-clock jump (NTP step, VM resume, a container starting with a bad clock),
// and the fact that Gmail's `after:` operator is evaluated against the message's
// own date rather than the instant we observed it.
//
// Re-scanning a day of mail is close to free and always safe: the pipeline's
// seen-set skips ids already delivered, and the sinks derive deterministic
// filenames, so anything that slips past the seen-set is skipped again at write
// time. Losing a message is not recoverable at all. When those two trade off,
// this package pays the extra API calls.
const RecoveryOverlap = 24 * time.Hour

var _ provider.Provider = (*Provider)(nil)

// FetchOptions tunes one account's fetch. The zero value is usable: it means
// "no server-side query, no lookback bound, default concurrency, default
// backoff".
type FetchOptions struct {
	// Query is the account's `gmail.query`, a Gmail search expression used as a
	// server-side pre-filter. It bounds the first-ever scan only: a scan that is
	// recovering from an expired history cursor ignores it, because there the
	// prefilter would cost messages rather than API calls. See the note on
	// Provider.Fetch.
	Query string
	// InitialLookback bounds how far back the first-ever full scan reaches (no
	// stored LastSyncTime). Zero means "no bound", i.e. the whole mailbox.
	// Callers normally pass config.GmailConfig.InitialLookbackDuration.
	InitialLookback time.Duration
	// IncludeSpamTrash is the account's `gmail.include_spam_trash`. The zero
	// value — exclude — is the default, and both sync routes honour it: see
	// listIDs for the full scan and excludedBySpamTrash for the history path.
	IncludeSpamTrash bool
	// Concurrency is the size of the RAW-download worker pool. Zero means
	// DefaultConcurrency.
	Concurrency int
	// PageSize is the maxResults asked of the list endpoints. Zero means
	// DefaultPageSize; values above 500 are clamped by the server anyway.
	PageSize int64
	// Retry is the backoff applied to every Gmail call. The zero value means
	// provider.DefaultRetryConfig.
	Retry provider.RetryConfig
	// Now overrides the clock, for tests. Nil means time.Now.
	Now func() time.Time
}

// Provider fetches mail from one Gmail account. Build one with New (or
// NewFromAccount); the injectable constructors exist for tests.
//
// A Provider is safe for concurrent use by one Fetch at a time; it is not
// designed to run two overlapping Fetches.
type Provider struct {
	account string
	svc     *gmailapi.Service
	opts    FetchOptions
	labels  labelCache

	// vanished counts the messages the most recent Fetch listed and then found
	// to be gone. Fetch resets it, download adds to it, VanishedCount reports
	// it. It is atomic because the run summary is read from the caller's
	// goroutine while the download pool is the thing that fills it.
	vanished atomic.Int64
}

// VanishedCount reports how many messages the most recent Fetch listed and then
// found no longer existed — deleted between the listing and the download, so
// skipped rather than fetched.
//
// It is how a skip that must never be silent reaches the run summary. The
// pipeline reads it through a one-method interface, so nothing outside this
// package has to know it is Gmail-specific; a provider that cannot tell simply
// does not implement it. Read it after Fetch returns; it describes that Fetch
// only, including a Fetch that ended in an error.
func (p *Provider) VanishedCount() int { return int(p.vanished.Load()) }

// New builds a Provider for the account described by opts, authenticating with
// the cached OAuth token (refreshing it as needed). If `auth` has never run the
// error matches ErrNoToken and says so.
//
// ctx bounds the lifetime of the underlying HTTP client, so pass a context that
// outlives the Fetch calls made with the returned Provider.
func New(ctx context.Context, opts Options, fetchOpts FetchOptions) (*Provider, error) {
	client, err := NewClient(ctx, opts)
	if err != nil {
		return nil, err
	}
	return NewWithHTTPClient(ctx, opts.Account, client, "", fetchOpts)
}

// NewFromAccount is the pipeline's entry point: it derives both the credentials
// and the fetch settings from a validated config account.
func NewFromAccount(ctx context.Context, account *config.Account) (*Provider, error) {
	opts, err := OptionsFromAccount(account)
	if err != nil {
		return nil, err
	}
	return New(ctx, opts, FetchOptionsFromAccount(account))
}

// FetchOptionsFromAccount reads the fetch-shaping keys (`gmail.query`,
// `gmail.initial_lookback`, `gmail.include_spam_trash`) off a config account. A
// nil account, or one with no `gmail:` section, yields the zero FetchOptions —
// which excludes Spam and Trash, the same as an account that omits the key.
func FetchOptionsFromAccount(account *config.Account) FetchOptions {
	if account == nil || account.Gmail == nil {
		return FetchOptions{}
	}
	return FetchOptions{
		Query:            account.Gmail.Query,
		InitialLookback:  account.Gmail.InitialLookbackDuration(),
		IncludeSpamTrash: account.Gmail.IncludesSpamTrash(),
	}
}

// NewWithHTTPClient builds a Provider on a caller-supplied HTTP client, and
// optionally against a non-default endpoint. It is how tests drive the provider
// against an httptest server holding canned JSON, with no network and no OAuth:
//
//	srv := httptest.NewServer(handler)
//	p, err := gmail.NewWithHTTPClient(ctx, "work", srv.Client(), srv.URL, gmail.FetchOptions{})
//
// An empty endpoint means the real Gmail endpoint.
func NewWithHTTPClient(ctx context.Context, account string, client *http.Client, endpoint string, fetchOpts FetchOptions) (*Provider, error) {
	if client == nil {
		return nil, errors.New("gmail: nil HTTP client")
	}
	apiOpts := []option.ClientOption{option.WithHTTPClient(client)}
	if strings.TrimSpace(endpoint) != "" {
		apiOpts = append(apiOpts, option.WithEndpoint(endpoint))
	}
	svc, err := gmailapi.NewService(ctx, apiOpts...)
	if err != nil {
		return nil, fmt.Errorf("gmail: build API service: %w", err)
	}
	return NewWithService(account, svc, fetchOpts), nil
}

// NewWithService wraps an already-built Gmail service. It is the lowest-level
// constructor; prefer New unless you need to control the transport.
func NewWithService(account string, svc *gmailapi.Service, fetchOpts FetchOptions) *Provider {
	return &Provider{account: account, svc: svc, opts: fetchOpts.withDefaults()}
}

// Name implements provider.Provider.
func (p *Provider) Name() string { return config.ProviderGmail }

// Account is the config account name this Provider fetches for.
func (p *Provider) Account() string { return p.account }

// Fetch implements provider.Provider.
//
// It takes one of two routes:
//
//   - Incremental. state.HistoryID is set, so users.history.list is asked what
//     was added since that cursor. This is the cheap path a cron run takes.
//   - Full scan. No cursor (first-ever run) or an expired one: users.messages.list
//     enumerates the mailbox, bounded by `after:`.
//
// An expired cursor is not an error: Gmail keeps roughly a week of history, so
// a 404 from users.history.list clears HistoryID and falls through to the full
// scan **in the same run**.
//
// Either way the ids that are new (not already in state.SeenIDs) are downloaded
// with format=RAW by a bounded worker pool and handed to fn one at a time — fn
// is never called concurrently. fn returning an error aborts the fetch; the
// state accumulated so far comes back alongside the error.
//
// Note on messages that disappear mid-cycle: a listed message can be deleted
// before the download reaches it, and users.messages.get then answers 404. That
// one case is skipped rather than fatal, on both routes, so the cycle completes
// and the cursor advances; anything else — 401, 403, 429, 5xx, transport
// failures, cancellation — still aborts and still leaves the cursor where it
// was. Skips are logged at WARN per message and counted by VanishedCount.
//
// Because both routes converge on that one format=RAW download, every delivered
// RawMessage carries Gmail's `threadId` regardless of which route produced it,
// and no route pays an extra request for it.
//
// Note on the account query: `gmail.query` is a server-side *optimization for
// the first-ever scan only*. History results are not query-filtered by Gmail,
// and we deliberately do not re-apply the query client-side — the local filter
// engine is the authority on what gets kept. A scan that recovers from an
// expired cursor therefore drops the query too, so it enumerates the same
// population the incremental path would have; see fullScan for the argument.
//
// Note on Spam and Trash: `gmail.include_spam_trash` (default false) is honoured
// by both routes, by different means — a server-side flag on the full scan, a
// post-download label check on the incremental one — precisely so the two agree
// on which messages exist. See listIDs and excludedBySpamTrash.
//
// Note on the two watermarks: both HistoryID and LastSyncTime are sampled
// *before* the run lists anything, so they describe the same instant. The pair
// is the contract between the two routes — LastSyncTime is the fallback bound a
// later run uses once HistoryID has aged out — and a pair that disagreed would
// leave a window of mail neither route covers.
func (p *Provider) Fetch(ctx context.Context, state provider.SyncState, fn func(provider.RawMessage) error) (provider.SyncState, error) {
	if p.svc == nil {
		return state, errors.New("gmail: provider has no API service")
	}
	if fn == nil {
		return state, errors.New("gmail: Fetch requires a non-nil callback")
	}

	// The vanished counter describes one cycle, so it starts each Fetch at zero
	// even when the Provider is reused.
	p.vanished.Store(0)

	out := state.Clone()
	if err := ctx.Err(); err != nil {
		return out, err
	}

	// firstEver is read off the *incoming* state, before the expired-cursor
	// branch below clears HistoryID. A run that began with a cursor is a
	// recovery even when it ends up doing a full scan, and recovery scans are
	// bounded differently from a genuine first run — see fullScan.
	firstEver := state.HistoryID == 0 && state.LastSyncTime.IsZero()

	if out.HistoryID > 0 {
		// Sample the watermark *before* asking Gmail what changed, for the same
		// reason fullScan reads the profile cursor first: a message that arrives
		// while this run is in flight is not covered by it, so the timestamp the
		// next run falls back to must not have moved past it. Stamping
		// completion time here is what let mail slip between the two watermarks.
		syncStart := p.now()

		ids, nextHistoryID, err := p.historyIDs(ctx, out.HistoryID)
		switch {
		case err == nil:
			if err := p.download(ctx, ids, &out, fn); err != nil {
				return out, err
			}
			if nextHistoryID > out.HistoryID {
				out.HistoryID = nextHistoryID
			}
			out.LastSyncTime = syncStart
			return out, nil
		case errors.Is(err, ErrHistoryExpired):
			// Gmail only keeps about a week of history. Losing the cursor costs
			// a full scan, not correctness.
			slog.Warn("gmail history id expired; falling back to a full scan",
				"account", p.account, "history_id", out.HistoryID)
			out.HistoryID = 0
		default:
			return out, err
		}
	}

	return p.fullScan(ctx, out, firstEver, fn)
}

// now is the Provider's clock, always in UTC so persisted state compares
// cleanly across runs.
func (p *Provider) now() time.Time {
	if p.opts.Now != nil {
		return p.opts.Now().UTC()
	}
	return time.Now().UTC()
}

// withDefaults fills the zero fields with the package defaults.
func (o FetchOptions) withDefaults() FetchOptions {
	if o.Concurrency <= 0 {
		o.Concurrency = DefaultConcurrency
	}
	if o.PageSize <= 0 {
		o.PageSize = DefaultPageSize
	}
	// Retry needs no defaulting here: provider.Retry fills its own zero fields
	// from DefaultRetryConfig.
	return o
}

// retryable decides which Gmail failures are worth another attempt.
//
// provider.RetryableStatus covers 408/429/5xx. It deliberately stops short of
// 403, because 403 is both "you are going too fast" and "you may not do that",
// and only the error body tells them apart — so that case is handled here.
func retryable(err error) bool {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		if provider.RetryableStatus(gerr.Code) {
			return true
		}
		return gerr.Code == http.StatusForbidden && isRateLimited(gerr)
	}
	// A transport-level failure (connection reset, unexpected EOF) never
	// reaches googleapi.CheckResponse; it is still usually transient.
	var uerr *url.Error
	return errors.As(err, &uerr)
}

// isRateLimited reports whether a 403 is Gmail saying "slow down" rather than
// "no". A daily-quota exhaustion (`dailyLimitExceeded`, `quotaExceeded`) is
// deliberately excluded: retrying it inside one run cannot help.
func isRateLimited(gerr *googleapi.Error) bool {
	for _, item := range gerr.Errors {
		switch item.Reason {
		case "rateLimitExceeded", "userRateLimitExceeded", "backendError":
			return true
		}
	}
	// Newer-style errors carry no `errors[]` array; fall back to the text.
	haystack := strings.ToLower(gerr.Message + " " + gerr.Body)
	return strings.Contains(haystack, "ratelimitexceeded") ||
		strings.Contains(haystack, "rate limit exceeded")
}
