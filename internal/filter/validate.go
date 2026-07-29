package filter

import (
	"fmt"
	"os"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
)

// init installs the file-content checker as config's validation hook, so
// `mail-muncher validate` can say something about what is *inside* an
// externally-owned file and not only whether it exists.
//
// It is installed here rather than from cmd/ (the way config.MatchValidator is)
// because it needs this package's parsers and config cannot import them. Every
// consumer that imports internal/filter — the CLI, the daemon, the MCP server —
// gets it, and every one of them wants it.
func init() {
	config.FileContentValidator = describeFileProblems
}

// describeFileProblems reports what `validate` should say about the contents of
// one externally-owned file. Each returned string becomes a warning.
//
// Warnings only. The file belongs to another program; a pattern it cannot
// compile costs one subscription, not a usable config, and the running pipeline
// already reports it through `on_degraded_filter`. `validate` exists so the
// same fact is visible before a cycle runs.
func describeFileProblems(predicate, path string) []string {
	if predicate != "from_regex_file" {
		// A domain list has no contents that can fail to parse: an entry that
		// does not look like a domain is kept and logged, so there is nothing
		// for validate to add that a cycle would not already say.
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return []string{fmt.Sprintf("cannot be read: %v", err)}
	}

	// Logging off: `validate` is not a cycle, and the per-cycle load counters
	// would be misleading in its output.
	patterns, perr := parsePatterns(data, path, false)

	var out []string
	if perr != nil {
		out = append(out, perr.Error())
	}
	if len(patterns) == 0 {
		out = append(out, "lists no usable patterns; this predicate matches nothing until it does")
	}
	return out
}
