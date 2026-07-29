package mcpserver

import (
	"errors"
	"io/fs"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/craigmidwinter/mail-muncher/internal/config"
	"github.com/craigmidwinter/mail-muncher/internal/filter"
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

// RuleInfo is one configured rule as list_rules reports it.
type RuleInfo struct {
	Name           string           `json:"name"`
	Account        string           `json:"account,omitempty" jsonschema:"the account this rule is restricted to; absent means every account"`
	Accounts       []string         `json:"accounts" jsonschema:"the configured accounts this rule actually applies to"`
	Dest           string           `json:"dest" jsonschema:"directory matching messages are written to"`
	Formats        []string         `json:"formats" jsonschema:"renderings this rule writes"`
	DomainFiles    []DomainFileInfo `json:"domain_files,omitempty" jsonschema:"every from_domains_file the rule's match tree references, resolved as of this call"`
	StoredMessages int              `json:"stored_messages" jsonschema:"messages currently on disk under this rule's dest"`
}

// ListRulesOutput is what list_rules returns.
type ListRulesOutput struct {
	Rules []RuleInfo `json:"rules" jsonschema:"the configured rules, in config order"`
}

// listRules answers "what am I currently subscribed to".
//
// The point of the tool is the resolution: every `from_domains_file` is read
// here, at call time, not echoed as a path. An agent that rewrote its
// wanted-senders file a second ago sees the new list, which is the same
// freshness guarantee the pipeline gives itself at the top of each cycle.
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
		for _, path := range domainFilePaths(&rule.Match) {
			info.DomainFiles = append(info.DomainFiles, resolveDomainFile(path))
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

// domainFilePaths collects every `from_domains_file` value in a match tree, in
// document order, without duplicates.
//
// It reads the raw YAML rather than compiling the tree because compiling
// throws the paths away: filter.Compile closes over the file name inside a
// matcher and never hands it back.
func domainFilePaths(node *yaml.Node) []string {
	var (
		out  []string
		seen = make(map[string]bool)
	)
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
				if key.Value == "from_domains_file" && value.Kind == yaml.ScalarNode && value.Value != "" {
					if !seen[value.Value] {
						seen[value.Value] = true
						out = append(out, value.Value)
					}
					continue
				}
				walk(value, depth+1)
			}
		}
	}
	walk(node, 0)
	return out
}
