package mcpserver

import (
	"errors"
	"fmt"
	"io/fs"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"gopkg.in/yaml.v3"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/model"
	"github.com/craigjmidwinter/mail-muncher/internal/sink"
)

const (
	// frontmatterFence delimits the YAML header internal/sink writes.
	frontmatterFence = "---"
	// maxFileBytes bounds a single stored file the archive will read into
	// memory. Mail is small; a multi-hundred-megabyte "message" is a reason to
	// skip the file, not to exhaust the server.
	maxFileBytes = 64 << 20
)

// fileRecord is one rendering of one stored message, parsed.
//
// Fields mirror the markdown frontmatter internal/sink writes, because that is
// the richer of the two sources; an `.eml` is parsed with internal/model and
// mapped onto the same shape.
type fileRecord struct {
	path    string
	format  config.Format
	size    int64
	modTime time.Time

	id   string
	rule string

	account        string
	subject        string
	from           string
	to             []string
	cc             []string
	date           time.Time
	messageID      string
	threadID       string
	threadIDSource string
	inReplyTo      string
	labels         []string
	attachments    []AttachmentInfo
	body           string
	bodyFormat     string
}

// key identifies the message this rendering belongs to.
func (r *fileRecord) key() messageKey { return messageKey{rule: r.rule, id: r.id} }

// searchText is the haystack search_messages scans: everything a human would
// expect "does this message mention X" to cover.
func (r *fileRecord) searchText() string {
	var b strings.Builder
	b.Grow(len(r.body) + 256)
	write := func(s string) {
		if s != "" {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	write(r.subject)
	write(r.from)
	write(strings.Join(r.to, ", "))
	write(strings.Join(r.cc, ", "))
	write(strings.Join(r.labels, ", "))
	for _, a := range r.attachments {
		write(a.Filename)
	}
	write(r.body)
	return b.String()
}

// parseFile reads one stored rendering. rule supplies the facts the file
// itself cannot carry — which rule claimed the message, and (for an `.eml`)
// which account it belongs to.
func parseFile(path string, rule *config.Rule, info fs.FileInfo) (*fileRecord, error) {
	if info.Size() > maxFileBytes {
		return nil, fmt.Errorf("file is %d bytes, over the %d byte limit", info.Size(), maxFileBytes)
	}

	id, ok := messageIDFromPath(path)
	if !ok {
		return nil, errors.New("filename does not carry an identity digest")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	rec := &fileRecord{
		path:    path,
		size:    info.Size(),
		modTime: info.ModTime(),
		id:      id,
	}
	if rule != nil {
		rec.rule = rule.Name
		rec.account = rule.Account
	}

	switch strings.ToLower(filepath.Ext(path)) {
	case sink.MarkdownExt:
		rec.format = config.FormatMarkdown
		if err := rec.readMarkdown(data); err != nil {
			return nil, err
		}
	case sink.EMLExt:
		rec.format = config.FormatEML
		if err := rec.readEML(data); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unreadable extension %q", filepath.Ext(path))
	}
	return rec, nil
}

// messageIDFromPath extracts the identity digest from a basename written by
// sink.Basename: `<unix-seconds>-<sha256(account+":"+id)[:8]>-<slug>`.
//
// Only the digest is treated as the id, because only the digest is stable: the
// timestamp and the slug both come from message content.
func messageIDFromPath(path string) (string, bool) {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, filepath.Ext(base))

	parts := strings.SplitN(base, "-", 3)
	if len(parts) < 2 || len(parts[1]) != sink.HashLen {
		return "", false
	}
	for _, r := range parts[1] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", false
		}
	}
	return parts[1], true
}

// markdownFrontmatter mirrors the YAML header internal/sink writes. It is
// decoded liberally — unknown keys are ignored — so a sink that grows a field
// does not make every stored file unreadable here.
type markdownFrontmatter struct {
	Subject        string    `yaml:"subject"`
	From           string    `yaml:"from"`
	To             []string  `yaml:"to"`
	Cc             []string  `yaml:"cc"`
	Date           time.Time `yaml:"date"`
	MessageID      string    `yaml:"message_id"`
	ThreadID       string    `yaml:"thread_id"`
	ThreadIDSource string    `yaml:"thread_id_source"`
	InReplyTo      string    `yaml:"in_reply_to"`
	Account        string    `yaml:"account"`
	Rule           string    `yaml:"rule"`
	Labels         []string  `yaml:"labels"`
	Attachments    []string  `yaml:"attachments"`
}

// readMarkdown fills the record from a rendered `.md` file: the frontmatter is
// already the structured form of everything a summary needs, so nothing has to
// be re-derived from mail headers.
func (r *fileRecord) readMarkdown(data []byte) error {
	front, body, err := splitFrontmatter(string(data))
	if err != nil {
		return err
	}

	var fm markdownFrontmatter
	if err := yaml.Unmarshal([]byte(front), &fm); err != nil {
		return fmt.Errorf("decode frontmatter: %w", err)
	}

	r.subject = fm.Subject
	r.from = fm.From
	r.to = fm.To
	r.cc = fm.Cc
	r.date = fm.Date.UTC()
	r.messageID = fm.MessageID
	r.threadID = fm.ThreadID
	r.threadIDSource = fm.ThreadIDSource
	r.inReplyTo = fm.InReplyTo
	r.labels = fm.Labels
	r.body = strings.TrimSpace(body)
	r.bodyFormat = string(config.FormatMarkdown)
	if fm.Account != "" {
		r.account = fm.Account
	}
	// fm.Rule is deliberately not used. It records the rule that wrote the file,
	// which a later config edit can rename out from under it; the rule whose
	// dest the file is sitting in is the one list_rules reports and the one a
	// `rule:` filter is written against, so that is the one a record carries.

	// A message with no ThreadID predates the threading fields. Falling back to
	// the Message-ID keeps grouping total — every record has a thread — and
	// says so through the source, exactly as model.Parse does.
	if r.threadID == "" {
		r.threadID = fm.MessageID
		if r.threadID == "" {
			r.threadID = r.id
		}
		r.threadIDSource = string(model.ThreadIDSourceSelf)
	}

	r.attachments = r.statAttachments(fm.Attachments)
	return nil
}

// statAttachments turns the frontmatter's attachment names into the sizes an
// agent needs to decide whether to ask for one. The files sit in the sibling
// `<basename>.attachments` directory; a name that is not there is reported
// with a zero size rather than dropped.
func (r *fileRecord) statAttachments(names []string) []AttachmentInfo {
	if len(names) == 0 {
		return nil
	}
	base := strings.TrimSuffix(filepath.Base(r.path), filepath.Ext(r.path))
	dir := filepath.Join(filepath.Dir(r.path), base+sink.AttachmentsDirSuffix)

	out := make([]AttachmentInfo, 0, len(names))
	for _, name := range names {
		// The name comes from a file mail-muncher wrote, but it is derived from
		// a sender-supplied filename, so it is never trusted as a path.
		info := AttachmentInfo{Filename: name}
		if clean := filepath.Base(name); clean == name && name != "." && name != ".." {
			if st, err := os.Stat(filepath.Join(dir, clean)); err == nil && st.Mode().IsRegular() {
				info.Bytes = st.Size()
			}
		}
		out = append(out, info)
	}
	return out
}

// splitFrontmatter separates the leading `---` fenced YAML header from the
// document body.
func splitFrontmatter(doc string) (front, body string, err error) {
	rest, ok := strings.CutPrefix(doc, frontmatterFence+"\n")
	if !ok {
		return "", "", errors.New("file does not start with a frontmatter fence")
	}
	end := strings.Index(rest, "\n"+frontmatterFence+"\n")
	if end < 0 {
		// A file whose fence is never closed still has a header worth reading.
		if strings.HasSuffix(rest, "\n"+frontmatterFence) {
			return strings.TrimSuffix(rest, "\n"+frontmatterFence), "", nil
		}
		return "", "", errors.New("frontmatter fence is never closed")
	}
	return rest[:end], rest[end+len("\n"+frontmatterFence+"\n"):], nil
}

// readEML fills the record by parsing the RFC822 source. This is the fallback
// for a rule configured `formats: [eml]` only.
//
// One thing cannot be recovered here: the provider's native conversation id.
// model.Parse synthesizes a ThreadID from the message's own headers instead,
// and says so through ThreadIDSource ("references" / "in_reply_to" / "self"),
// so a consumer can tell this reconstructed grouping from a provider-guaranteed
// one. Adding `markdown` to the rule's formats is what makes the provider's
// thread id survive into the archive.
func (r *fileRecord) readEML(raw []byte) error {
	msg, err := model.Parse(r.id, r.account, raw, r.modTime, nil)
	if err != nil {
		return err
	}

	r.subject = msg.Subject
	r.from = joinAddresses(msg.From)
	r.to = addressStrings(msg.To)
	r.cc = addressStrings(msg.Cc)
	r.date = msg.Date.UTC()
	r.messageID = msg.MessageID
	r.threadID = msg.ThreadID
	r.threadIDSource = string(msg.ThreadIDSource)
	r.inReplyTo = msg.InReplyTo
	r.labels = msg.Labels
	r.body, r.bodyFormat = emlBody(msg)

	if len(msg.Attachments) > 0 {
		r.attachments = make([]AttachmentInfo, 0, len(msg.Attachments))
		for _, a := range msg.Attachments {
			r.attachments = append(r.attachments, AttachmentInfo{
				Filename:    a.Filename,
				ContentType: a.ContentType,
				Bytes:       int64(len(a.Content)),
			})
		}
	}
	return nil
}

// emlBody picks the readable body of a parsed message: the text/plain part
// when there is one, the HTML part converted to Markdown otherwise. That is
// the same choice the markdown sink makes, so a rule that adds `markdown`
// later does not change what these tools return.
func emlBody(msg *model.Message) (body, format string) {
	if text := strings.TrimSpace(msg.TextBody); text != "" {
		return text, "text"
	}
	if html := strings.TrimSpace(msg.HTMLBody); html != "" {
		if md, err := htmltomarkdown.ConvertString(html); err == nil {
			if md = strings.TrimSpace(md); md != "" {
				return md, string(config.FormatMarkdown)
			}
		}
	}
	return "", "text"
}

// joinAddresses renders a header's addresses the way the markdown frontmatter
// does, so `.md` and `.eml` records read alike.
func joinAddresses(addrs []mail.Address) string {
	if len(addrs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		parts = append(parts, formatAddress(a))
	}
	return strings.Join(parts, ", ")
}

// addressStrings renders each address separately.
func addressStrings(addrs []mail.Address) []string {
	if len(addrs) == 0 {
		return nil
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, formatAddress(a))
	}
	return out
}

// formatAddress mirrors internal/sink: `Jane Doe <jane@acme.com>`, or the bare
// addr-spec when there is no display name.
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
