package filter

import (
	"fmt"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/model"
)

// Engine holds the compiled rules of one config and routes messages to the
// first rule that claims them.
//
// It is safe for concurrent Evaluate calls; BeginCycle is not meant to race
// with them (the pipeline calls it between cycles, not during one).
type Engine struct {
	rules []compiledRule
	files *Files
	// fileRefs is every externally-owned file the compiled rules reference,
	// deduplicated, in first-use order.
	fileRefs []FileRef
}

// compiledRule pairs a configured rule with its compiled match tree.
type compiledRule struct {
	rule    *config.Rule
	matcher Matcher
}

// NewEngine compiles every rule, in config order. The first rule that fails to
// compile aborts the whole thing with an error naming the rule and the location
// inside its match tree.
//
// The returned Engine keeps pointers into rules, and Evaluate hands those
// pointers back, so callers must keep the slice alive and must not reorder or
// re-slice it afterwards. Pass cfg.Rules directly.
func NewEngine(rules []config.Rule, opts ...Option) (*Engine, error) {
	o := newOptions(opts)
	e := &Engine{
		rules: make([]compiledRule, 0, len(rules)),
		files: o.files,
	}
	for i := range rules {
		m, err := compileNode(&rules[i].Match, "", o)
		if err != nil {
			return nil, fmt.Errorf("rule %q: match: %w", rules[i].Name, err)
		}
		e.rules = append(e.rules, compiledRule{rule: &rules[i], matcher: m})
	}
	e.fileRefs = o.refs
	return e, nil
}

// BeginCycle starts a new pipeline cycle: every externally-owned file is
// re-read the next time a predicate needs it. The pipeline calls this once per
// `run` and once per daemon tick, before evaluating any message.
func (e *Engine) BeginCycle() {
	if e == nil {
		return
	}
	e.files.BeginCycle()
}

// Preload reads every externally-owned file the rules reference, now, instead
// of lazily on first use. The pipeline calls it straight after BeginCycle so
// that Degraded is authoritative before the first message is fetched — which is
// what makes "store nothing when a list is unreadable" implementable.
func (e *Engine) Preload() {
	if e == nil {
		return
	}
	e.files.Preload(e.fileRefs)
}

// Degraded reports the externally-owned files that could not be read in full
// during this cycle.
//
// A non-empty result means some predicate could not really be evaluated, so
// every message the cycle called "no match" is suspect and advancing the cursor
// past it would consume wanted mail. The pipeline turns that into the
// configured `on_degraded_filter` behaviour; the engine only reports it.
func (e *Engine) Degraded() []DegradedFile {
	if e == nil {
		return nil
	}
	return e.files.Degraded()
}

// FileRefs returns every externally-owned file the compiled rules reference,
// with the parser each one is read by, deduplicated and in first-use order.
func (e *Engine) FileRefs() []FileRef {
	if e == nil {
		return nil
	}
	return append([]FileRef(nil), e.fileRefs...)
}

// DomainFilePaths returns every `from_domains_file` path the compiled rules
// reference, deduplicated, in first-use order.
func (e *Engine) DomainFilePaths() []string { return e.filePaths(FileKindDomains) }

// RegexFilePaths returns every `from_regex_file` path the compiled rules
// reference, deduplicated, in first-use order.
func (e *Engine) RegexFilePaths() []string { return e.filePaths(FileKindPatterns) }

// filePaths returns the referenced paths of one kind, in first-use order.
func (e *Engine) filePaths(kind FileKind) []string {
	if e == nil {
		return nil
	}
	var out []string
	for _, ref := range e.fileRefs {
		if ref.Kind == kind {
			out = append(out, ref.Path)
		}
	}
	return out
}

// Evaluate returns the first rule in config order that is in scope for the
// message's account and whose match tree accepts it, or nil when no rule
// claims the message. A message no rule claims is simply not stored; the
// caller still advances its state so the message is never re-evaluated —
// unless Degraded reports that some predicate could not be evaluated at all.
func (e *Engine) Evaluate(msg *model.Message) *config.Rule {
	if e == nil || msg == nil {
		return nil
	}
	for _, cr := range e.rules {
		if !cr.rule.AppliesTo(msg.Account) {
			continue
		}
		if cr.matcher.Match(msg) {
			return cr.rule
		}
	}
	return nil
}

// Len reports how many rules the engine holds.
func (e *Engine) Len() int {
	if e == nil {
		return 0
	}
	return len(e.rules)
}

// Files exposes the engine's per-cycle cache of externally-owned files, for
// callers that want to share it with something they compiled themselves.
func (e *Engine) Files() *Files {
	if e == nil {
		return nil
	}
	return e.files
}

// DomainFiles is the former name of Files.
//
// Deprecated: use Files.
func (e *Engine) DomainFiles() *Files { return e.Files() }
