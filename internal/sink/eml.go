package sink

import (
	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/model"
)

// EMLExt is the file extension the EML sink writes.
const EMLExt = ".eml"

// EMLSink writes the message exactly as it was fetched — model.Message.Raw,
// byte for byte — to <dest>/<YYYY>/<MM>/<basename>.eml.
//
// This is the fidelity format: nothing is re-encoded, re-wrapped, or
// normalized, so the file round-trips through any mail tool and stays
// verifiable against DKIM signatures.
type EMLSink struct{}

// NewEML returns the .eml sink. The zero EMLSink is equally usable; the
// constructor exists so callers do not depend on the struct being empty.
func NewEML() *EMLSink { return &EMLSink{} }

// Format implements Sink.
func (s *EMLSink) Format() config.Format { return config.FormatEML }

// Path implements Sink.
func (s *EMLSink) Path(msg *model.Message, rule *config.Rule) string {
	return pathFor(msg, rule, EMLExt)
}

// Plan implements Sink.
func (s *EMLSink) Plan(msg *model.Message, rule *config.Rule) (string, bool, error) {
	path := s.Path(msg, rule)
	if err := checkDest(rule); err != nil {
		return path, false, err
	}
	if err := verifyDir(msg, rule); err != nil {
		return path, false, err
	}
	exists, err := plan(path)
	return path, exists, err
}

// Store implements Sink: it writes Raw to the destination unless a file is
// already there, in which case it writes nothing and reports skipped.
//
// The name is derived from the message identity, so an existing file is by
// construction this same message from an earlier run — a cron overlap, a
// replay after state loss — and re-writing it would only risk corrupting a
// good archive.
//
// The order matters. The directory chain is established first, because an
// existence check is only worth as much as the path it walked to get there: a
// symlinked month directory would otherwise answer the check from somewhere
// else entirely. The Lstat that follows is then an early out for the common
// re-run case — nearly every message is already on disk, and there is no reason
// to cut a temp file to discover that — and the place a symlink planted at the
// final path becomes an error instead of a silent skip. Whether the name is
// actually free is settled by the exclusive create, not by that Lstat.
func (s *EMLSink) Store(msg *model.Message, rule *config.Rule) (string, bool, error) {
	path := s.Path(msg, rule)
	if err := checkDest(rule); err != nil {
		return path, false, err
	}
	if _, err := ensureDir(msg, rule); err != nil {
		return path, false, err
	}
	exists, err := plan(path)
	if err != nil {
		return path, false, err
	}
	if exists {
		return path, true, nil
	}

	var raw []byte
	if msg != nil {
		raw = msg.Raw
	}
	created, err := createFileExclusive(path, raw)
	if err != nil {
		return path, false, err
	}
	return path, !created, nil
}
