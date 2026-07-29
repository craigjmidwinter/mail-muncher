package gmail

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/craigjmidwinter/mail-muncher/internal/provider"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
)

// fullScan is the enumerate-the-mailbox path: it is what a first-ever run does
// (firstEver), and what any run falls back to when the history cursor has
// expired (a *recovery* scan).
//
// The distinction matters for correctness, not just cost. On a recovery scan the
// full scan is standing in for the incremental path, so it has to enumerate the
// same population users.history.list would have — see scanQuery and listIDs.
func (p *Provider) fullScan(ctx context.Context, out provider.SyncState, firstEver bool, fn func(provider.RawMessage) error) (provider.SyncState, error) {
	// Sample *both* watermarks before listing anything.
	//
	// Reading the mailbox cursor first means anything that arrives while the
	// scan is running still shows up in the next incremental run; the seen-set
	// absorbs the small overlap. Taking the cursor afterwards would silently
	// skip those messages.
	//
	// The completion timestamp has to be sampled with it, not at the end. The
	// two are one pair: HistoryID is the primary cursor and LastSyncTime is the
	// bound used once HistoryID expires, so a LastSyncTime describing a *later*
	// instant than HistoryID opens a window of mail neither watermark covers —
	// a message that lands mid-scan is missed by this run, then excluded by the
	// `after:` bound of the recovery scan that replaces the expired cursor, and
	// that scan then installs a fresh cursor and passes over it forever.
	scanStart := p.now()

	historyID, err := p.profileHistoryID(ctx)
	if err != nil {
		return out, err
	}

	ids, err := p.listIDs(ctx, p.scanQuery(out, firstEver))
	if err != nil {
		return out, err
	}
	if err := p.download(ctx, ids, &out, fn); err != nil {
		return out, err
	}

	out.HistoryID = historyID
	out.LastSyncTime = scanStart
	return out, nil
}

// scanQuery builds the `q` for a full scan: an `after:` bound, and on a
// first-ever run the account's configured query as well.
//
// The bound is InitialLookback before now for a first-ever run — so a first run
// does not trawl a decade of mail — and the previous successful sync, less
// RecoveryOverlap, for a recovery scan. With neither, the scan is unbounded,
// which is the safe direction.
//
// `gmail.query` is deliberately *not* applied to a recovery scan. Gmail does not
// query-filter users.history.list, so the incremental path delivers every new
// message and lets the local filter engine decide what is kept; the account
// query is only ever a cost bound on how much we enumerate. Applying it to a
// recovery scan would break that: a wanted message outside the query would be
// omitted from the scan, and the scan then installs the current profile cursor,
// so the message is never enumerated again and never reported — a prefilter
// meant to save API calls would have caused silent data loss. A first-ever run
// has no prior coverage to preserve and the user asked for the bound explicitly,
// so there the query is what it says on the tin.
//
// The account query is parenthesised when combined, so an `OR` inside it cannot
// swallow the `after:` term.
func (p *Provider) scanQuery(state provider.SyncState, firstEver bool) string {
	var query string
	if firstEver {
		query = strings.TrimSpace(p.opts.Query)
	}
	after := p.scanAfter(state, firstEver)
	switch {
	case after.IsZero():
		return query
	case query == "":
		return fmt.Sprintf("after:%d", after.Unix())
	default:
		return fmt.Sprintf("(%s) after:%d", query, after.Unix())
	}
}

// scanAfter is the lower time bound of a full scan, or the zero time when the
// scan should be unbounded.
//
// A recovery scan backs the stored watermark off by RecoveryOverlap before using
// it, because the watermark is only as trustworthy as the clock that wrote it.
// A recovery with no watermark at all (a cursor restored from a state file that
// predates LastSyncTime, say) is left unbounded rather than silently borrowing
// the first-run lookback: too many API calls is a cost, too few is data loss.
func (p *Provider) scanAfter(state provider.SyncState, firstEver bool) time.Time {
	if !state.LastSyncTime.IsZero() {
		return state.LastSyncTime.UTC().Add(-RecoveryOverlap)
	}
	if firstEver && p.opts.InitialLookback > 0 {
		return p.now().Add(-p.opts.InitialLookback)
	}
	return time.Time{}
}

// listIDs walks users.messages.list, following nextPageToken to the end, and
// returns the message ids in the order Gmail gave them (newest first),
// deduplicated defensively across pages.
//
// includeSpamTrash carries the account's `gmail.include_spam_trash` — nothing
// more and nothing less. It is not a decision this function gets to make on its
// own, because users.messages.list and users.history.list must enumerate the
// same population.
//
// The two endpoints are asymmetric: messages.list hides Spam and Trash unless
// asked, while history.list has no such notion and reports everything added
// anywhere in the mailbox. Left to their defaults they enumerate different
// mailboxes, and the difference is silent data loss in one direction — a wanted
// message misfiled into Spam is delivered by an incremental run, skipped by the
// recovery scan that replaces an expired cursor, and skipped for good, because
// that scan installs a fresh cursor on the way out. So whichever way the setting
// points, both routes must point the same way: this flag is the full scan's half
// and excludedBySpamTrash is history's.
//
// Excluding by default is the safe direction here, and unusually the safe
// direction is also the smaller one. mail-muncher feeds an AI agent, so
// delivered mail lands in an LLM's context window, and Spam is the one folder
// whose contents are hostile by construction. See config.GmailConfig.
func (p *Provider) listIDs(ctx context.Context, query string) ([]string, error) {
	var (
		ids       []string
		seen      = make(map[string]struct{})
		pageToken string
		pages     int
	)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token := pageToken
		resp, err := provider.RetryValue(ctx, p.opts.Retry, retryable,
			func(ctx context.Context) (*gmailapi.ListMessagesResponse, error) {
				call := p.svc.Users.Messages.List(userID).
					MaxResults(p.opts.PageSize).
					IncludeSpamTrash(p.opts.IncludeSpamTrash).
					Context(ctx)
				if query != "" {
					call = call.Q(query)
				}
				if token != "" {
					call = call.PageToken(token)
				}
				return call.Do()
			})
		if err != nil {
			return nil, fmt.Errorf("gmail: list messages for account %q (q=%q): %w", p.account, query, err)
		}
		pages++

		for _, msg := range resp.Messages {
			if msg == nil || msg.Id == "" {
				continue
			}
			if _, dup := seen[msg.Id]; dup {
				continue
			}
			seen[msg.Id] = struct{}{}
			ids = append(ids, msg.Id)
		}

		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

	slog.Debug("gmail full scan listed messages",
		"account", p.account, "query", query, "pages", pages, "ids", len(ids))
	return ids, nil
}

// profileHistoryID reads the mailbox's current history cursor.
func (p *Provider) profileHistoryID(ctx context.Context) (uint64, error) {
	profile, err := provider.RetryValue(ctx, p.opts.Retry, retryable,
		func(ctx context.Context) (*gmailapi.Profile, error) {
			return p.svc.Users.GetProfile(userID).Context(ctx).Do()
		})
	if err != nil {
		return 0, fmt.Errorf("gmail: read profile for account %q: %w", p.account, err)
	}
	return profile.HistoryId, nil
}

// fetchResult is one worker's outcome. The id is carried alongside the message
// because on the failure paths there is no message to read it off.
type fetchResult struct {
	id  string
	msg provider.RawMessage
	err error
}

// download fetches every id that is not already in the seen-set with
// format=RAW, using a bounded worker pool, and hands each message to fn.
//
// fn is called from a single goroutine, so sinks need no locking of their own.
// The first error (a download failure or fn's own) cancels the pool, drains it,
// and is returned; ids delivered before that stay marked seen in state.
//
// The one exception is a message that has been deleted since it was listed: its
// 404 is skipped rather than returned, so the cycle completes and the cursor
// advances past mail that no longer exists. Both routes reach this function, so
// both get that behaviour — a message can disappear between users.history.list
// and the download exactly as it can between users.messages.list and the
// download. See errMessageVanished for why this case, and only this case, is
// benign.
//
// A vanished id is deliberately not marked seen, for the same reason an excluded
// one is not: the seen-set records what was delivered. Nothing re-fetches it
// either — the cursor has moved past it, and no list call can name a message
// that no longer exists.
//
// This is also where `gmail.include_spam_trash: false` bites on the incremental
// route: a message that comes back wearing SPAM or TRASH is dropped instead of
// being passed to fn. Both routes run through here, so the predicate applies to
// both — see excludedBySpamTrash.
//
// A dropped message is deliberately *not* marked seen. The seen-set is the
// record of what was delivered, and leaving excluded ids out of it is what keeps
// the two routes' persisted state identical: the full scan never learns those
// ids exist (the server filtered them), so if the history route recorded them
// the two would diverge, and flipping the key to true later would find the
// history route's ids already "seen" and never deliver them. Nothing re-fetches
// them in a loop either — the history cursor advances past a message whatever we
// decide about it, and a later full scan excludes them server-side.
//
// The cost is one users.messages.get per excluded message per time it is
// enumerated. That is the price of history.list not having an includeSpamTrash
// parameter: the labels are only knowable once the message is in hand. It is
// paid in API calls, never in delivered mail.
func (p *Provider) download(ctx context.Context, ids []string, state *provider.SyncState, fn func(provider.RawMessage) error) error {
	pending := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || state.Seen(id) {
			continue
		}
		pending = append(pending, id)
	}
	if len(pending) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// One labels.list per run, before any worker needs it, so the workers only
	// ever read the cache.
	if err := p.loadLabels(ctx); err != nil {
		return err
	}

	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	workers := p.opts.Concurrency
	if workers > len(pending) {
		workers = len(pending)
	}

	idCh := make(chan string)
	resCh := make(chan fetchResult)

	go func() {
		defer close(idCh)
		for _, id := range pending {
			select {
			case idCh <- id:
			case <-ctx.Done():
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range idCh {
				msg, err := p.getRaw(ctx, id)
				select {
				case resCh <- fetchResult{id: id, msg: msg, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(resCh)
	}()

	var (
		firstErr error
		excluded int
		vanished int
	)
	for res := range resCh {
		if firstErr != nil {
			// Keep draining so the workers can finish and the pool can close.
			continue
		}
		if res.err != nil {
			if errors.Is(res.err, errMessageVanished) {
				// The one download failure that is not a failure to download:
				// the message was listed and has since been deleted. Aborting
				// on it would pin the cursor on a window that can only ever
				// produce the same 404, forever — see errMessageVanished.
				vanished++
				slog.Warn("gmail message was listed but no longer exists; skipping it",
					"account", p.account, "id", res.id,
					"detail", "it was deleted between the listing and the download; the cycle continues and the sync cursor advances past it")
				continue
			}
			firstErr = res.err
			cancel()
			continue
		}
		if p.excludedBySpamTrash(res.msg) {
			excluded++
			continue
		}
		if err := fn(res.msg); err != nil {
			firstErr = err
			cancel()
			continue
		}
		state.MarkSeen(res.msg.ID)
	}

	// Excluded mail is reported here and nowhere else. It never reached the
	// pipeline, so it is not in the run summary's `fetched` — and it could not
	// be, honestly: on a full scan the exclusion happens inside Gmail and the
	// count is not knowable at all. A counter only the incremental route could
	// fill would misreport the very symmetry this setting exists to preserve.
	if excluded > 0 {
		slog.Info("gmail excluded spam/trash from this fetch",
			"account", p.account, "excluded", excluded,
			"hint", "set gmail.include_spam_trash: true to fetch them")
	}

	// Vanished mail *is* counted for the run, unlike excluded mail: it is
	// symmetric across both routes (a message can disappear between either list
	// call and the download), and it is the one thing an operator needs to see
	// to tell "nothing arrived" apart from "something arrived and then went
	// away before we could read it". Fetch resets the counter, so it describes
	// this cycle only.
	if vanished > 0 {
		p.vanished.Add(int64(vanished))
	}

	if firstErr != nil {
		return firstErr
	}
	return parent.Err()
}

// getRaw downloads one message as RFC822 bytes and shapes it into a
// provider.RawMessage: label ids resolved to names, internalDate (epoch ms)
// turned into a UTC time, threadId carried through.
//
// This is the one place a message is downloaded: both the full scan and the
// incremental history path reach it through download, so the thread id comes
// along on both routes and neither pays an extra API call for it —
// users.messages.get returns `threadId` on the same response as `raw`.
func (p *Provider) getRaw(ctx context.Context, id string) (provider.RawMessage, error) {
	msg, err := provider.RetryValue(ctx, p.opts.Retry, retryable,
		func(ctx context.Context) (*gmailapi.Message, error) {
			return p.svc.Users.Messages.Get(userID, id).Format("RAW").Context(ctx).Do()
		})
	if err != nil {
		if messageGone(err) {
			return provider.RawMessage{}, fmt.Errorf("%w: get message %s for account %q: %w",
				errMessageVanished, id, p.account, err)
		}
		return provider.RawMessage{}, fmt.Errorf("gmail: get message %s for account %q: %w", id, p.account, err)
	}

	raw, err := decodeRaw(msg.Raw)
	if err != nil {
		return provider.RawMessage{}, fmt.Errorf("gmail: message %s: %w", id, err)
	}

	out := provider.RawMessage{
		ID:       msg.Id,
		ThreadID: strings.TrimSpace(msg.ThreadId),
		Raw:      raw,
		Labels:   p.labelNames(msg.LabelIds),
	}
	if out.ID == "" {
		out.ID = id
	}
	if msg.InternalDate != 0 {
		out.InternalDate = time.UnixMilli(msg.InternalDate).UTC()
	}
	return out, nil
}

// errMessageVanished marks the one users.messages.get failure that is not a
// failure to fetch: a 404 for a message a list call had just named, because it
// was deleted between the listing and the download.
//
// The distinction is the difference between a cycle that recovers and one that
// cannot. Every other download failure aborts the fetch on purpose — a
// *possibly* available message must never be passed over, because the cursor
// would advance past mail that was never delivered, and silent mail loss is the
// worst thing this tool can do. A 404 on a specific message is the opposite
// evidence: the message is authoritatively gone, no retry and no later cycle can
// produce it, and refusing to advance turns one deleted message into a permanent
// wedge — the same window is re-listed, the same id 404s, and the cursor never
// moves again. So this one case is skipped, loudly, and counted.
//
// It stays inside this package: download consumes it, so it never reaches a
// caller and never needs to be part of the public API.
var errMessageVanished = errors.New("gmail: message no longer exists")

// reasonNotFound is the `reason` Gmail puts on a 404 error item.
const reasonNotFound = "notFound"

// messageGone reports whether an error from users.messages.get means that
// particular message no longer exists.
//
// It is deliberately narrow: a 404 status, or Gmail's `notFound` reason on the
// error body. 401, 403, 429, 5xx, transport failures and context cancellation
// are all "we could not read it *this time*" and keep their existing
// abort-the-cycle behaviour. Widening this predicate would convert a recoverable
// stall into silent mail loss, so it must only ever mean "this message is gone".
//
// It is never applied to users.history.list, whose 404 means something else
// entirely — an expired cursor — and is handled by isNotFound/ErrHistoryExpired.
func messageGone(err error) bool {
	var gerr *googleapi.Error
	if !errors.As(err, &gerr) {
		return false
	}
	if gerr.Code == http.StatusNotFound {
		return true
	}
	for _, item := range gerr.Errors {
		if item.Reason == reasonNotFound {
			return true
		}
	}
	return false
}

// rawCleaner strips the line breaks some responses wrap the base64 payload in.
var rawCleaner = strings.NewReplacer("\n", "", "\r", "", "\t", "", " ", "")

// decodeRaw decodes the `raw` field of a format=RAW message.
//
// Gmail documents it as base64url, unpadded. Padding, embedded newlines and the
// standard (+/) alphabet are all tolerated, because none of them change the
// bytes and a decode failure here would drop a real message.
func decodeRaw(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, errors.New("format=RAW response carried no `raw` field")
	}
	cleaned := strings.TrimRight(rawCleaner.Replace(encoded), "=")

	if decoded, err := base64.RawURLEncoding.DecodeString(cleaned); err == nil {
		return decoded, nil
	}
	decoded, err := base64.RawStdEncoding.DecodeString(cleaned)
	if err != nil {
		return nil, fmt.Errorf("decode base64url `raw`: %w", err)
	}
	return decoded, nil
}
