package gmail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/craigmidwinter/mail-muncher/internal/provider"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
)

// historyTypeMessageAdded is the only history change we care about: mail
// arriving. Label churn and deletions do not create work for mail-muncher,
// which never writes back to the mailbox.
const historyTypeMessageAdded = "messageAdded"

// ErrHistoryExpired reports that the stored history cursor is older than the
// window Gmail keeps (roughly a week), so users.history.list answered 404.
// Fetch handles this itself by clearing the cursor and doing a full scan; the
// error is exported so callers can recognise it in wrapped errors.
var ErrHistoryExpired = errors.New("gmail: history id expired")

// historyIDs walks users.history.list from startHistoryID and returns the ids
// of messages added since, in first-seen order, along with the cursor the next
// run should start from.
//
// The returned cursor is the highest history id observed — across the records
// themselves and the `historyId` each page reports — so a run that finds
// nothing still advances the cursor instead of re-asking the same question
// forever.
//
// Gmail does not filter history by the account's `q`, and does not hide Spam or
// Trash from it either; that is by design here. The server query is an
// optimization for the first-ever scan only, and the local filter engine remains
// the authority on what is kept.
//
// The full-scan path is matched to this population deliberately — it drops the
// account query on a recovery scan and always passes includeSpamTrash — because
// a recovery scan stands in for this one. Any message this route would deliver
// but that route would not is a message lost for good once the recovery installs
// a fresh cursor. See scanQuery and listIDs.
//
// Only ids come back from here. The bodies — and with them the `threadId` —
// are fetched by the same download/getRaw path a full scan uses, so the
// incremental route needs no thread lookup of its own. History records do carry
// a threadId, but reading it here would put the field in two places.
func (p *Provider) historyIDs(ctx context.Context, startHistoryID uint64) ([]string, uint64, error) {
	var (
		ids       []string
		seen      = make(map[string]struct{})
		pageToken string
		pages     int
	)
	maxHistoryID := startHistoryID

	for {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		token := pageToken
		resp, err := provider.RetryValue(ctx, p.opts.Retry, retryable,
			func(ctx context.Context) (*gmailapi.ListHistoryResponse, error) {
				call := p.svc.Users.History.List(userID).
					StartHistoryId(startHistoryID).
					HistoryTypes(historyTypeMessageAdded).
					MaxResults(p.opts.PageSize).
					Context(ctx)
				if token != "" {
					call = call.PageToken(token)
				}
				return call.Do()
			})
		if err != nil {
			if isNotFound(err) {
				return nil, 0, fmt.Errorf("%w (start=%d): %w", ErrHistoryExpired, startHistoryID, err)
			}
			return nil, 0, fmt.Errorf("gmail: list history for account %q (start=%d): %w",
				p.account, startHistoryID, err)
		}
		pages++

		for _, record := range resp.History {
			if record == nil {
				continue
			}
			if record.Id > maxHistoryID {
				maxHistoryID = record.Id
			}
			for _, added := range record.MessagesAdded {
				if added == nil || added.Message == nil || added.Message.Id == "" {
					continue
				}
				id := added.Message.Id
				if _, dup := seen[id]; dup {
					continue
				}
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
		if resp.HistoryId > maxHistoryID {
			maxHistoryID = resp.HistoryId
		}

		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

	slog.Debug("gmail incremental sync read history",
		"account", p.account, "start_history_id", startHistoryID,
		"next_history_id", maxHistoryID, "pages", pages, "added", len(ids))
	return ids, maxHistoryID, nil
}

// isNotFound reports whether err is a Gmail 404. For users.history.list that
// means one thing only: the startHistoryId has aged out.
func isNotFound(err error) bool {
	var gerr *googleapi.Error
	return errors.As(err, &gerr) && gerr.Code == http.StatusNotFound
}
