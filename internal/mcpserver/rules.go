package mcpserver

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/filter"
)

// maxMatchDepth bounds the walk over a rule's match tree. YAML aliases can
// point backwards, and a bounded walk is simpler than cycle detection for a
// tree this shallow.
const maxMatchDepth = 64

// DomainFileInfo is one `from_domains_file` predicate, resolved.
//
// The path is deliberately included: the file belongs to the agent (or to
// whatever program curates its wanted-senders list), so naming it is how the
// agent knows which of its own files this rule reads. Nothing else about the
// configuration is exposed — not the config file, not credential or token
// files, not the state directory.
type DomainFileInfo struct {
	Path       string    `json:"path" jsonschema:"the domain list file this rule reads, as configured"`
	Exists     bool      `json:"exists" jsonschema:"whether the file was readable at the time of this call"`
	Domains    []string  `json:"domains" jsonschema:"the domains currently listed in the file, normalized and de-duplicated"`
	Count      int       `json:"count"`
	ModifiedAt time.Time `json:"modified_at,omitempty" jsonschema:"when the file was last written"`
	Note       string    `json:"note,omitempty" jsonschema:"why the list is empty, when it is"`
}

// PatternFileInfo is one `from_regex_file` predicate, resolved.
//
// It is a sibling of DomainFileInfo rather than a generalization of it. The two
// carry different facts — a domain list has no way to fail to parse, a pattern
// list has breadth guards that can reject a line — and `domain_files` is already
// a published part of this tool's output. A `kind`-tagged union would have cost
// every existing consumer a rename in exchange for a shape in which half the
// fields are always empty.
//
// Rejected is the field that earns its place. A generator that emitted one
// catch-all instead of twelve patterns shows up here as `count: 1,
// rejected: 11`, which is something an agent can tell its operator, and which
// a list of the patterns that survived would not reveal on its own.
//
// The rejected lines' own text is deliberately not returned. Its size is
// bounded by nothing but the file, which mail-muncher does not own, and a tool
// result is a bad place to put an arbitrary quantity of another program's
// output. `mail-muncher validate` prints the offending lines for a human; the
// count plus the note is what an agent needs to know something is wrong.
type PatternFileInfo struct {
	Path       string    `json:"path" jsonschema:"the pattern list file this rule reads, as configured"`
	Exists     bool      `json:"exists" jsonschema:"whether the file was readable at the time of this call"`
	Patterns   []string  `json:"patterns" jsonschema:"the RE2 patterns currently in force, as written in the file, de-duplicated"`
	Count      int       `json:"count" jsonschema:"how many patterns loaded"`
	Rejected   int       `json:"rejected" jsonschema:"how many lines were refused because they do not compile or would match every message; they are not in force and their text is not returned"`
	ModifiedAt time.Time `json:"modified_at,omitempty" jsonschema:"when the file was last written"`
	Note       string    `json:"note,omitempty" jsonschema:"why the list is empty or short, when it is"`
}

// RuleInfo is one configured rule as list_rules reports it.
type RuleInfo struct {
	Name           string            `json:"name"`
	Account        string            `json:"account,omitempty" jsonschema:"the account this rule is restricted to; absent means every account"`
	Accounts       []string          `json:"accounts" jsonschema:"the configured accounts this rule actually applies to"`
	Dest           string            `json:"dest" jsonschema:"directory matching messages are written to"`
	Formats        []string          `json:"formats" jsonschema:"renderings this rule writes"`
	DomainFiles    []DomainFileInfo  `json:"domain_files,omitempty" jsonschema:"every from_domains_file the rule's match tree references, resolved as of this call"`
	PatternFiles   []PatternFileInfo `json:"pattern_files,omitempty" jsonschema:"every from_regex_file the rule's match tree references, resolved as of this call"`
	StoredMessages int               `json:"stored_messages" jsonschema:"messages currently on disk under this rule's dest"`
}

// ListRulesOutput is what list_rules returns.
type ListRulesOutput struct {
	Rules []RuleInfo `json:"rules" jsonschema:"the configured rules, in config order"`
}

// listRules answers "what am I currently subscribed to".
//
// The point of the tool is the resolution: every externally-owned file a rule
// reads — every `from_domains_file` and every `from_regex_file` — is read here,
// at call time, not echoed as a path. An agent that rewrote its wanted-senders
// file a second ago sees the new list, which is the same freshness guarantee
// the pipeline gives itself at the top of each cycle.
//
// Both kinds are resolved because answering with only one is worse than not
// answering: an agent that sees the domain subscriptions and silently misses
// the pattern subscriptions gets a confident, incomplete picture of what it is
// collecting.
//
// A missing file is an empty list plus a note, never an error: the program
// that owns the file may simply not have written it yet.
func (s *Server) listRules() (ListRulesOutput, error) {
	records, err := s.archive.Index()
	if err != nil {
		return ListRulesOutput{}, err
	}
	stored := make(map[string]int, len(s.cfg.Rules))
	for _, r := range records {
		stored[r.rule]++
	}

	out := ListRulesOutput{Rules: make([]RuleInfo, 0, len(s.cfg.Rules))}
	for i := range s.cfg.Rules {
		rule := &s.cfg.Rules[i]

		info := RuleInfo{
			Name:           rule.Name,
			Account:        rule.Account,
			Accounts:       accountsFor(s.cfg, rule),
			Dest:           rule.Dest,
			Formats:        formatNames(rule.Formats),
			StoredMessages: stored[rule.Name],
		}
		domains, patterns := matchFilePaths(&rule.Match)
		for _, path := range domains {
			info.DomainFiles = append(info.DomainFiles, resolveDomainFile(path))
		}
		for _, path := range patterns {
			info.PatternFiles = append(info.PatternFiles, resolvePatternFile(path))
		}
		out.Rules = append(out.Rules, info)
	}
	return out, nil
}

// accountsFor lists the configured accounts a rule is in scope for.
func accountsFor(cfg *config.Config, rule *config.Rule) []string {
	out := make([]string, 0, len(cfg.Accounts))
	for i := range cfg.Accounts {
		if rule.AppliesTo(cfg.Accounts[i].Name) {
			out = append(out, cfg.Accounts[i].Name)
		}
	}
	return out
}

// resolveDomainFile reads one domain list, right now.
//
// Parsing goes through internal/filter so the answer is exactly the list the
// pipeline would match against — same comment handling, same `@` stripping,
// same normalization and de-duplication. The nil receiver is deliberate: it
// bypasses the per-cycle cache, so every call re-reads the file.
func resolveDomainFile(path string) DomainFileInfo {
	info := DomainFileInfo{Path: path, Domains: []string{}}

	st, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		info.Note = "file does not exist yet; this predicate currently matches nothing"
		return info
	case err != nil:
		info.Note = "file could not be read; this predicate currently matches nothing"
		return info
	case st.IsDir():
		info.Note = "path is a directory, not a domain list; this predicate currently matches nothing"
		return info
	}

	info.Exists = true
	info.ModifiedAt = st.ModTime().UTC()
	if domains := (*filter.DomainFiles)(nil).Domains(path); len(domains) > 0 {
		info.Domains = domains
	} else {
		info.Note = "file is empty or lists no usable domains; this predicate currently matches nothing"
	}
	info.Count = len(info.Domains)
	return info
}

// resolvePatternFile reads one pattern list, right now.
//
// Loading goes through internal/filter, exactly as resolveDomainFile does, so
// the patterns reported are the ones the pipeline would actually match on:
// same comment handling, same de-duplication, and — the part that matters —
// the same breadth guards, so a line that would have matched every message is
// reported as rejected here because it is rejected there too. The nil receiver
// bypasses the per-cycle cache, so every call re-reads the file.
func resolvePatternFile(path string) PatternFileInfo {
	info := PatternFileInfo{Path: path, Patterns: []string{}}

	st, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		info.Note = "file does not exist yet; this predicate currently matches nothing"
		return info
	case err != nil:
		info.Note = "file could not be read; this predicate currently matches nothing"
		return info
	case st.IsDir():
		info.Note = "path is a directory, not a pattern list; this predicate currently matches nothing"
		return info
	}

	info.Exists = true
	info.ModifiedAt = st.ModTime().UTC()
	for _, re := range (*filter.Files)(nil).Patterns(path) {
		// Regexp.String returns the source text it was compiled from, so this
		// is the line as the owning program wrote it, not a re-rendering.
		info.Patterns = append(info.Patterns, re.String())
	}
	info.Count = len(info.Patterns)
	info.Rejected = countRejected(path, info.Patterns)
	info.Note = patternNote(info.Count, info.Rejected)
	return info
}

// patternNote explains a pattern list that is empty, short, or both. An empty
// note means "nothing to say": every line in the file is in force.
func patternNote(loaded, rejected int) string {
	const why = "a line is refused when it does not compile, or when it would match every message"
	switch {
	case loaded == 0 && rejected == 0:
		return "file is empty or lists no usable patterns; this predicate currently matches nothing"
	case loaded == 0:
		return fmt.Sprintf("%s rejected and nothing loaded: %s. This predicate currently matches nothing",
			plural(rejected, "line"), why)
	case rejected > 0:
		return fmt.Sprintf("%s rejected: %s. Only the %s listed here are in force",
			plural(rejected, "line"), why, plural(loaded, "pattern"))
	default:
		return ""
	}
}

// plural renders a count with its noun, so the notes read as sentences rather
// than as log lines.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// countRejected reports how many lines of a pattern list the filter package
// refused.
//
// It is derived rather than reported because internal/filter does not export
// the count: the nil-receiver read above returns the patterns that survived and
// drops the parse error, and the error is only reachable through a live
// Files.Degraded, as prose. So the census here re-walks the file with the same
// line conventions the pattern parser uses — trim, skip blanks, `#` only at the
// start of a line, same 1 MiB line cap — and calls a line rejected when its
// text is not among the patterns that loaded. Duplicates of a loaded pattern
// are not rejected, and repeats of a bad line each count, which is exactly how
// the parser counts them.
//
// It is a second read of the file, so a file rewritten between the two reads
// can be under-counted for one call. That is the same window the existing
// stat-then-read of a domain list has, and the next call closes it.
func countRejected(path string, loaded []string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}

	inForce := make(map[string]bool, len(loaded))
	for _, p := range loaded {
		inForce[p] = true
	}

	rejected := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if !inForce[text] {
			rejected++
		}
	}
	return rejected
}

// domainFilePaths collects every `from_domains_file` value in a match tree.
func domainFilePaths(node *yaml.Node) []string {
	domains, _ := matchFilePaths(node)
	return domains
}

// regexFilePaths collects every `from_regex_file` value in a match tree.
func regexFilePaths(node *yaml.Node) []string {
	_, patterns := matchFilePaths(node)
	return patterns
}

// matchFilePaths collects every externally-owned file a match tree references,
// split by the parser that reads it, in document order, without duplicates.
//
// It reads the raw YAML rather than compiling the tree because compiling
// throws the paths away: filter.Compile closes over the file name inside a
// matcher and never hands it back. filter.Engine does keep them — FileRefs
// reports both kinds — but the engine is built over the whole config, and
// list_rules needs the answer per rule.
//
// Adding a third file-valued predicate is one line in fileKeys, not a third
// walk.
func matchFilePaths(node *yaml.Node) (domains, patterns []string) {
	// fileKeys maps a predicate to the bucket its path belongs in. A predicate
	// reading the same file format as an existing one shares its bucket.
	fileKeys := map[string]*[]string{
		"from_domains_file": &domains,
		"from_regex_file":   &patterns,
	}

	seen := make(map[string]bool)
	var walk func(n *yaml.Node, depth int)
	walk = func(n *yaml.Node, depth int) {
		if n == nil || depth > maxMatchDepth {
			return
		}
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, c := range n.Content {
				walk(c, depth+1)
			}
		case yaml.AliasNode:
			walk(n.Alias, depth+1)
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				key, value := n.Content[i], n.Content[i+1]
				if bucket, ok := fileKeys[key.Value]; ok && value.Kind == yaml.ScalarNode && value.Value != "" {
					// Deduplication is per predicate, not per path: the same
					// file read by two different parsers is two subscriptions.
					id := key.Value + "\x00" + value.Value
					if !seen[id] {
						seen[id] = true
						*bucket = append(*bucket, value.Value)
					}
					continue
				}
				walk(value, depth+1)
			}
		}
	}
	walk(node, 0)
	return domains, patterns
}
