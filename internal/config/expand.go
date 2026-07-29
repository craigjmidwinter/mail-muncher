package config

import (
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// filePathPredicates names the match-tree predicate keys whose scalar value is
// a filesystem path, so that Load can expand them and Validate can check them.
//
// TODO(filter-engine): once internal/filter owns the match schema this set
// should come from there instead of being duplicated here.
// A predicate missing from this set silently loses both `~`/`$VAR` expansion
// and the "file does not exist yet" warning, so every new file-valued predicate
// must be added here as well as to internal/filter.
var filePathPredicates = map[string]bool{
	"from_domains_file": true,
	"from_regex_file":   true,
}

// ExpandPath expands `$VAR` / `${VAR}` references and a leading `~` in a
// filesystem path.
//
// Environment expansion runs first (so `$HOME/mail` works), then a leading
// `~` or `~/` is replaced with the current user's home directory. `~user`
// forms are not supported and are left untouched. Undefined variables expand
// to the empty string, matching shell behaviour. An empty path stays empty.
func ExpandPath(p string) string {
	if p == "" {
		return ""
	}

	p = os.ExpandEnv(p)

	switch {
	case p == "~":
		if home := homeDir(); home != "" {
			return home
		}
	case strings.HasPrefix(p, "~/"):
		if home := homeDir(); home != "" {
			return home + p[1:]
		}
	}

	return p
}

// homeDir returns the user's home directory, preferring $HOME so that tests
// (and `env HOME=...` invocations) can redirect it.
func homeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// expandPathsInNode walks an opaque match tree and expands every scalar value
// held under a path-valued predicate key.
func expandPathsInNode(n *yaml.Node) {
	walkFilePathPredicates(n, "", func(_, _ string, v *yaml.Node) {
		v.Value = ExpandPath(v.Value)
	})
}

// filePathRef is one path-valued predicate found in a match tree.
type filePathRef struct {
	// Path is a dotted/bracketed location within the match node, e.g.
	// "any[0].from_domains_file".
	Path string
	// Key is the predicate itself, e.g. "from_domains_file". It decides how the
	// file's *contents* are checked; see FileContentValidator.
	Key string
	// Value is the expanded filesystem path.
	Value string
	// Line is the 1-based line in the config file, or 0 if unknown.
	Line int
}

// collectFilePathRefs returns every path-valued predicate in a match tree.
func collectFilePathRefs(n *yaml.Node) []filePathRef {
	var refs []filePathRef
	walkFilePathPredicates(n, "", func(path, key string, v *yaml.Node) {
		refs = append(refs, filePathRef{Path: path, Key: key, Value: v.Value, Line: v.Line})
	})
	return refs
}

// walkFilePathPredicates invokes fn for each scalar value stored under a key
// listed in filePathPredicates, passing a human-readable location and the
// predicate key itself.
func walkFilePathPredicates(n *yaml.Node, prefix string, fn func(path, key string, value *yaml.Node)) {
	if n == nil {
		return
	}

	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			walkFilePathPredicates(c, prefix, fn)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			child := k.Value
			if prefix != "" {
				child = prefix + "." + k.Value
			}
			if k.Kind == yaml.ScalarNode && filePathPredicates[k.Value] && v.Kind == yaml.ScalarNode {
				fn(child, k.Value, v)
				continue
			}
			walkFilePathPredicates(v, child, fn)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			walkFilePathPredicates(c, indexPath(prefix, i), fn)
		}
	}
}

// indexPath renders prefix[i], tolerating an empty prefix.
func indexPath(prefix string, i int) string {
	return prefix + "[" + strconv.Itoa(i) + "]"
}
