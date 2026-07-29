package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/craigmidwinter/mail-muncher/internal/provider"
)

// Quarantine file permissions. A quarantined message is a whole email sitting
// outside its destination tree, so it is owner-only like the state directory.
const (
	quarantineDirPerm  os.FileMode = 0o700
	quarantineFilePerm os.FileMode = 0o600
)

// quarantineReason names the stage a quarantined message failed at.
const (
	quarantineReasonParse = "parse"
	quarantineReasonSink  = "sink"
)

// quarantine parks messages the cycle could not deliver.
//
// It exists because the alternatives are both unacceptable: dropping the
// message loses mail the agent asked for, and refusing to advance the cursor
// lets one permanently broken message block every message behind it forever.
// Writing the raw bytes somewhere durable first makes advancing safe — the
// message still exists, in full, and an operator can act on it later.
//
// Layout is one directory per account:
//
//	<quarantine_dir>/<account>/<id>.eml    the raw RFC822 bytes, verbatim
//	<quarantine_dir>/<account>/<id>.json   why it is here
type quarantine struct {
	// dir is the quarantine root, config.Config.QuarantineRoot().
	dir string
	// now is the clock stamped into the sidecar. Nil means time.Now.
	now func() time.Time
}

// quarantineRecord is the `.json` sidecar written beside each `.eml`. It is
// deliberately self-contained: an operator (or an agent) sweeping the
// quarantine directory must not need the run's logs to know what happened.
type quarantineRecord struct {
	ID            string    `json:"id"`
	Account       string    `json:"account"`
	Rule          string    `json:"rule,omitempty"`
	ThreadID      string    `json:"thread_id,omitempty"`
	Reason        string    `json:"reason"`
	Error         string    `json:"error"`
	QuarantinedAt time.Time `json:"quarantined_at"`
	Bytes         int       `json:"bytes"`
	EML           string    `json:"eml"`
}

// path is where a message's raw bytes go. It is deterministic, so re-running a
// cycle that fails the same way overwrites rather than accumulating copies.
func (q *quarantine) path(account, id string) string {
	return filepath.Join(q.dir, safeSegment(account), safeSegment(id)+".eml")
}

// write stores the raw message and its sidecar, and returns the `.eml` path.
//
// The `.eml` is written first and both writes are checked: a caller that gets
// an error here must not let the cursor advance, because the message would
// then exist nowhere at all.
func (q *quarantine) write(account string, raw provider.RawMessage, rule, reason string, cause error) (string, time.Time, error) {
	at := q.clock()
	emlPath := q.path(account, raw.ID)

	if err := os.MkdirAll(filepath.Dir(emlPath), quarantineDirPerm); err != nil {
		return "", at, fmt.Errorf("quarantine: create %s: %w", filepath.Dir(emlPath), err)
	}
	if err := os.WriteFile(emlPath, raw.Raw, quarantineFilePerm); err != nil {
		return "", at, fmt.Errorf("quarantine: write %s: %w", emlPath, err)
	}

	record := quarantineRecord{
		ID:            raw.ID,
		Account:       account,
		Rule:          rule,
		ThreadID:      raw.ThreadID,
		Reason:        reason,
		QuarantinedAt: at,
		Bytes:         len(raw.Raw),
		EML:           emlPath,
	}
	if cause != nil {
		record.Error = cause.Error()
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", at, fmt.Errorf("quarantine: encode sidecar for %s: %w", raw.ID, err)
	}
	sidecar := strings.TrimSuffix(emlPath, ".eml") + ".json"
	if err := os.WriteFile(sidecar, append(data, '\n'), quarantineFilePerm); err != nil {
		return "", at, fmt.Errorf("quarantine: write %s: %w", sidecar, err)
	}
	return emlPath, at, nil
}

func (q *quarantine) clock() time.Time {
	if q.now != nil {
		return q.now().UTC()
	}
	return time.Now().UTC()
}

// safeSegment turns an account name or a provider message id into one safe path
// segment. Provider ids are opaque strings — an IMAP id is
// `account:mailbox:uidvalidity:uid` — and nothing downstream may be able to
// escape the quarantine root, so anything outside [A-Za-z0-9._-] becomes "_".
func safeSegment(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		return "unknown"
	}
	return out
}
