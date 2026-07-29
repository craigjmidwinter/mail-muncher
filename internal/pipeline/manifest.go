package pipeline

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/craigmidwinter/mail-muncher/internal/config"
	"github.com/craigmidwinter/mail-muncher/internal/model"
)

// Manifest is the machine-readable record of one account's cycle: what the
// cycle stored, what it found already on disk, and how much it looked at.
//
// It is the contract an agent codes against. `mail-muncher run --json` writes
// one Manifest per account to stdout, `daemon --json` writes one per account
// per tick (newline-delimited), and the MCP server's sync tool returns this
// same struct — so its JSON shape is API surface, not debug output.
//
// A Manifest is always complete enough to act on: when a fetch dies partway
// through, the manifest still lists everything that reached disk before the
// failure and carries the failure in Error.
type Manifest struct {
	// Account is the configured account name this cycle covered.
	Account string `json:"account"`
	// StartedAt is when the account's cycle began, in UTC.
	StartedAt time.Time `json:"started_at"`
	// DurationMS is how long the account's cycle took, in milliseconds.
	DurationMS int64 `json:"duration_ms"`
	// DryRun reports that nothing was written: every path in Stored is a path
	// that *would* have been written, and no state was saved.
	DryRun bool `json:"dry_run,omitempty"`

	// Stored is one entry per (message x format) written by this cycle. A rule
	// with `formats: [eml, markdown]` contributes two entries sharing an ID.
	// Never nil; an empty cycle marshals as [].
	Stored []Entry `json:"stored"`
	// Skipped is one entry per (message x format) that was already on disk, so
	// the cycle wrote nothing. These files exist and are safe to read — the
	// distinction from Stored is only whether *this* cycle created them.
	// Never nil.
	Skipped []Entry `json:"skipped"`

	// Quarantined is one entry per message this cycle could not deliver and
	// parked under the quarantine directory instead: it would not parse, or
	// every rendering its rule asked for failed to write. The cursor advanced
	// past these messages, so quarantine is where they live now — nothing else
	// will re-fetch them. Empty unless `on_message_failure: quarantine` bit.
	Quarantined []QuarantineEntry `json:"quarantined,omitempty"`

	// Degraded reports that some `from_domains_file` this cycle's rules
	// reference could not be read, so "no match" is not a trustworthy verdict
	// for any message in it. See DegradedFiles for which, and StateHeld for
	// what the cycle did about it.
	Degraded bool `json:"degraded,omitempty"`
	// DegradedFiles names the unreadable domain lists and why.
	DegradedFiles []DegradedFile `json:"degraded_files,omitempty"`
	// StateHeld reports that the account's sync state was deliberately not
	// saved, so the same mail is fetched and re-evaluated next cycle. It is set
	// by `on_degraded_filter: hold`. Files already stored are still stored —
	// the sinks are idempotent, so the re-run skips them.
	StateHeld bool `json:"state_held,omitempty"`
	// Stopped reports that the cycle ended early because a shutdown was
	// requested. The message in flight was finished, nothing new was fetched
	// after it, and the state that was reached is saved: the remaining mail is
	// picked up by the next run, not lost.
	Stopped bool `json:"stopped,omitempty"`

	// Summary counts the cycle at a glance.
	Summary Summary `json:"summary"`

	// Error is the message of the failure that ended the cycle early, if any.
	// It is empty on success. Stored and Skipped are still authoritative for
	// the work completed before the failure.
	Error string `json:"error,omitempty"`
}

// QuarantineEntry describes one message parked in the quarantine directory.
//
// The raw RFC822 bytes are at Path, byte for byte as the provider delivered
// them, with a `.json` sidecar beside them carrying the same fields as this
// entry. Re-delivering one is a matter of fixing the cause and feeding the
// `.eml` back in by hand; nothing in mail-muncher does it automatically.
type QuarantineEntry struct {
	// Path is the `.eml` holding the raw message.
	Path string `json:"path"`
	// ID is the provider-scoped message id.
	ID string `json:"id"`
	// Rule is the rule that claimed the message, when it got that far. Empty
	// for a message that would not even parse.
	Rule string `json:"rule,omitempty"`
	// Reason is the stage that failed: "parse" or "sink".
	Reason string `json:"reason"`
	// Error is the failure text.
	Error string `json:"error"`
	// QuarantinedAt is when the message was parked, in UTC.
	QuarantinedAt time.Time `json:"quarantined_at"`
}

// DegradedFile names one externally-owned domain list the cycle could not read.
type DegradedFile struct {
	// Path is the domain list.
	Path string `json:"path"`
	// Error is why it could not be read.
	Error string `json:"error"`
}

// Entry describes one rendering of one message at one path.
//
// Every field is populated for both Stored and Skipped entries: an agent that
// finds a message under Skipped can still read its subject and sender without
// opening the file.
type Entry struct {
	// Path is the absolute-or-config-relative destination file.
	Path string `json:"path"`
	// Format is the rendering written there ("eml", "markdown").
	Format config.Format `json:"format"`
	// Rule is the name of the rule that claimed the message.
	Rule string `json:"rule"`
	// ID is the provider-scoped message id. Entries for the same message in
	// different formats share it.
	ID string `json:"id"`
	// MessageID is the RFC822 Message-ID header, angle brackets included.
	MessageID string `json:"message_id,omitempty"`
	// ThreadID is the conversation this message belongs to. Every entry has
	// one, so an agent can group a cycle's deliveries by thread — "everything
	// that arrived about the Acme thread" — without opening a single file.
	// Entries for the same message in different formats share it, and so do
	// entries for different messages in the same conversation.
	ThreadID string `json:"thread_id"`
	// ThreadIDSource is where ThreadID came from: "provider" when the mail
	// provider grouped the thread itself, and "references" / "in_reply_to" /
	// "self" when mail-muncher reconstructed it from the message's headers.
	// Reconstruction is best-effort — a mailer that breaks the References
	// chain splits a thread — so an agent that needs a guarantee should check
	// this before treating the grouping as complete.
	ThreadIDSource string `json:"thread_id_source"`
	// From is the first From address, bare addr-spec, no display name.
	From string `json:"from,omitempty"`
	// Subject is the RFC2047-decoded Subject header.
	Subject string `json:"subject"`
	// Date is the message date in UTC (Date header, or the provider's internal
	// date when the header was missing or unparseable).
	Date time.Time `json:"date"`
	// HasAttachment reports whether the message carries a non-inline part.
	HasAttachment bool `json:"has_attachment"`
}

// Summary counts one account's cycle.
//
// The counters work at two granularities, deliberately:
//
//   - Fetched, Matched and ParseErrors count messages. Messages no rule
//     claimed are Fetched-Matched-ParseErrors; they are not counted as
//     Skipped, because "skipped" is about writing, not about matching.
//   - Stored, Skipped and SinkErrors count renderings, i.e. (message x
//     format) pairs. A rule with `formats: [eml, markdown]` contributes two.
//
// So Stored == len(Manifest.Stored) and Skipped == len(Manifest.Skipped)
// always: every counter that names a rendering has an array to match it. A
// re-run over the same window reads `matched=N stored=0 skipped=N`, which is
// idempotency working.
type Summary struct {
	// Fetched is messages the provider delivered this cycle.
	Fetched int `json:"fetched"`
	// Matched is messages some rule claimed.
	Matched int `json:"matched"`
	// Stored is renderings actually written (or, under --dry-run, that would
	// have been written).
	Stored int `json:"stored"`
	// Skipped is renderings not written because the destination already
	// existed.
	Skipped int `json:"skipped"`
	// ParseErrors is messages that would not parse; logged and skipped.
	ParseErrors int `json:"parse_errors"`
	// SinkErrors is write failures; logged and counted, the cycle continues.
	SinkErrors int `json:"sink_errors"`
	// Quarantined is messages parked under the quarantine directory because
	// they could not be delivered. It counts messages, not renderings, and it
	// overlaps ParseErrors and SinkErrors by design: those count what went
	// wrong, this counts what was done about it. A non-zero value is not a
	// failed run, but it is always mail an operator has to look at.
	Quarantined int `json:"quarantined"`
}

// Add accumulates other into s, so a daemon can total several cycles.
func (s *Summary) Add(other Summary) {
	s.Fetched += other.Fetched
	s.Matched += other.Matched
	s.Stored += other.Stored
	s.Skipped += other.Skipped
	s.ParseErrors += other.ParseErrors
	s.SinkErrors += other.SinkErrors
	s.Quarantined += other.Quarantined
}

// String renders the summary as the run's human one-liner:
//
//	fetched=42 matched=3 stored=3 skipped=39 parse_errors=0 sink_errors=0 quarantined=0
func (s Summary) String() string {
	return fmt.Sprintf("fetched=%d matched=%d stored=%d skipped=%d parse_errors=%d sink_errors=%d quarantined=%d",
		s.Fetched, s.Matched, s.Stored, s.Skipped, s.ParseErrors, s.SinkErrors, s.Quarantined)
}

// LogArgs returns the summary as alternating slog key/value pairs.
func (s Summary) LogArgs() []any {
	return []any{
		"fetched", s.Fetched,
		"matched", s.Matched,
		"stored", s.Stored,
		"skipped", s.Skipped,
		"parse_errors", s.ParseErrors,
		"sink_errors", s.SinkErrors,
		"quarantined", s.Quarantined,
	}
}

// Line renders the manifest as the run command's human summary line:
//
//	personal: fetched=42 matched=3 stored=3 skipped=39 parse_errors=0 sink_errors=0 duration=1.84s
//
// The published field list runs contiguously from fetched= to duration=, so a
// dry run is marked on the account label rather than spliced into the middle:
//
//	personal (dry-run): fetched=42 ...
func (m Manifest) Line() string {
	var b strings.Builder
	b.WriteString(m.Account)
	if labels := m.labels(); len(labels) > 0 {
		b.WriteString(" (" + strings.Join(labels, ", ") + ")")
	}
	b.WriteString(": ")
	b.WriteString(m.Summary.String())
	fmt.Fprintf(&b, " duration=%s", (time.Duration(m.DurationMS) * time.Millisecond).String())
	return b.String()
}

// labels are the parenthesised markers Line puts on the account name. They go
// on the label rather than into the counter list so the published field list
// stays contiguous from fetched= to duration=.
func (m Manifest) labels() []string {
	var out []string
	if m.DryRun {
		out = append(out, "dry-run")
	}
	if m.Degraded {
		out = append(out, "degraded")
	}
	if m.StateHeld {
		out = append(out, "state held")
	}
	if m.Stopped {
		out = append(out, "stopped")
	}
	return out
}

// WriteJSON writes one manifest to w as a single line of JSON followed by a
// newline: the newline-delimited stream `run --json` and `daemon --json` both
// emit. Callers pass stdout; every human-facing message goes to stderr, so the
// two never interleave.
func WriteJSON(w io.Writer, m Manifest) error {
	enc := json.NewEncoder(w)
	// Paths and subjects are not HTML, and escaping them would corrupt the
	// literal path an agent is meant to open.
	enc.SetEscapeHTML(false)
	return enc.Encode(m)
}

// newManifest starts an account's manifest with non-nil entry slices, so a
// cycle that stores nothing marshals as [] rather than null.
func newManifest(account string, startedAt time.Time, dryRun bool) Manifest {
	return Manifest{
		Account:   account,
		StartedAt: startedAt.UTC(),
		DryRun:    dryRun,
		Stored:    []Entry{},
		Skipped:   []Entry{},
	}
}

// entryFor renders one (message x format) result as a manifest entry.
func entryFor(msg *model.Message, rule *config.Rule, format config.Format, path string) Entry {
	e := Entry{
		Path:           path,
		Format:         format,
		ID:             msg.ID,
		MessageID:      msg.MessageID,
		ThreadID:       msg.ThreadID,
		ThreadIDSource: string(msg.ThreadIDSource),
		Subject:        msg.Subject,
		Date:           msg.Date.UTC(),
		HasAttachment:  msg.HasAttachment(),
	}
	if rule != nil {
		e.Rule = rule.Name
	}
	if from := msg.FromAddresses(); len(from) > 0 {
		e.From = from[0]
	}
	return e
}
