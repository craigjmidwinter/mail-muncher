package filter

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/craigjmidwinter/mail-muncher/internal/model"
	"gopkg.in/yaml.v3"
)

// Matcher reports whether a parsed message satisfies a compiled match tree.
type Matcher interface {
	Match(msg *model.Message) bool
}

// MatcherFunc adapts a plain function to Matcher.
type MatcherFunc func(msg *model.Message) bool

// Match implements Matcher.
func (f MatcherFunc) Match(msg *model.Message) bool { return f(msg) }

// Option customizes Compile and NewEngine.
type Option func(*options)

// options are the resolved settings a compile runs with.
type options struct {
	files *Files
	now   func() time.Time
	// refs collects every externally-owned file the compile saw, in first-use
	// order and deduplicated, so an Engine can read them all up front and
	// report the ones it could not read.
	refs []FileRef
}

// reference records a file a compiled predicate will read.
func (o *options) reference(kind FileKind, path string) {
	for _, seen := range o.refs {
		if seen.Kind == kind && seen.Path == path {
			return
		}
	}
	o.refs = append(o.refs, FileRef{Kind: kind, Path: path})
}

// WithFiles makes every file-valued predicate in the compiled tree read through
// the given cache, so that one BeginCycle refreshes them all and a file
// referenced by several rules is read once per cycle. Compile allocates a
// private cache when this option is not given.
func WithFiles(files *Files) Option {
	return func(o *options) {
		if files != nil {
			o.files = files
		}
	}
}

// WithDomainFiles is the former name of WithFiles.
//
// Deprecated: use WithFiles.
func WithDomainFiles(files *Files) Option { return WithFiles(files) }

// WithClock replaces time.Now for the `older_than` / `newer_than` predicates.
// It exists for tests.
func WithClock(now func() time.Time) Option {
	return func(o *options) {
		if now != nil {
			o.now = now
		}
	}
}

// newOptions resolves the option list, filling in defaults.
func newOptions(opts []Option) *options {
	o := &options{now: time.Now}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}
	if o.files == nil {
		o.files = NewFiles()
	}
	return o
}

// combinators and predicates are the recognized match-node keys, used for
// "unknown key" diagnostics.
var (
	combinators = []string{"all", "any", "not"}
	predicates  = []string{
		"from_domains",
		"from_domains_file",
		"from_regex",
		"from_regex_file",
		"has_attachment",
		"header",
		"label",
		"newer_than",
		"older_than",
		"subject_regex",
		"to_regex",
	}
)

// Compile turns a rule's `match:` node into a Matcher.
//
// Every regex and duration in the tree is compiled here, so a returned Matcher
// never fails at evaluation time. Errors name the offending location inside the
// tree (for example `any[1].from_regex`), which callers prefix with the rule
// they came from.
//
// Without WithDomainFiles the compiled tree gets a private domain-file cache
// that nothing refreshes: fine for one-shot uses such as `validate`, but the
// pipeline should compile through an Engine (or pass WithDomainFiles) so
// BeginCycle can re-read the files.
func Compile(node *yaml.Node, opts ...Option) (Matcher, error) {
	return compileNode(node, "", newOptions(opts))
}

// compileNode compiles one match node. path locates the node inside the tree
// and is "" at the root.
func compileNode(n *yaml.Node, path string, o *options) (Matcher, error) {
	n = resolve(n)

	switch {
	case n == nil || n.IsZero():
		return nil, errAt(path, "match node is empty")
	case n.Kind == yaml.ScalarNode && n.Tag == nullTag:
		return nil, errAt(path, "match node is empty")
	case n.Kind != yaml.MappingNode:
		return nil, errAt(path, "expected a mapping with exactly one key (a combinator or a predicate), got %s", kindName(n))
	case len(n.Content) == 0:
		return nil, errAt(path, "expected a mapping with exactly one key (a combinator or a predicate), got an empty mapping")
	case len(n.Content) > 2:
		return nil, errAt(path, "a match node must have exactly one key, got %d (%s); combine them with all: or any:",
			len(n.Content)/2, strings.Join(mappingKeys(n), ", "))
	}

	key, value := n.Content[0], n.Content[1]
	if key.Kind != yaml.ScalarNode {
		return nil, errAt(path, "match node keys must be plain strings")
	}
	at := joinPath(path, key.Value)

	switch key.Value {
	case "all":
		return compileCombinator(value, at, o, true)
	case "any":
		return compileCombinator(value, at, o, false)
	case "not":
		inner, err := compileNode(value, at, o)
		if err != nil {
			return nil, err
		}
		return MatcherFunc(func(msg *model.Message) bool { return !inner.Match(msg) }), nil
	case "from_domains":
		return compileFromDomains(value, at)
	case "from_domains_file":
		return compileFromDomainsFile(value, at, o)
	case "from_regex":
		return compileRegex(value, at, func(msg *model.Message) []string { return msg.FromAddresses() })
	case "from_regex_file":
		return compileRegexFile(value, at, o, func(msg *model.Message) []string { return msg.FromAddresses() })
	case "to_regex":
		return compileRegex(value, at, func(msg *model.Message) []string { return msg.RecipientAddresses() })
	case "subject_regex":
		return compileRegex(value, at, func(msg *model.Message) []string { return []string{msg.Subject} })
	case "header":
		return compileHeader(value, at)
	case "has_attachment":
		return compileHasAttachment(value, at)
	case "label":
		return compileLabel(value, at)
	case "older_than":
		return compileAge(value, at, o, true)
	case "newer_than":
		return compileAge(value, at, o, false)
	default:
		return nil, errAt(path, "unknown match key %q (combinators: %s; predicates: %s)",
			key.Value, strings.Join(combinators, ", "), strings.Join(predicates, ", "))
	}
}

// compileCombinator compiles `all:` (requireAll) or `any:`.
func compileCombinator(n *yaml.Node, path string, o *options, requireAll bool) (Matcher, error) {
	n = resolve(n)
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil, errAt(path, "expected a list of match nodes, got %s", kindName(n))
	}
	if len(n.Content) == 0 {
		return nil, errAt(path, "must contain at least one match node")
	}

	children := make([]Matcher, 0, len(n.Content))
	for i, c := range n.Content {
		m, err := compileNode(c, indexPath(path, i), o)
		if err != nil {
			return nil, err
		}
		children = append(children, m)
	}

	if requireAll {
		return MatcherFunc(func(msg *model.Message) bool {
			for _, c := range children {
				if !c.Match(msg) {
					return false
				}
			}
			return true
		}), nil
	}
	return MatcherFunc(func(msg *model.Message) bool {
		for _, c := range children {
			if c.Match(msg) {
				return true
			}
		}
		return false
	}), nil
}

// compileFromDomains compiles an inline domain list.
func compileFromDomains(n *yaml.Node, path string) (Matcher, error) {
	raw, err := stringList(n, path, "domain")
	if err != nil {
		return nil, err
	}
	wanted := make([]string, 0, len(raw))
	for i, d := range raw {
		norm := NormalizeDomain(d)
		if norm == "" {
			return nil, errAt(indexPath(path, i), "domain must not be empty")
		}
		wanted = append(wanted, norm)
	}
	return MatcherFunc(func(msg *model.Message) bool {
		return domainsMatch(msg.FromDomains(), wanted)
	}), nil
}

// compileFromDomainsFile compiles a domain list held in an externally-owned
// file. The file is read lazily, once per cycle, through the shared cache.
func compileFromDomainsFile(n *yaml.Node, path string, o *options) (Matcher, error) {
	file, err := scalar(n, path)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(file) == "" {
		return nil, errAt(path, "path must not be empty")
	}
	o.reference(FileKindDomains, file)
	files := o.files
	return MatcherFunc(func(msg *model.Message) bool {
		return domainsMatch(msg.FromDomains(), files.Domains(file))
	}), nil
}

// compileRegexFile compiles a pattern list held in an externally-owned file
// into a predicate over the strings extract returns. The file is read and its
// patterns compiled lazily, once per cycle, through the shared cache — never
// once per message.
//
// extract is the only thing that varies between `from_regex_file` and the
// `to_regex_file` / `subject_regex_file` predicates nobody has asked for yet:
// the file format, the parser, the guards and the cache entry are all the same,
// so each of those is one more case in compileNode and nothing else.
func compileRegexFile(n *yaml.Node, path string, o *options, extract func(*model.Message) []string) (Matcher, error) {
	file, err := scalar(n, path)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(file) == "" {
		return nil, errAt(path, "path must not be empty")
	}
	o.reference(FileKindPatterns, file)
	files := o.files
	return MatcherFunc(func(msg *model.Message) bool {
		patterns := files.Patterns(file)
		if len(patterns) == 0 {
			return false
		}
		for _, s := range extract(msg) {
			for _, re := range patterns {
				if re.MatchString(s) {
					return true
				}
			}
		}
		return false
	}), nil
}

// compileRegex compiles a regex predicate over the strings extract returns.
func compileRegex(n *yaml.Node, path string, extract func(*model.Message) []string) (Matcher, error) {
	pattern, err := scalar(n, path)
	if err != nil {
		return nil, err
	}
	re, err := compilePattern(pattern, path)
	if err != nil {
		return nil, err
	}
	return MatcherFunc(func(msg *model.Message) bool {
		for _, s := range extract(msg) {
			if re.MatchString(s) {
				return true
			}
		}
		return false
	}), nil
}

// compileHeader compiles `header: {name: X-Foo, regex: pattern}`. The predicate
// matches when any value of the named header matches.
func compileHeader(n *yaml.Node, path string) (Matcher, error) {
	n = resolve(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, errAt(path, "expected a mapping with keys name and regex, got %s", kindName(n))
	}

	var name, pattern string
	var haveName, havePattern bool
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		switch k.Value {
		case "name":
			s, err := scalar(v, joinPath(path, "name"))
			if err != nil {
				return nil, err
			}
			name, haveName = s, true
		case "regex":
			s, err := scalar(v, joinPath(path, "regex"))
			if err != nil {
				return nil, err
			}
			pattern, havePattern = s, true
		default:
			return nil, errAt(path, "unknown key %q (want name and regex)", k.Value)
		}
	}

	switch {
	case !haveName:
		return nil, errAt(path, "missing key name")
	case strings.TrimSpace(name) == "":
		return nil, errAt(joinPath(path, "name"), "must not be empty")
	case !havePattern:
		return nil, errAt(path, "missing key regex")
	}

	re, err := compilePattern(pattern, joinPath(path, "regex"))
	if err != nil {
		return nil, err
	}
	return MatcherFunc(func(msg *model.Message) bool {
		for _, v := range msg.HeaderValues(name) {
			if re.MatchString(v) {
				return true
			}
		}
		return false
	}), nil
}

// compileHasAttachment compiles `has_attachment: true|false`.
func compileHasAttachment(n *yaml.Node, path string) (Matcher, error) {
	s, err := scalar(n, path)
	if err != nil {
		return nil, err
	}
	want, err := strconv.ParseBool(strings.TrimSpace(s))
	if err != nil {
		return nil, errAt(path, "expected true or false, got %q", s)
	}
	return MatcherFunc(func(msg *model.Message) bool {
		return msg.HasAttachment() == want
	}), nil
}

// compileLabel compiles `label: name` — exact, case-sensitive membership.
func compileLabel(n *yaml.Node, path string) (Matcher, error) {
	label, err := scalar(n, path)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(label) == "" {
		return nil, errAt(path, "must not be empty")
	}
	return MatcherFunc(func(msg *model.Message) bool { return msg.HasLabel(label) }), nil
}

// compileAge compiles `older_than:` (older) or `newer_than:`. A message with no
// usable Date matches neither.
func compileAge(n *yaml.Node, path string, o *options, older bool) (Matcher, error) {
	s, err := scalar(n, path)
	if err != nil {
		return nil, err
	}
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return nil, errAt(path, "invalid duration %q (want a Go duration such as \"720h\")", s)
	}
	if d <= 0 {
		return nil, errAt(path, "duration must be positive, got %q", s)
	}
	now := o.now
	return MatcherFunc(func(msg *model.Message) bool {
		if msg.Date.IsZero() {
			return false
		}
		age := now().Sub(msg.Date)
		if older {
			return age > d
		}
		return age < d
	}), nil
}

// compilePattern compiles an RE2 pattern once, reporting where it came from.
func compilePattern(pattern, path string) (*regexp.Regexp, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, errAt(path, "invalid regular expression %q: %v", pattern, err)
	}
	return re, nil
}

// domainsMatch reports whether any of have equals or is a subdomain of any of
// want. Both sides are expected to be normalized already.
func domainsMatch(have, want []string) bool {
	if len(have) == 0 || len(want) == 0 {
		return false
	}
	for _, d := range have {
		d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
		for _, w := range want {
			if d == w || strings.HasSuffix(d, "."+w) {
				return true
			}
		}
	}
	return false
}

// NormalizeDomain lowercases a domain, trims surrounding whitespace, and drops
// a leading "@" and any trailing dot, so that `@Example.COM.` and `example.com`
// are the same list entry.
func NormalizeDomain(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "@")
	s = strings.TrimSuffix(s, ".")
	return strings.ToLower(strings.TrimSpace(s))
}

// nullTag is the YAML tag of an explicit null scalar.
const nullTag = "!!null"

// resolve unwraps document nodes and follows aliases so callers only ever see
// the node that carries the value.
func resolve(n *yaml.Node) *yaml.Node {
	for i := 0; n != nil && i < 16; i++ {
		switch {
		case n.Kind == yaml.DocumentNode && len(n.Content) == 1:
			n = n.Content[0]
		case n.Kind == yaml.AliasNode && n.Alias != nil:
			n = n.Alias
		default:
			return n
		}
	}
	return n
}

// scalar returns the value of a scalar node, or an error naming path.
func scalar(n *yaml.Node, path string) (string, error) {
	n = resolve(n)
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag == nullTag {
		return "", errAt(path, "expected a single value, got %s", kindName(n))
	}
	return n.Value, nil
}

// stringList returns the values of a non-empty sequence of scalars. noun names
// the elements in error messages.
func stringList(n *yaml.Node, path, noun string) ([]string, error) {
	n = resolve(n)
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil, errAt(path, "expected a list of %ss, got %s", noun, kindName(n))
	}
	if len(n.Content) == 0 {
		return nil, errAt(path, "must list at least one %s", noun)
	}
	out := make([]string, 0, len(n.Content))
	for i, c := range n.Content {
		s, err := scalar(c, indexPath(path, i))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// mappingKeys returns the keys of a mapping node, sorted, for diagnostics.
func mappingKeys(n *yaml.Node) []string {
	keys := make([]string, 0, len(n.Content)/2)
	for i := 0; i < len(n.Content); i += 2 {
		keys = append(keys, n.Content[i].Value)
	}
	sort.Strings(keys)
	return keys
}

// kindName renders a node's kind for error messages.
func kindName(n *yaml.Node) string {
	if n == nil {
		return "nothing"
	}
	switch n.Kind {
	case yaml.DocumentNode:
		return "a document"
	case yaml.SequenceNode:
		return "a list"
	case yaml.MappingNode:
		return "a mapping"
	case yaml.AliasNode:
		return "an alias"
	case yaml.ScalarNode:
		if n.Tag == nullTag {
			return "null"
		}
		return fmt.Sprintf("the value %q", n.Value)
	default:
		return "nothing"
	}
}

// joinPath appends a key to a match-tree path.
func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// indexPath appends a list index to a match-tree path.
func indexPath(prefix string, i int) string {
	return prefix + "[" + strconv.Itoa(i) + "]"
}

// errAt builds an error prefixed with the location inside the match tree.
func errAt(path, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if path == "" {
		return fmt.Errorf("%s", msg)
	}
	return fmt.Errorf("%s: %s", path, msg)
}
