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
