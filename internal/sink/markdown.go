package sink

import (
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"gopkg.in/yaml.v3"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/model"
)

const (
	// MarkdownExt is the file extension the Markdown sink writes.
	MarkdownExt = ".md"
	// AttachmentsDirSuffix is appended to the basename to name the sibling
	// directory attachments are written into.
	AttachmentsDirSuffix = ".attachments"

	// noBodyPlaceholder stands in for a message with neither a text nor an
	// HTML part, so the file is never a bare frontmatter block.
	noBodyPlaceholder = "*(no body)*"
	// attachmentsHeading introduces the links to the attachments directory.
	attachmentsHeading = "## Attachments"
	// fallbackAttachmentName is used when a filename sanitizes to nothing.
	fallbackAttachmentName = "attachment"
	// maxAttachmentNameLen bounds a sanitized attachment filename, leaving
	// room under the usual 255-byte limit for the "-NN" dedupe suffix.
	maxAttachmentNameLen = 120
)

// MarkdownSink writes a human-readable rendering of the message: YAML
// frontmatter, the body as Markdown, and links to any attachments.
//
// It is explicitly not a fidelity format — that is what the .eml is for. The
// goal is a file you can read in an editor, grep, or feed to other tooling.
// Inline images referenced by `cid:` are left exactly as the converter emits
// them (`![alt](cid:...)`); resolving them against the message's inline parts
// is out of scope.
type MarkdownSink struct{}

// NewMarkdown returns the .md sink. The zero MarkdownSink is equally usable;
// the constructor exists so callers do not depend on the struct being empty.
func NewMarkdown() *MarkdownSink { return &MarkdownSink{} }

// Format implements Sink.
func (s *MarkdownSink) Format() config.Format { return config.FormatMarkdown }

// Path implements Sink.
func (s *MarkdownSink) Path(msg *model.Message, rule *config.Rule) string {
	return pathFor(msg, rule, MarkdownExt)
}

// AttachmentsDir is the sibling directory this sink writes attachments into,
// whether or not the message has any. It sits next to the .md file and is
// named after the same basename.
func (s *MarkdownSink) AttachmentsDir(msg *model.Message, rule *config.Rule) string {
	return pathFor(msg, rule, AttachmentsDirSuffix)
}

// Plan implements Sink.
func (s *MarkdownSink) Plan(msg *model.Message, rule *config.Rule) (string, bool, error) {
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

// Store implements Sink. The .md file is the idempotency marker: when it
// already exists nothing is written, attachments included.
//
// Attachments are written before the .md so the document never links to files
// that are not there yet. If the .md write then fails, an attachments
// directory this call created is removed again, leaving the destination as it
// was found.
//
// As in the .eml sink the directory chain comes first, the Lstat after it is an
// early out and a symlink check, and the claim on the .md name is what actually
// decides "already stored".
func (s *MarkdownSink) Store(msg *model.Message, rule *config.Rule) (string, bool, error) {
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

	names := attachmentNames(attachmentsOf(msg))
	doc, err := s.render(msg, rule, names)
	if err != nil {
		return path, false, err
	}

	cleanup, err := s.writeAttachments(msg, rule, names)
	if err != nil {
		return path, false, err
	}
	created, err := createFileExclusive(path, doc)
	if err != nil {
		cleanup()
		return path, false, err
	}
	if !created {
		// Another writer claimed the .md between the check above and this
		// call. Its attachments are ours byte for byte — same message,
		// same names, same content — so they are left alone rather than
		// rolled back, which would strip the winner's document of the
		// files it links to.
		return path, true, nil
	}
	return path, false, nil
}

// writeAttachments writes each attachment under the message's attachments
// directory. It returns a cleanup func that removes the directory again, but
// only if this call is what created it.
//
// The directory is created without following a link at its own name, so a
// planted `<basename>.attachments -> /elsewhere` is refused rather than used to
// scatter decoded attachments outside the destination tree.
func (s *MarkdownSink) writeAttachments(msg *model.Message, rule *config.Rule, names []string) (func(), error) {
	noop := func() {}
	atts := attachmentsOf(msg)
	if len(atts) == 0 {
		return noop, nil
	}

	dir := s.AttachmentsDir(msg, rule)
	preexisting, err := plan(dir)
	if err != nil {
		return noop, err
	}
	if err := mkdirNoFollow(dir); err != nil {
		return noop, err
	}
	cleanup := noop
	if !preexisting {
		cleanup = func() { _ = os.RemoveAll(dir) }
	}

	for i, a := range atts {
		if err := writeFileAtomic(filepath.Join(dir, names[i]), a.Content); err != nil {
			cleanup()
			return noop, err
		}
	}
	return cleanup, nil
}

// render builds the whole document: frontmatter, body, attachment links.
func (s *MarkdownSink) render(msg *model.Message, rule *config.Rule, names []string) ([]byte, error) {
	front, err := yaml.Marshal(newFrontmatter(msg, rule, names))
	if err != nil {
		return nil, fmt.Errorf("sink: encode frontmatter: %w", err)
	}

	body, err := renderBody(msg)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.Write(front)
	b.WriteString("---\n\n")
	b.WriteString(body)
	b.WriteString("\n")
	if len(names) > 0 {
		b.WriteString("\n")
		b.WriteString(attachmentsHeading)
		b.WriteString("\n\n")
		linkDir := Basename(msg) + AttachmentsDirSuffix
		for _, n := range names {
			fmt.Fprintf(&b, "- [%s](%s/%s)\n", n, linkDir, n)
		}
	}
	return []byte(b.String()), nil
}

// renderBody picks the body part and returns it as Markdown: TextBody when
// there is one, the converted HTMLBody when the message is HTML-only, and the
// placeholder when the message has neither.
func renderBody(msg *model.Message) (string, error) {
	if msg == nil {
		return noBodyPlaceholder, nil
	}
	if text := normalizeBody(msg.TextBody); text != "" {
		return text, nil
	}
	if strings.TrimSpace(msg.HTMLBody) != "" {
		md, err := htmltomarkdown.ConvertString(msg.HTMLBody)
		if err != nil {
			return "", fmt.Errorf("sink: convert html body: %w", err)
		}
		if body := normalizeBody(md); body != "" {
			return body, nil
		}
	}
	return noBodyPlaceholder, nil
}

// normalizeBody converts line endings to LF, strips trailing whitespace from
// every line, and trims leading and trailing blank lines. Mail bodies arrive
// with CRLF and ragged right edges; neither survives into the archive.
func normalizeBody(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\v\f\u00a0")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// frontmatter is the YAML header of a rendered message. Marshalling it (rather
// than formatting the block by hand) is what keeps a subject full of quotes,
// colons, and newlines from producing an unparsable file.
//
// The threading fields are deliberately three and not four: `thread_id` is the
// join key, `thread_id_source` says how much to trust it, `in_reply_to` names
// the parent. The full `references` chain is left out — it is unbounded, and
// the .eml sitting next to this file has it verbatim.
type frontmatter struct {
	Subject   string      `yaml:"subject"`
	From      string      `yaml:"from"`
	To        flowStrings `yaml:"to"`
	Cc        flowStrings `yaml:"cc,omitempty"`
	Date      time.Time   `yaml:"date"`
	MessageID string      `yaml:"message_id"`
	// ThreadID is the conversation join key: every message of a thread carries
	// the same value, so a reader can group a directory of these files without
	// reassembling reference chains. Always present.
	ThreadID string `yaml:"thread_id"`
	// ThreadIDSource is "provider" when the mail provider itself grouped the
	// thread, and "references" / "in_reply_to" / "self" when mail-muncher
	// reconstructed it from headers — best-effort, and a mailer that breaks the
	// chain will split a thread.
	ThreadIDSource string `yaml:"thread_id_source"`
	// InReplyTo is the parent message's id, omitted for a message that starts a
	// thread.
	InReplyTo   string      `yaml:"in_reply_to,omitempty"`
	Account     string      `yaml:"account"`
	Rule        string      `yaml:"rule"`
	Labels      flowStrings `yaml:"labels,omitempty"`
	Attachments flowStrings `yaml:"attachments,omitempty"`
}

func newFrontmatter(msg *model.Message, rule *config.Rule, names []string) frontmatter {
	fm := frontmatter{Attachments: names}
	if rule != nil {
		fm.Rule = rule.Name
	}
	if msg == nil {
		return fm
	}
	fm.Subject = msg.Subject
	fm.From = formatAddresses(msg.From)
	fm.To = addressList(msg.To)
	fm.Cc = addressList(msg.Cc)
	fm.Date = msg.Date.UTC()
	fm.MessageID = msg.MessageID
	fm.ThreadID = msg.ThreadID
	fm.ThreadIDSource = string(msg.ThreadIDSource)
	fm.InReplyTo = msg.InReplyTo
	fm.Account = msg.Account
	fm.Labels = msg.Labels
	return fm
}

// formatAddress renders one address for a human reader: `Jane Doe
// <jane@acme.com>`, or the bare addr-spec when there is no display name.
//
// This is deliberately not mail.Address.String(), which always quotes the
// display name (`"Jane Doe" <jane@acme.com>`) because it produces a header
// that must re-parse. Nothing re-parses this file — the .eml is the fidelity
// copy — so readability wins. Newlines are folded to spaces so a header
// injected with CRLF cannot spill out of its frontmatter value.
func formatAddress(a mail.Address) string {
	name := strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(a.Name))
	switch {
	case name == "":
		return a.Address
	case a.Address == "":
		return name
	default:
		return name + " <" + a.Address + ">"
	}
}

// formatAddresses renders a header's addresses as one display string.
func formatAddresses(addrs []mail.Address) string {
	if len(addrs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		parts = append(parts, formatAddress(a))
	}
	return strings.Join(parts, ", ")
}

// addressList renders each address separately, for the sequence-valued fields.
func addressList(addrs []mail.Address) flowStrings {
	if len(addrs) == 0 {
		return nil
	}
	out := make(flowStrings, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, formatAddress(a))
	}
	return out
}

// flowStrings marshals as a single-line YAML flow sequence (`to: [a, b]`)
// instead of a block list, which keeps the frontmatter compact. Quoting is
// still the encoder's job, so values containing commas or brackets come out
// correctly escaped.
type flowStrings []string

// MarshalYAML implements yaml.Marshaler.
func (f flowStrings) MarshalYAML() (any, error) {
	node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, s := range f {
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s})
	}
	return node, nil
}

// attachmentNames maps attachments to the filenames they are written under:
// sanitized, and de-duplicated with a `-2`, `-3` suffix before the extension.
// Collisions are detected case-insensitively so the result is stable on
// case-insensitive filesystems too.
func attachmentNames(atts []model.Attachment) []string {
	if len(atts) == 0 {
		return nil
	}
	names := make([]string, 0, len(atts))
	used := make(map[string]bool, len(atts))
	for _, a := range atts {
		base := sanitizeFilename(a.Filename)
		name := base
		if used[strings.ToLower(name)] {
			ext := filepath.Ext(base)
			stem := strings.TrimSuffix(base, ext)
			for n := 2; ; n++ {
				name = fmt.Sprintf("%s-%d%s", stem, n, ext)
				if !used[strings.ToLower(name)] {
					break
				}
			}
		}
		used[strings.ToLower(name)] = true
		names = append(names, name)
	}
	return names
}

// sanitizeFilename reduces an attachment filename to something safe to join
// onto a path: no directory components, no path traversal, no shell- or
// URL-awkward characters, bounded length. model.Parse guarantees a non-empty
// filename, but not a well-behaved one — it comes from the sender.
func sanitizeFilename(name string) string {
	// Drop directory components written with either separator; a Windows
	// sender's "C:\docs\offer.pdf" must not become a directory here.
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}

	var b strings.Builder
	b.Grow(len(name))
	dash := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = true
		}
	}
	clean := strings.Trim(b.String(), "-.")
	if clean == "" {
		return fallbackAttachmentName
	}
	if len(clean) > maxAttachmentNameLen {
		ext := filepath.Ext(clean)
		if len(ext) > 16 {
			ext = ""
		}
		stem := strings.TrimSuffix(clean, ext)
		keep := maxAttachmentNameLen - len(ext)
		if keep < 1 {
			keep = 1
		}
		if len(stem) > keep {
			stem = stem[:keep]
		}
		clean = strings.TrimRight(stem, "-.") + ext
	}
	if clean == "" {
		return fallbackAttachmentName
	}
	return clean
}

// attachmentsOf is a nil-safe accessor, so Store can treat a nil message the
// same way the rest of the package does.
func attachmentsOf(msg *model.Message) []model.Attachment {
	if msg == nil {
		return nil
	}
	return msg.Attachments
}
