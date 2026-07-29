package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/jhillyerd/enmime/v2"
)

// ErrEmptyMessage is returned by Parse when raw contains no bytes.
var ErrEmptyMessage = errors.New("model: empty message")

// parser is the shared enmime parser.
//
// DisableTextConversion keeps enmime from silently down-converting a text/html
// part into Envelope.Text: Message.TextBody must reflect an actual text/plain
// part so that sinks can tell "the sender sent plain text" apart from "we
// invented plain text", and do their own HTML rendering when they prefer.
var parser = enmime.NewParser(
	enmime.DisableTextConversion(true),
	enmime.SkipMalformedParts(true),
)

// ParseOption is an optional piece of provider knowledge handed to Parse.
// Options are how the parser learns things that are not in the bytes; the
// required provider metadata stays in the positional parameters.
type ParseOption func(*parseOptions)

// parseOptions is the accumulated effect of the ParseOptions passed to Parse.
type parseOptions struct {
	threadID string
}

// WithThreadID supplies the provider's native conversation id (Gmail's
// threadId), which then wins over the header-derived fallback. An empty or
// blank id is ignored, so a provider with no threading of its own can pass its
// zero value unconditionally and still get a synthesized ThreadID.
func WithThreadID(id string) ParseOption {
	return func(o *parseOptions) { o.threadID = strings.TrimSpace(id) }
}

// Parse turns raw RFC822 bytes into a canonical Message.
//
// id, account, internalDate and labels come from the provider: internalDate is
// used as the Date when the message carries no usable Date header, and labels
// are copied verbatim. Anything further the provider knows arrives as a
// ParseOption — today that is WithThreadID.
//
// The resulting Message always has a non-empty ThreadID: the provider's, when
// one was given, and otherwise one synthesized from the threading headers (see
// synthesizeThread).
//
// A malformed message returns an error and must not kill a run — callers log
// and skip it. Parse never panics: any panic from the MIME parser is recovered
// and returned as an error.
func Parse(id, account string, raw []byte, internalDate time.Time, labels []string, opts ...ParseOption) (msg *Message, err error) {
	defer func() {
		if r := recover(); r != nil {
			msg = nil
			err = fmt.Errorf("model: panic parsing message %q: %v", id, r)
		}
	}()

	var options parseOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}

	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("model: parse message %q: %w", id, ErrEmptyMessage)
	}

	env, err := parser.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("model: parse message %q: %w", id, err)
	}
	if env == nil || env.Root == nil {
		return nil, fmt.Errorf("model: parse message %q: %w", id, errors.New("no MIME root part"))
	}

	m := &Message{
		ID:        id,
		Account:   account,
		Raw:       raw,
		Subject:   env.GetHeader("Subject"),
		MessageID: strings.TrimSpace(env.GetHeader("Message-Id")),
		TextBody:  env.Text,
		HTMLBody:  env.HTML,
		Headers:   collectHeaders(env),
	}

	if len(labels) > 0 {
		m.Labels = append([]string(nil), labels...)
	}

	m.From = addressList(env, "From")
	if len(m.From) == 0 {
		// Some automated senders only set Sender.
		m.From = addressList(env, "Sender")
	}
	m.To = addressList(env, "To")
	m.Cc = addressList(env, "Cc")

	m.Date = messageDate(env, internalDate)
	m.Attachments = collectAttachments(env)

	m.InReplyTo = firstMessageID(env.GetHeader("In-Reply-To"))
	m.References = parseReferences(env.GetHeaderValues("References"))
	m.ThreadID, m.ThreadIDSource = synthesizeThread(m, options.threadID)

	return m, nil
}

// synthesizeThread decides the message's thread key.
//
// The provider's own id wins outright — it is the only value backed by a
// guarantee. Failing that the message is keyed on the root of its reference
// chain, so every reply in a conversation lands on the same key: References[0],
// then In-Reply-To (which keys on the parent, the best a mailer that omits
// References allows), then the message's own Message-ID, i.e. a thread of one.
//
// The last two fallbacks exist only so the "never empty" promise holds for a
// message that carries no Message-ID at all: the provider id, then a digest of
// the bytes. Both are stable across runs, which is what grouping needs.
func synthesizeThread(m *Message, providerThreadID string) (string, ThreadIDSource) {
	switch {
	case providerThreadID != "":
		return providerThreadID, ThreadIDSourceProvider
	case len(m.References) > 0:
		return m.References[0], ThreadIDSourceReferences
	case m.InReplyTo != "":
		return m.InReplyTo, ThreadIDSourceInReplyTo
	case m.MessageID != "":
		return m.MessageID, ThreadIDSourceSelf
	case strings.TrimSpace(m.ID) != "":
		return strings.TrimSpace(m.ID), ThreadIDSourceSelf
	default:
		sum := sha256.Sum256(m.Raw)
		return "sha256:" + hex.EncodeToString(sum[:16]), ThreadIDSourceSelf
	}
}

// parseReferences extracts the msg-ids of a References chain, oldest first,
// angle brackets kept.
//
// RFC 5322 says the value is whitespace-separated angle-addrs, but the header
// is written by every mail client ever shipped: values get glued together
// without spaces, comments and stray words get interleaved, and brackets go
// unclosed. So rather than tokenizing on whitespace and trusting the tokens,
// this scans for bracketed runs and ignores everything between them. A `<` that
// is never closed is dropped, as is a second `<` opening before the first
// closes. Repeats are collapsed, keeping first position, because a chain that
// names the same ancestor twice still describes one thread.
func parseReferences(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	var (
		out  []string
		seen = make(map[string]struct{})
	)
	for _, v := range values {
		start := -1
		for i, r := range v {
			switch r {
			case '<':
				// An unclosed '<' is garbage; restart the token here.
				start = i
			case '>':
				if start < 0 {
					continue
				}
				id := strings.TrimSpace(v[start : i+1])
				start = -1
				if id == "<>" {
					continue
				}
				if _, dup := seen[id]; dup {
					continue
				}
				seen[id] = struct{}{}
				out = append(out, id)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// firstMessageID reduces a single-msg-id header (In-Reply-To) to the id it
// names. Senders append comments to it — `<a@b> (Jane's message)` — so the
// first bracketed run wins.
//
// A value with no brackets is accepted only when it still looks like an
// addr-spec: one whitespace-free token containing an "@". That rules out the
// literals broken mailers emit (`undefined`, `null`, a human sentence), which
// matters because whatever comes back here can become a thread key — and a key
// of "undefined" would silently weld unrelated conversations together.
func firstMessageID(value string) string {
	if ids := parseReferences([]string{value}); len(ids) > 0 {
		return ids[0]
	}
	bare := strings.TrimSpace(value)
	if bare == "" || strings.ContainsAny(bare, " \t<>") || !strings.Contains(bare, "@") {
		return ""
	}
	return bare
}

// messageDate prefers the Date header and falls back to the provider's
// internal date when the header is missing, unparseable or zero.
func messageDate(env *enmime.Envelope, internalDate time.Time) time.Time {
	if d, err := env.Date(); err == nil && !d.IsZero() {
		return d
	}
	return internalDate
}

// addressList parses an address header, tolerating malformed values by
// returning whatever enmime could recover (possibly nothing).
func addressList(env *enmime.Envelope, key string) []mail.Address {
	ptrs, err := env.AddressList(key)
	if err != nil && len(ptrs) == 0 {
		return nil
	}
	out := make([]mail.Address, 0, len(ptrs))
	for _, p := range ptrs {
		if p == nil {
			continue
		}
		out = append(out, mail.Address{Name: p.Name, Address: strings.TrimSpace(p.Address)})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// collectHeaders snapshots the top-level headers with RFC2047-decoded values,
// re-keyed with the standard canonical form so textproto lookups work.
func collectHeaders(env *enmime.Envelope) textproto.MIMEHeader {
	keys := env.GetHeaderKeys()
	if len(keys) == 0 {
		return nil
	}
	h := make(textproto.MIMEHeader, len(keys))
	for _, k := range keys {
		values := env.GetHeaderValues(k)
		if len(values) == 0 {
			continue
		}
		ck := textproto.CanonicalMIMEHeaderKey(k)
		h[ck] = append(h[ck], values...)
	}
	if len(h) == 0 {
		return nil
	}
	return h
}

func collectAttachments(env *enmime.Envelope) []Attachment {
	if len(env.Attachments) == 0 {
		return nil
	}
	out := make([]Attachment, 0, len(env.Attachments))
	for i, p := range env.Attachments {
		if p == nil {
			continue
		}
		a := Attachment{
			Filename:    strings.TrimSpace(p.FileName),
			ContentType: p.ContentType,
			Content:     p.Content,
		}
		if a.Filename == "" {
			a.Filename = fallbackFilename(i, p.ContentType)
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// fallbackFilename names an attachment that arrived without one, so sinks
// always have something to write to disk.
func fallbackFilename(index int, contentType string) string {
	ext := ".bin"
	if exts, err := mime.ExtensionsByType(contentType); err == nil && len(exts) > 0 {
		ext = exts[0]
	}
	return "attachment-" + strconv.Itoa(index+1) + ext
}
