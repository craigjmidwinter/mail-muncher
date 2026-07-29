package model

import (
	"net/mail"
	"net/textproto"
	"strings"
	"time"
)

// Attachment is a single non-inline part of a message, decoded into memory.
//
// Content is already decoded (base64/quoted-printable undone, charset converted
// where enmime could do so), so sinks can write it to disk verbatim.
type Attachment struct {
	Filename    string // file name from Content-Disposition or Content-Type; never empty (see Parse)
	ContentType string // media type without parameters, e.g. "application/pdf"
	Content     []byte // decoded bytes
}

// ThreadIDSource records how a Message.ThreadID was arrived at. It is the
// difference between "the provider says these messages are one conversation"
// and "we reconstructed that from headers the sender controls".
type ThreadIDSource string

const (
	// ThreadIDSourceProvider: the provider handed us a native conversation id
	// (Gmail's threadId). Authoritative — the provider did the grouping.
	ThreadIDSourceProvider ThreadIDSource = "provider"
	// ThreadIDSourceReferences: reconstructed from References[0], the root of
	// the reference chain. The usual case for a reply on a non-Gmail source.
	ThreadIDSourceReferences ThreadIDSource = "references"
	// ThreadIDSourceInReplyTo: reconstructed from In-Reply-To, because the
	// message carried no usable References. This keys the thread on the parent
	// rather than the root, so a deep chain from a mailer that omits
	// References will fragment.
	ThreadIDSourceInReplyTo ThreadIDSource = "in_reply_to"
	// ThreadIDSourceSelf: the message has no threading headers, so it is a
	// thread of one keyed by its own Message-ID (falling back to the provider
	// message id, then to a digest of the raw bytes, for a message that has
	// no Message-ID either).
	ThreadIDSourceSelf ThreadIDSource = "self"
)

// Synthesized reports whether the thread id was reconstructed from headers
// rather than supplied by the provider.
func (s ThreadIDSource) Synthesized() bool { return s != ThreadIDSourceProvider }

// Message is the canonical, provider-neutral representation of one mail
// message. Everything downstream of the provider seam (filters, sinks) works
// off this struct and never touches a provider API.
//
// A Message is produced by Parse and treated as read-only afterwards.
type Message struct {
	ID          string         // provider-scoped stable id (e.g. Gmail message id)
	Account     string         // name of the configured account this came from
	Raw         []byte         // full RFC822, exactly as fetched
	From        []mail.Address // From header, RFC2047-decoded display names
	To, Cc      []mail.Address // To / Cc headers
	Subject     string         // Subject header, RFC2047-decoded
	Date        time.Time      // Date header; falls back to the provider internal date
	MessageID   string         // Message-ID header, angle brackets included
	Labels      []string       // provider labels/folders, if any
	TextBody    string         // best-effort text/plain part ("" if the message is HTML-only)
	HTMLBody    string         // best-effort text/html part
	Attachments []Attachment   // parts with Content-Disposition: attachment

	// ThreadID is the conversation this message belongs to. It is never empty:
	// when the provider supplies one (Gmail's threadId) it is used verbatim,
	// and otherwise Parse synthesizes one from the message's threading headers
	// (see ThreadIDSource). Consumers can therefore group by it
	// unconditionally — a message with no threading headers at all is simply a
	// thread of one, keyed by itself.
	ThreadID string
	// ThreadIDSource says where ThreadID came from, so a consumer can tell a
	// provider-guaranteed thread from a reconstructed one. Reconstruction is
	// best-effort: a mailer that drops or rewrites References will split a
	// thread, and that must not be mistaken for a provider guarantee.
	ThreadIDSource ThreadIDSource
	// InReplyTo is the In-Reply-To header, angle brackets included, or "" when
	// the message carries none.
	InReplyTo string
	// References is the parsed References chain, oldest first, each id angle
	// brackets included. Nil when the message carries no usable References.
	References []string

	// Headers holds every top-level header of the message, keyed by canonical
	// MIME header key (textproto.CanonicalMIMEHeaderKey) with RFC2047-decoded
	// UTF-8 values. Repeated headers keep all their values, in order.
	//
	// Prefer the HeaderValues / Header accessors, which are case-insensitive
	// and nil-safe; this field is exported so callers can enumerate headers.
	Headers textproto.MIMEHeader
}

// HeaderValues returns every value of the named header, RFC2047-decoded, in the
// order they appeared. Lookup is case-insensitive. Returns nil when absent.
func (m *Message) HeaderValues(name string) []string {
	if m == nil || len(m.Headers) == 0 {
		return nil
	}
	if v, ok := m.Headers[textproto.CanonicalMIMEHeaderKey(name)]; ok {
		return v
	}
	// Fall back to a case-insensitive scan for keys that do not survive
	// canonicalisation (headers containing spaces or exotic bytes).
	for k, v := range m.Headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return nil
}

// Header returns the first value of the named header, or "" when absent.
func (m *Message) Header(name string) string {
	v := m.HeaderValues(name)
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// FromDomains returns the lowercased domain of each From address, in order.
// Addresses without an "@" are skipped.
func (m *Message) FromDomains() []string {
	if m == nil {
		return nil
	}
	domains := make([]string, 0, len(m.From))
	for _, a := range m.From {
		if d := addressDomain(a.Address); d != "" {
			domains = append(domains, d)
		}
	}
	if len(domains) == 0 {
		return nil
	}
	return domains
}

// FromAddresses returns the bare addr-spec of each From address (no display
// name), as written in the header.
func (m *Message) FromAddresses() []string {
	if m == nil {
		return nil
	}
	return addressStrings(m.From)
}

// ToAddresses returns the bare addr-spec of each To address (no display name),
// as written in the header.
func (m *Message) ToAddresses() []string {
	if m == nil {
		return nil
	}
	return addressStrings(m.To)
}

// CcAddresses returns the bare addr-spec of each Cc address (no display name),
// as written in the header. Nil when the message was not copied to anyone.
func (m *Message) CcAddresses() []string {
	if m == nil {
		return nil
	}
	return addressStrings(m.Cc)
}

// Recipients returns To followed by Cc, as a single slice.
func (m *Message) Recipients() []mail.Address {
	if m == nil {
		return nil
	}
	if len(m.To) == 0 && len(m.Cc) == 0 {
		return nil
	}
	out := make([]mail.Address, 0, len(m.To)+len(m.Cc))
	out = append(out, m.To...)
	out = append(out, m.Cc...)
	return out
}

// RecipientAddresses returns the bare addr-spec of every To and Cc address.
func (m *Message) RecipientAddresses() []string {
	return addressStrings(m.Recipients())
}

// HasAttachment reports whether the message carries at least one attachment.
// Inline parts (embedded images referenced by cid:) do not count.
func (m *Message) HasAttachment() bool {
	return m != nil && len(m.Attachments) > 0
}

// HasLabel reports whether the message carries the given provider label,
// compared exactly (case-sensitive) as providers report them.
func (m *Message) HasLabel(label string) bool {
	if m == nil {
		return false
	}
	for _, l := range m.Labels {
		if l == label {
			return true
		}
	}
	return false
}

// AttachmentFilenames returns the filename of each attachment, in order.
func (m *Message) AttachmentFilenames() []string {
	if m == nil || len(m.Attachments) == 0 {
		return nil
	}
	names := make([]string, 0, len(m.Attachments))
	for _, a := range m.Attachments {
		names = append(names, a.Filename)
	}
	return names
}

func addressStrings(addrs []mail.Address) []string {
	if len(addrs) == 0 {
		return nil
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.Address)
	}
	return out
}

// addressDomain extracts the lowercased domain from an addr-spec. Returns ""
// when the address has no domain part.
func addressDomain(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at < 0 || at == len(addr)-1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(addr[at+1:]))
}
