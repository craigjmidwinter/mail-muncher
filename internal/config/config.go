package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults applied by Load when a field is omitted.
const (
	// DefaultStateDir is the state directory used when `state_dir` is omitted.
	// It is expanded like any other path field.
	DefaultStateDir = "~/.local/state/mail-muncher"

	// DefaultInitialLookback is the `gmail.initial_lookback` used when the key
	// is omitted: how far back a first-ever sync reaches (30 days).
	DefaultInitialLookback = "720h"

	// ProviderGmail is the only provider recognized today. It is also the
	// default when `provider` is omitted.
	ProviderGmail = "gmail"

	// QuarantineDirName is the sub-directory of `state_dir` that `quarantine_dir`
	// defaults to.
	QuarantineDirName = "quarantine"
)

// MessageFailurePolicy decides what a cycle does with one message it could not
// deliver: one that will not parse, or one where a sink failed.
//
// The choice is between losing mail and wedging the cursor, so it is the user's
// to make. Neither option ever silently drops a message.
type MessageFailurePolicy string

const (
	// MessageFailureQuarantine writes the raw RFC822 bytes (plus a `.json`
	// sidecar naming the failure) under `quarantine_dir` and then lets the
	// provider cursor advance past the message. Nothing is lost, and a poison
	// message cannot wedge the pipeline forever. This is the default.
	//
	// If the quarantine write itself fails, the cycle falls back to
	// MessageFailureAbort semantics for that message rather than losing it.
	MessageFailureQuarantine MessageFailurePolicy = "quarantine"
	// MessageFailureAbort returns the failure to the provider, so the cursor
	// does not advance and the message is re-fetched next cycle.
	//
	// The trade-off is explicit: a permanently unparseable message wedges the
	// cursor under this setting — every cycle re-fetches it, fails on it again,
	// and nothing behind it is ever delivered until the message is dealt with
	// by hand.
	MessageFailureAbort MessageFailurePolicy = "abort"

	// DefaultMessageFailure is the `on_message_failure` used when the key is
	// omitted.
	DefaultMessageFailure = MessageFailureQuarantine
)

// Valid reports whether p is a recognized `on_message_failure` value.
func (p MessageFailurePolicy) Valid() bool {
	switch p {
	case MessageFailureQuarantine, MessageFailureAbort:
		return true
	default:
		return false
	}
}

// DegradedFilterPolicy decides what a cycle does when a rule's externally-owned
// domain list could not be read: the file does not exist yet, the program that
// owns it is mid-redeploy, or it is unreadable.
//
// Such a file matches nothing, so without a policy every message that cycle is
// evaluated against an empty list, found not to match, and consumed — the
// cursor advances past mail that was wanted.
type DegradedFilterPolicy string

const (
	// DegradedFilterHold runs the cycle and stores everything that did match,
	// logs the degradation at error level, but does not save the advanced
	// cursor — so the same mail is re-evaluated once the file returns. The
	// manifest reports `degraded` and `state_held`. This is the default.
	DegradedFilterHold DegradedFilterPolicy = "hold"
	// DegradedFilterFail ends the cycle before anything is fetched: nothing is
	// stored, nothing is advanced, and the process exits non-zero.
	DegradedFilterFail DegradedFilterPolicy = "fail"
	// DegradedFilterProceed treats an unreadable list as an empty one and
	// advances the cursor anyway. It is the fastest and the only option that
	// accepts silent loss of wanted mail.
	DegradedFilterProceed DegradedFilterPolicy = "proceed"

	// DefaultDegradedFilter is the `on_degraded_filter` used when the key is
	// omitted.
	DefaultDegradedFilter = DegradedFilterHold
)

// Valid reports whether p is a recognized `on_degraded_filter` value.
func (p DegradedFilterPolicy) Valid() bool {
	switch p {
	case DegradedFilterHold, DegradedFilterFail, DegradedFilterProceed:
		return true
	default:
		return false
	}
}

// Format names an on-disk rendering a matched message is written as.
type Format string

const (
	// FormatEML writes the byte-faithful RFC822 source as `.eml`.
	FormatEML Format = "eml"
	// FormatMarkdown writes a markdown rendering alongside the message.
	FormatMarkdown Format = "markdown"
)

// Valid reports whether f is a format mail-muncher knows how to write.
func (f Format) Valid() bool {
	switch f {
	case FormatEML, FormatMarkdown:
		return true
	default:
		return false
	}
}

// Config is the whole mail-muncher configuration file.
//
// Every path-valued field (StateDir, the gmail credential/token files, each
// rule's Dest, and `from_domains_file` values inside a rule's Match tree) has
// already had `~` and `$ENV_VAR` expanded by the time Load returns.
type Config struct {
	// StateDir holds sync cursors and other per-run bookkeeping.
	StateDir string `yaml:"state_dir"`
	// QuarantineDir is where messages that could not be delivered are parked
	// under `on_message_failure: quarantine`. Load defaults it to
	// <state_dir>/quarantine; read it through QuarantineRoot.
	QuarantineDir string `yaml:"quarantine_dir"`
	// OnMessageFailure is the cycle policy for a message that will not parse or
	// that a sink failed on. Load defaults it to DefaultMessageFailure; read it
	// through MessageFailure.
	OnMessageFailure MessageFailurePolicy `yaml:"on_message_failure"`
	// OnDegradedFilter is the cycle policy for a `from_domains_file` that could
	// not be read. Load defaults it to DefaultDegradedFilter; read it through
	// DegradedFilter.
	OnDegradedFilter DegradedFilterPolicy `yaml:"on_degraded_filter"`
	// Accounts are the mailboxes to pull from.
	Accounts []Account `yaml:"accounts"`
	// Rules are evaluated in order against every message; first match wins.
	Rules []Rule `yaml:"rules"`

	// Path is the file this Config was read from. It is not a YAML key.
	Path string `yaml:"-"`
}

// Account is one mailbox to pull mail from.
type Account struct {
	// Name is the unique identifier rules refer to via `Rule.Account`.
	Name string `yaml:"name"`
	// Provider selects the fetch backend. Only ProviderGmail is recognized;
	// it is also the default when the key is omitted.
	Provider string `yaml:"provider"`
	// Gmail carries the provider-specific settings; required (and only
	// meaningful) when Provider is ProviderGmail.
	Gmail *GmailConfig `yaml:"gmail"`
}

// GmailConfig holds the Gmail provider settings for one account.
type GmailConfig struct {
	// CredentialsFile is the OAuth client secret downloaded from GCP.
	CredentialsFile string `yaml:"credentials_file"`
	// TokenFile is where the OAuth token is cached; written by `auth`.
	TokenFile string `yaml:"token_file"`
	// Query is an optional Gmail server-side pre-filter, applied to the
	// first-ever full scan only (incremental history results are not
	// query-filtered, and a recovery scan drops the query deliberately).
	Query string `yaml:"query"`
	// InitialLookback is a Go duration string bounding how far back the
	// first-ever full scan reaches. Load defaults it to
	// DefaultInitialLookback; use InitialLookbackDuration to parse it.
	InitialLookback string `yaml:"initial_lookback"`
	// IncludeSpamTrash opts the account into fetching mail that Gmail filed
	// under Spam or Trash. It defaults to false, and the default is a safety
	// decision rather than a tidiness one: mail-muncher exists to put mail in
	// front of an AI agent, so everything it delivers ends up inside an LLM's
	// context window, and Spam is by definition the highest-density source of
	// hostile, attacker-authored text in a mailbox. Prompt injection arrives by
	// email like everything else. Excluding it by default keeps the obvious
	// attack surface out of the pipeline unless the operator asks for it.
	//
	// It is honoured identically by both sync routes — see the gmail provider —
	// so turning it on or off never makes the two disagree about which messages
	// exist. Read it through IncludesSpamTrash.
	IncludeSpamTrash bool `yaml:"include_spam_trash"`
}

// IncludesSpamTrash is the effective `gmail.include_spam_trash`: false (the
// default) unless the account opted in.
//
// It is the accessor every consumer must use, so a GmailConfig built in code
// behaves exactly like one Load defaulted.
func (g *GmailConfig) IncludesSpamTrash() bool {
	return g != nil && g.IncludeSpamTrash
}

// InitialLookbackDuration parses InitialLookback, falling back to
// DefaultInitialLookback when the field is empty or malformed. Validate
// reports malformed values as errors, so a validated config never falls back
// on account of a parse failure.
func (g *GmailConfig) InitialLookbackDuration() time.Duration {
	if g != nil && g.InitialLookback != "" {
		if d, err := time.ParseDuration(g.InitialLookback); err == nil {
			return d
		}
	}
	d, _ := time.ParseDuration(DefaultInitialLookback)
	return d
}

// Rule routes matching messages to a destination directory.
type Rule struct {
	// Name is the unique identifier used in logs and state.
	Name string `yaml:"name"`
	// Account restricts the rule to one account; empty means all accounts.
	Account string `yaml:"account"`
	// Match is the rule's match tree.
	//
	// TODO(filter-engine): this stays an opaque yaml.Node until
	// internal/filter lands. Once it does, the filter package should be
	// wired in through MatchValidator (see validate.go) so `validate`
	// compiles every tree, and consumers should compile Match into a
	// filter.Matcher rather than reading the node directly.
	Match yaml.Node `yaml:"match"`
	// Dest is the directory matched messages are written to; created if
	// missing.
	Dest string `yaml:"dest"`
	// Formats lists the renderings to write. Load defaults it to
	// [FormatEML].
	Formats []Format `yaml:"formats"`
}

// HasFormat reports whether the rule writes the given format.
func (r *Rule) HasFormat(f Format) bool {
	for _, got := range r.Formats {
		if got == f {
			return true
		}
	}
	return false
}

// AppliesTo reports whether the rule is in scope for the named account. A rule
// with no `account:` applies to every account.
func (r *Rule) AppliesTo(account string) bool {
	return r.Account == "" || r.Account == account
}

// MessageFailure is the effective `on_message_failure` policy: the configured
// value, or DefaultMessageFailure when the key was omitted.
//
// It is the accessor every consumer must use, so that a Config built in code
// (a test, the MCP server) behaves exactly like one Load defaulted.
func (c *Config) MessageFailure() MessageFailurePolicy {
	if c == nil || !c.OnMessageFailure.Valid() {
		return DefaultMessageFailure
	}
	return c.OnMessageFailure
}

// DegradedFilter is the effective `on_degraded_filter` policy: the configured
// value, or DefaultDegradedFilter when the key was omitted.
func (c *Config) DegradedFilter() DegradedFilterPolicy {
	if c == nil || !c.OnDegradedFilter.Valid() {
		return DefaultDegradedFilter
	}
	return c.OnDegradedFilter
}

// QuarantineRoot is the effective quarantine directory: `quarantine_dir` when
// set, otherwise <state_dir>/quarantine.
func (c *Config) QuarantineRoot() string {
	if c == nil {
		return ""
	}
	if dir := strings.TrimSpace(c.QuarantineDir); dir != "" {
		return dir
	}
	if dir := strings.TrimSpace(c.StateDir); dir != "" {
		return filepath.Join(dir, QuarantineDirName)
	}
	return ""
}

// Account returns the account with the given name, or nil.
func (c *Config) Account(name string) *Account {
	for i := range c.Accounts {
		if c.Accounts[i].Name == name {
			return &c.Accounts[i]
		}
	}
	return nil
}

// Load reads, decodes, defaults, and path-expands the config file at path.
//
// Unknown YAML keys are a hard error (yaml.Decoder.KnownFields). Load does not
// check for semantic problems such as duplicate names or missing referenced
// files — call Validate for that.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	defer func() { _ = f.Close() }()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: config file is empty", path)
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	// Reject trailing YAML documents rather than silently ignoring them.
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, fmt.Errorf("%s: config must contain exactly one YAML document", path)
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	cfg.Path = path
	cfg.applyDefaults()
	cfg.expandPaths()

	return &cfg, nil
}

// applyDefaults fills in the omitted-key defaults documented on each field.
func (c *Config) applyDefaults() {
	if strings.TrimSpace(c.StateDir) == "" {
		c.StateDir = DefaultStateDir
	}
	if strings.TrimSpace(c.QuarantineDir) == "" {
		c.QuarantineDir = filepath.Join(c.StateDir, QuarantineDirName)
	}

	// Policies are normalized but never corrected: an unrecognized value stays
	// as written so Validate can report it instead of silently running a
	// different policy than the file asks for.
	c.OnMessageFailure = MessageFailurePolicy(strings.ToLower(strings.TrimSpace(string(c.OnMessageFailure))))
	if c.OnMessageFailure == "" {
		c.OnMessageFailure = DefaultMessageFailure
	}
	c.OnDegradedFilter = DegradedFilterPolicy(strings.ToLower(strings.TrimSpace(string(c.OnDegradedFilter))))
	if c.OnDegradedFilter == "" {
		c.OnDegradedFilter = DefaultDegradedFilter
	}

	for i := range c.Accounts {
		a := &c.Accounts[i]
		if strings.TrimSpace(a.Provider) == "" {
			a.Provider = ProviderGmail
		}
		a.Provider = strings.ToLower(strings.TrimSpace(a.Provider))
		if a.Gmail != nil && strings.TrimSpace(a.Gmail.InitialLookback) == "" {
			a.Gmail.InitialLookback = DefaultInitialLookback
		}
	}

	for i := range c.Rules {
		r := &c.Rules[i]
		if len(r.Formats) == 0 {
			r.Formats = []Format{FormatEML}
		}
		for j, f := range r.Formats {
			r.Formats[j] = Format(strings.ToLower(strings.TrimSpace(string(f))))
		}
	}
}

// expandPaths rewrites every path-valued field in place.
func (c *Config) expandPaths() {
	c.StateDir = ExpandPath(c.StateDir)
	c.QuarantineDir = ExpandPath(c.QuarantineDir)

	for i := range c.Accounts {
		g := c.Accounts[i].Gmail
		if g == nil {
			continue
		}
		g.CredentialsFile = ExpandPath(g.CredentialsFile)
		g.TokenFile = ExpandPath(g.TokenFile)
	}

	for i := range c.Rules {
		c.Rules[i].Dest = ExpandPath(c.Rules[i].Dest)
		expandPathsInNode(&c.Rules[i].Match)
	}
}
