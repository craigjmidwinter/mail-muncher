package provider

import (
	"context"
	"time"
)

// MaxSeenIDs bounds the recently-seen message-id set carried in SyncState.
// The set is FIFO: once it is full, marking a new id evicts the oldest one.
const MaxSeenIDs = 2000

// RawMessage is one message as the provider delivered it: the untouched RFC822
// bytes plus the handful of provider metadata fields the pipeline needs before
// it has parsed anything.
type RawMessage struct {
	// ID is a provider-scoped stable identifier. It is used for dedup and, by
	// the sinks, to derive idempotent filenames, so it must be stable across
	// runs (Gmail: the message id; IMAP: account:mailbox:uidvalidity:uid).
	ID string
	// ThreadID is the provider's own conversation id, when it has one (Gmail:
	// `threadId`, returned on the same users.messages.get response as `raw`, so
	// it costs no extra call). Providers with no native threading (IMAP) leave
	// it empty and model.Parse synthesizes one from the message's threading
	// headers — see model.Message.ThreadID.
	ThreadID string
	// Raw is the complete RFC822 message, byte-faithful.
	Raw []byte
	// InternalDate is the time the provider received the message (not the
	// Date: header, which the sender controls).
	InternalDate time.Time
	// Labels are provider-side labels/folders resolved to human names
	// (Gmail label names; IMAP mailbox names).
	Labels []string
}

// SyncState is the per-account cursor a provider hands back after a fetch, so
// the next run can pick up where this one stopped. It is JSON-serializable and
// persisted verbatim by internal/state.
//
// Providers should treat fields they do not own as opaque and carry them
// through unchanged.
type SyncState struct {
	// HistoryID is the Gmail users.history cursor. Zero means "no incremental
	// cursor", which forces a full scan.
	HistoryID uint64 `json:"history_id,omitempty"`
	// LastSyncTime is when the last successful fetch completed. Providers that
	// have no cursor of their own use it to bound their query.
	LastSyncTime time.Time `json:"last_sync_time"`
	// Extra is free-form per-provider state. Keys are namespaced by the
	// provider, e.g. the IMAP provider stores one pair per mailbox:
	//
	//	imap.INBOX.uidvalidity = "1650000000"
	//	imap.INBOX.last_uid    = "48213"
	Extra map[string]string `json:"extra,omitempty"`
	// SeenIDs is a bounded FIFO set of recently delivered RawMessage.IDs,
	// oldest first. It is belt-and-braces dedup alongside the sinks' idempotent
	// filenames: providers may skip ids already in it, and the pipeline marks
	// each stored message with MarkSeen.
	SeenIDs []string `json:"seen_ids,omitempty"`
}

// Provider is the seam that keeps the pipeline provider-agnostic: everything
// downstream sees RawMessage and SyncState, never a provider SDK type.
type Provider interface {
	// Name is the provider's short identifier, e.g. "gmail" or "imap".
	Name() string
	// Fetch streams messages newer than state via fn and returns the updated
	// state. fn returning an error aborts the fetch; the state accumulated up
	// to that point is returned along with the error.
	//
	// Implementations must respect ctx cancellation.
	Fetch(ctx context.Context, state SyncState, fn func(RawMessage) error) (SyncState, error)
}

// Seen reports whether id is in the recently-seen set.
func (s *SyncState) Seen(id string) bool {
	for _, seen := range s.SeenIDs {
		if seen == id {
			return true
		}
	}
	return false
}

// MarkSeen adds id to the recently-seen set, evicting the oldest entries once
// the set exceeds MaxSeenIDs. Marking an id that is already present is a no-op,
// so an id keeps its original position in the FIFO.
func (s *SyncState) MarkSeen(id string) {
	if id == "" || s.Seen(id) {
		return
	}
	s.SeenIDs = append(s.SeenIDs, id)
	s.SeenIDs = TrimSeenIDs(s.SeenIDs)
}

// TrimSeenIDs drops the oldest entries until at most MaxSeenIDs remain.
func TrimSeenIDs(ids []string) []string {
	if len(ids) <= MaxSeenIDs {
		return ids
	}
	trimmed := make([]string, MaxSeenIDs)
	copy(trimmed, ids[len(ids)-MaxSeenIDs:])
	return trimmed
}

// GetExtra returns the Extra value for key, or "" when unset.
func (s *SyncState) GetExtra(key string) string {
	if s.Extra == nil {
		return ""
	}
	return s.Extra[key]
}

// SetExtra sets an Extra key, allocating the map when needed. Setting an empty
// value deletes the key.
func (s *SyncState) SetExtra(key, value string) {
	if value == "" {
		delete(s.Extra, key)
		return
	}
	if s.Extra == nil {
		s.Extra = make(map[string]string)
	}
	s.Extra[key] = value
}

// Clone returns a deep copy, so a caller can hand a provider a state it may
// mutate freely without disturbing the copy on disk.
func (s SyncState) Clone() SyncState {
	out := s
	if s.Extra != nil {
		out.Extra = make(map[string]string, len(s.Extra))
		for k, v := range s.Extra {
			out.Extra[k] = v
		}
	}
	if s.SeenIDs != nil {
		out.SeenIDs = append([]string(nil), s.SeenIDs...)
	}
	return out
}
