package gmail

import (
	"context"
	"fmt"
	"sync"

	"github.com/craigjmidwinter/mail-muncher/internal/provider"
	gmailapi "google.golang.org/api/gmail/v1"
)

// labelCache holds one account's label id -> name mapping.
//
// Messages carry label *ids* (`INBOX`, `Label_17`), but everything downstream —
// rules, filenames, logs — wants the human name the user sees in Gmail. The
// mapping barely changes, so it is fetched once per run and reused.
type labelCache struct {
	mu     sync.RWMutex
	byID   map[string]string
	loaded bool
}

// loadLabels populates the label cache, once per Provider. It runs before the
// download pool starts, so the workers only ever read.
func (p *Provider) loadLabels(ctx context.Context) error {
	p.labels.mu.RLock()
	loaded := p.labels.loaded
	p.labels.mu.RUnlock()
	if loaded {
		return nil
	}

	resp, err := provider.RetryValue(ctx, p.opts.Retry, retryable,
		func(ctx context.Context) (*gmailapi.ListLabelsResponse, error) {
			return p.svc.Users.Labels.List(userID).Context(ctx).Do()
		})
	if err != nil {
		return fmt.Errorf("gmail: list labels for account %q: %w", p.account, err)
	}

	byID := make(map[string]string, len(resp.Labels))
	for _, label := range resp.Labels {
		if label == nil || label.Id == "" || label.Name == "" {
			continue
		}
		byID[label.Id] = label.Name
	}

	p.labels.mu.Lock()
	p.labels.byID = byID
	p.labels.loaded = true
	p.labels.mu.Unlock()
	return nil
}

// LabelSpam and LabelTrash are Gmail's system labels for the two folders
// `gmail.include_spam_trash` governs. Gmail gives system labels the same string
// for id and name, so these match whether or not the label cache resolved them.
const (
	LabelSpam  = "SPAM"
	LabelTrash = "TRASH"
)

// excludedBySpamTrash reports whether a downloaded message must be dropped
// because it lives in Spam or Trash and the account did not opt into those.
//
// This is the incremental route's half of `gmail.include_spam_trash`.
// users.history.list has no includeSpamTrash parameter — it reports everything
// added anywhere in the mailbox — so the only place the two routes can be made
// to agree is after the download, once labels are known. The full scan gets the
// same treatment even though its server-side filter has already done the work:
// running one predicate on both routes is what makes "identical population" a
// property of the code rather than of a comment.
//
// It reads the *resolved* names on the RawMessage rather than the raw label ids,
// so it is exactly the test a user would write as a rule (`not: {label: SPAM}`).
// The two mechanisms are deliberately the same test at two different stages.
//
// A message dropped here is not an error and not a parse failure: it was never
// delivered, so nothing downstream ever sees it. It is also deliberately *not*
// added to the seen-set — see download.
func (p *Provider) excludedBySpamTrash(msg provider.RawMessage) bool {
	if p.opts.IncludeSpamTrash {
		return false
	}
	for _, name := range msg.Labels {
		if name == LabelSpam || name == LabelTrash {
			return true
		}
	}
	return false
}

// labelNames resolves label ids to names, preserving order. An id with no known
// name (a label created after the cache was filled) passes through unchanged —
// a slightly ugly name beats losing the label.
func (p *Provider) labelNames(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	p.labels.mu.RLock()
	defer p.labels.mu.RUnlock()

	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if name, ok := p.labels.byID[id]; ok {
			names = append(names, name)
			continue
		}
		names = append(names, id)
	}
	return names
}
