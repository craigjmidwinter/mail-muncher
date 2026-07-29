package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Severity distinguishes problems that must be fixed from ones worth knowing
// about.
type Severity string

const (
	// SeverityError means the config cannot be used as written.
	SeverityError Severity = "error"
	// SeverityWarning means the config is usable but something looks off —
	// typically a referenced file another program has not written yet.
	SeverityWarning Severity = "warning"
)

// Problem is one validation finding.
type Problem struct {
	// Severity is SeverityError or SeverityWarning.
	Severity Severity
	// Field locates the problem in the config, e.g. "rules[1].formats".
	Field string
	// Message describes the problem.
	Message string
}

func (p Problem) String() string {
	if p.Field == "" {
		return fmt.Sprintf("%s: %s", p.Severity, p.Message)
	}
	return fmt.Sprintf("%s: %s: %s", p.Severity, p.Field, p.Message)
}

// Problems is an ordered list of validation findings.
type Problems []Problem

// HasErrors reports whether any problem is error-severity.
func (ps Problems) HasErrors() bool { return len(ps.filter(SeverityError)) > 0 }

// Errors returns only the error-severity problems.
func (ps Problems) Errors() Problems { return ps.filter(SeverityError) }

// Warnings returns only the warning-severity problems.
func (ps Problems) Warnings() Problems { return ps.filter(SeverityWarning) }

func (ps Problems) filter(s Severity) Problems {
	var out Problems
	for _, p := range ps {
		if p.Severity == s {
			out = append(out, p)
		}
	}
	return out
}

// Err returns a non-nil error summarizing the error-severity problems, or nil
// when there are none. Warnings never produce an error.
func (ps Problems) Err() error {
	errs := ps.Errors()
	if len(errs) == 0 {
		return nil
	}
	lines := make([]string, 0, len(errs))
	for _, p := range errs {
		if p.Field == "" {
			lines = append(lines, p.Message)
			continue
		}
		lines = append(lines, p.Field+": "+p.Message)
	}
	return fmt.Errorf("invalid config: %s", strings.Join(lines, "; "))
}

// MatchValidator is an optional hook for validating a rule's match tree.
//
// TODO(filter-engine): internal/filter should set this (or `validate` should
// call filter.Compile directly) so that a malformed match tree — unknown
// predicate, multiple keys in one node, bad regex or duration — is reported by
// `mail-muncher validate` with the rule name and key path. Until then the
// match node is only checked for presence.
var MatchValidator func(rule *Rule) error

// Validate checks a loaded config for semantic problems and returns every
// finding in config order. It is shared by the `validate` command and by
// run/daemon startup; callers that only care whether the config is usable can
// use Problems.Err.
//
// Referenced files that another program owns — a rule's `from_domains_file`,
// the OAuth credential and token files — are warnings when missing, not
// errors: they may legitimately not exist yet.
func Validate(cfg *Config) Problems {
	var ps Problems
	if cfg == nil {
		return append(ps, Problem{Severity: SeverityError, Message: "no config loaded"})
	}

	add := func(sev Severity, field, format string, args ...any) {
		ps = append(ps, Problem{Severity: sev, Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if strings.TrimSpace(cfg.StateDir) == "" {
		add(SeverityError, "state_dir", "must not be empty")
	}

	validatePolicies(cfg, add)

	accounts := validateAccounts(cfg, add)
	validateRules(cfg, accounts, add)

	return ps
}

// addFunc records a problem; split out so the validators below stay readable.
type addFunc func(sev Severity, field, format string, args ...any)

// validatePolicies checks the two cycle policies and the quarantine directory.
//
// An unrecognized policy is always an error: silently falling back to the
// default would run a different failure policy than the file asks for, and both
// keys exist precisely to decide what happens to mail.
func validatePolicies(cfg *Config, add addFunc) {
	switch p := cfg.OnMessageFailure; {
	case strings.TrimSpace(string(p)) == "":
		// Omitted: Load defaults it, and MessageFailure() defaults it again for
		// configs built in code.
	case !p.Valid():
		add(SeverityError, "on_message_failure", "unknown value %q (want one of: %s, %s)",
			string(p), MessageFailureQuarantine, MessageFailureAbort)
	}

	switch p := cfg.OnDegradedFilter; {
	case strings.TrimSpace(string(p)) == "":
	case !p.Valid():
		add(SeverityError, "on_degraded_filter", "unknown value %q (want one of: %s, %s, %s)",
			string(p), DegradedFilterHold, DegradedFilterFail, DegradedFilterProceed)
	case p == DegradedFilterProceed:
		add(SeverityWarning, "on_degraded_filter",
			"%q accepts silent loss: while a `from_domains_file` is unreadable every message is evaluated against an empty list and the cursor advances past it anyway",
			string(DegradedFilterProceed))
	}

	if cfg.MessageFailure() == MessageFailureQuarantine && strings.TrimSpace(cfg.QuarantineRoot()) == "" {
		add(SeverityError, "quarantine_dir",
			"must not be empty when on_message_failure is %q", string(MessageFailureQuarantine))
	}
}

// validateAccounts checks the accounts block and returns the set of names
// rules may legally reference.
func validateAccounts(cfg *Config, add addFunc) map[string]bool {
	names := make(map[string]bool, len(cfg.Accounts))

	if len(cfg.Accounts) == 0 {
		add(SeverityError, "accounts", "at least one account is required")
		return names
	}

	seen := make(map[string]int, len(cfg.Accounts))
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		field := fmt.Sprintf("accounts[%d]", i)

		switch {
		case strings.TrimSpace(a.Name) == "":
			add(SeverityError, field+".name", "must not be empty")
		default:
			if first, dup := seen[a.Name]; dup {
				add(SeverityError, field+".name", "duplicate account name %q (already defined at accounts[%d])", a.Name, first)
			} else {
				seen[a.Name] = i
				names[a.Name] = true
			}
		}

		if a.Provider != ProviderGmail {
			add(SeverityError, field+".provider", "unknown provider %q (known providers: %s)", a.Provider, ProviderGmail)
			continue
		}

		if a.Gmail == nil {
			add(SeverityError, field+".gmail", "required for provider %q", ProviderGmail)
			continue
		}
		validateGmail(a.Gmail, field+".gmail", add)
	}

	return names
}

// validateGmail checks one account's gmail block.
func validateGmail(g *GmailConfig, field string, add addFunc) {
	if strings.TrimSpace(g.CredentialsFile) == "" {
		add(SeverityError, field+".credentials_file", "must not be empty")
	} else if !fileExists(g.CredentialsFile) {
		add(SeverityWarning, field+".credentials_file", "file does not exist: %s", g.CredentialsFile)
	}

	if strings.TrimSpace(g.TokenFile) == "" {
		add(SeverityError, field+".token_file", "must not be empty")
	} else if !fileExists(g.TokenFile) {
		add(SeverityWarning, field+".token_file", "file does not exist yet: %s (run `mail-muncher auth`)", g.TokenFile)
	}

	switch d, err := time.ParseDuration(g.InitialLookback); {
	case err != nil:
		add(SeverityError, field+".initial_lookback", "invalid duration %q (want a Go duration such as %q)", g.InitialLookback, DefaultInitialLookback)
	case d <= 0:
		add(SeverityError, field+".initial_lookback", "must be positive, got %q", g.InitialLookback)
	}
}

// validateRules checks the rules block against the known account names.
func validateRules(cfg *Config, accounts map[string]bool, add addFunc) {
	if len(cfg.Rules) == 0 {
		add(SeverityWarning, "rules", "no rules configured; no messages will be archived")
		return
	}

	seen := make(map[string]int, len(cfg.Rules))
	for i := range cfg.Rules {
		r := &cfg.Rules[i]
		field := fmt.Sprintf("rules[%d]", i)

		switch {
		case strings.TrimSpace(r.Name) == "":
			add(SeverityError, field+".name", "must not be empty")
		default:
			if first, dup := seen[r.Name]; dup {
				add(SeverityError, field+".name", "duplicate rule name %q (already defined at rules[%d])", r.Name, first)
			} else {
				seen[r.Name] = i
			}
		}

		if r.Account != "" && !accounts[r.Account] {
			add(SeverityError, field+".account", "unknown account %q", r.Account)
		}

		if strings.TrimSpace(r.Dest) == "" {
			add(SeverityError, field+".dest", "must not be empty")
		}

		validateFormats(r, field, add)
		validateMatch(r, field, add)
	}
}

// validateFormats checks a rule's output formats.
func validateFormats(r *Rule, field string, add addFunc) {
	seen := make(map[Format]bool, len(r.Formats))
	for j, f := range r.Formats {
		switch {
		case !f.Valid():
			add(SeverityError, fmt.Sprintf("%s.formats[%d]", field, j),
				"unknown format %q (want one of: %s, %s)", string(f), FormatEML, FormatMarkdown)
		case seen[f]:
			add(SeverityWarning, fmt.Sprintf("%s.formats[%d]", field, j), "duplicate format %q", string(f))
		default:
			seen[f] = true
		}
	}
}

// validateMatch checks a rule's match tree: that it exists, that any external
// files it points at are readable, and — once the hook is wired — that it
// compiles.
func validateMatch(r *Rule, field string, add addFunc) {
	if r.Match.IsZero() || (r.Match.Kind == yaml.ScalarNode && r.Match.Tag == "!!null") {
		add(SeverityError, field+".match", "required (a rule with no match would archive every message)")
		return
	}

	for _, ref := range collectFilePathRefs(&r.Match) {
		refField := field + ".match." + ref.Path
		if strings.TrimSpace(ref.Value) == "" {
			add(SeverityError, refField, "must not be empty")
			continue
		}
		if !fileExists(ref.Value) {
			add(SeverityWarning, refField,
				"file does not exist yet: %s (it is maintained by another program; the rule matches nothing until it appears)", ref.Value)
		}
	}

	if MatchValidator != nil {
		if err := MatchValidator(r); err != nil {
			add(SeverityError, field+".match", "%s", err.Error())
		}
	}
}

// fileExists reports whether path names an existing, non-directory file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// LoadAndValidate is the convenience entry point for run/daemon startup: it
// loads the config and validates it, returning an error if the file cannot be
// parsed or if validation produced any error-severity problem. The Problems
// are always returned so the caller can log warnings.
func LoadAndValidate(path string) (*Config, Problems, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, nil, err
	}
	ps := Validate(cfg)
	return cfg, ps, ps.Err()
}
