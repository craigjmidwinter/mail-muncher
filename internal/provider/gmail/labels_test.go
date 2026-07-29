package gmail

import (
	"context"
	"net/http"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	gmailapi "google.golang.org/api/gmail/v1"
)

// labelSet turns an id -> name map into the API's label list, in a stable order
// so the canned responses are deterministic.
func labelSet(byID map[string]string) []*gmailapi.Label {
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	labels := make([]*gmailapi.Label, 0, len(ids))
	for _, id := range ids {
		labels = append(labels, &gmailapi.Label{Id: id, Name: byID[id]})
	}
	return labels
}

func TestLabelNamesResolvesIDsInOrder(t *testing.T) {
	stub := newGmailStub()
	stub.labels = labelSet(map[string]string{
		"INBOX":     "INBOX",
		"Label_7":   "Receipts",
		"Label_8":   "Job search/Recruiters",
		"CATEGORY_": "",
	})

	p := newTestProvider(t, stub, FetchOptions{})
	require.NoError(t, p.loadLabels(context.Background()))

	require.Equal(t,
		[]string{"Job search/Recruiters", "INBOX", "Receipts"},
		p.labelNames([]string{"Label_8", "INBOX", "Label_7"}))
	require.Nil(t, p.labelNames(nil))
	require.Equal(t, []string{"Label_999"}, p.labelNames([]string{"Label_999"}),
		"an unknown id passes through rather than being dropped")
}

func TestLoadLabelsIsCached(t *testing.T) {
	stub := newGmailStub()
	stub.labels = labelSet(map[string]string{"INBOX": "INBOX"})

	p := newTestProvider(t, stub, FetchOptions{})
	for i := 0; i < 3; i++ {
		require.NoError(t, p.loadLabels(context.Background()))
	}
	stub.snapshot(func(s *gmailStub) {
		require.Equal(t, 1, s.labelCalls)
	})
}

func TestLoadLabelsFailureIsReported(t *testing.T) {
	stub := newGmailStub()
	stub.addListPage("", "", "m1")

	// Serve a 401 for labels by pointing the provider at a handler that fails
	// that route only.
	srvStub := stub
	base := srvStub.handler()
	p := newTestProviderWithHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gmail/v1/users/me/labels" {
			writeAPIError(w, http.StatusUnauthorized, "authError", "invalid credentials")
			return
		}
		base.ServeHTTP(w, r)
	}), FetchOptions{})

	err := p.loadLabels(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "list labels for account \"work\"")
}
