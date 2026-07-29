package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// write drops content into a temp file and returns its path.
func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// minimalConfig is a valid config with every optional key omitted.
const minimalConfig = `
accounts:
  - name: personal
    provider: gmail
    gmail:
      credentials_file: /tmp/creds.json
      token_file: /tmp/token.json
rules:
  - name: job-search
    match:
      any:
        - from_domains: [example.com]
    dest: /tmp/mail/job-search
`

func TestLoadHappyPath(t *testing.T) {
	path := write(t, `
state_dir: /var/lib/mail-muncher

accounts:
  - name: personal
    provider: gmail
    gmail:
      credentials_file: /etc/mm/credentials.json
      token_file: /etc/mm/token.json
      query: "-in:chats"
      initial_lookback: 168h
  - name: work
    gmail:
      credentials_file: /etc/mm/work-credentials.json
      token_file: /etc/mm/work-token.json

rules:
  - name: job-search
    account: personal
    match:
      any:
        - from_domains_file: /srv/jobsearch/domains.txt
    dest: /srv/mail/job-search
    formats: [eml, markdown]
  - name: receipts
    match:
      all:
        - has_attachment: true
    dest: /srv/mail/receipts
`)

	cfg, err := Load(path)
	require.NoError(t, err)

	require.Equal(t, path, cfg.Path)
	require.Equal(t, "/var/lib/mail-muncher", cfg.StateDir)
	require.Len(t, cfg.Accounts, 2)
	require.Len(t, cfg.Rules, 2)

	personal := cfg.Account("personal")
	require.NotNil(t, personal)
	require.Equal(t, ProviderGmail, personal.Provider)
	require.Equal(t, "/etc/mm/credentials.json", personal.Gmail.CredentialsFile)
	require.Equal(t, "/etc/mm/token.json", personal.Gmail.TokenFile)
	require.Equal(t, "-in:chats", personal.Gmail.Query)
	require.Equal(t, "168h", personal.Gmail.InitialLookback)
	require.Equal(t, 168*time.Hour, personal.Gmail.InitialLookbackDuration())

	// Omitted keys get their documented defaults.
	work := cfg.Account("work")
	require.NotNil(t, work)
	require.Equal(t, ProviderGmail, work.Provider, "provider defaults to gmail")
	require.Equal(t, DefaultInitialLookback, work.Gmail.InitialLookback)
	require.Equal(t, 720*time.Hour, work.Gmail.InitialLookbackDuration())

	require.Nil(t, cfg.Account("nope"))

	require.Equal(t, []Format{FormatEML, FormatMarkdown}, cfg.Rules[0].Formats)
	require.Equal(t, []Format{FormatEML}, cfg.Rules[1].Formats, "formats defaults to [eml]")
	require.True(t, cfg.Rules[0].HasFormat(FormatMarkdown))
	require.False(t, cfg.Rules[1].HasFormat(FormatMarkdown))

	// Account scoping: an unbound rule applies everywhere.
	require.True(t, cfg.Rules[0].AppliesTo("personal"))
	require.False(t, cfg.Rules[0].AppliesTo("work"))
	require.True(t, cfg.Rules[1].AppliesTo("work"))

	// The match tree survives as an opaque node.
	require.Equal(t, yaml.MappingNode, cfg.Rules[0].Match.Kind)

	// Referenced files are missing, so validation warns but does not error.
	ps := Validate(cfg)
	require.False(t, ps.HasErrors(), "unexpected errors: %v", ps.Errors())
	require.NoError(t, ps.Err())
	require.NotEmpty(t, ps.Warnings())
}

func TestLoadDefaultsStateDir(t *testing.T) {
	t.Setenv("HOME", "/home/tester")

	cfg, err := Load(write(t, minimalConfig))
	require.NoError(t, err)
	require.Equal(t, "/home/tester/.local/state/mail-muncher", cfg.StateDir)
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	cases := map[string]string{
		"top level": `
totally_bogus: yes
accounts:
  - name: personal
    gmail: {credentials_file: /a, token_file: /b}
`,
		"account": `
accounts:
  - name: personal
    nickname: pers
    gmail: {credentials_file: /a, token_file: /b}
`,
		"gmail": `
accounts:
  - name: personal
    gmail: {credentials_file: /a, token_file: /b, oops: 1}
`,
		"rule": `
accounts:
  - name: personal
    gmail: {credentials_file: /a, token_file: /b}
rules:
  - name: r
    dest: /d
    destination: /d
    match: {has_attachment: true}
`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, body))
			require.Error(t, err)
			require.Contains(t, err.Error(), "not found in type")
		})
	}
}

func TestLoadUnknownKeysInsideMatchAreNotRejectedHere(t *testing.T) {
	// The match tree is opaque until the filter engine lands; anything
	// inside it must survive Load untouched.
	cfg, err := Load(write(t, `
accounts:
  - name: personal
    gmail: {credentials_file: /a, token_file: /b}
rules:
  - name: r
    dest: /d
    match:
      not:
        subject_regex: "^ping"
`))
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, cfg.Rules[0].Match.Decode(&decoded))
	require.Contains(t, decoded, "not")
}

func TestLoadErrors(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		_, err := Load(filepath.Join(t.TempDir(), "nope.yml"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "open config")
	})

	t.Run("empty file", func(t *testing.T) {
		_, err := Load(write(t, ""))
		require.ErrorContains(t, err, "config file is empty")
	})

	t.Run("malformed yaml", func(t *testing.T) {
		_, err := Load(write(t, "accounts: [oops\n"))
		require.Error(t, err)
	})

	t.Run("multiple documents", func(t *testing.T) {
		_, err := Load(write(t, minimalConfig+"\n---\nstate_dir: /other\n"))
		require.ErrorContains(t, err, "exactly one YAML document")
	})
}

func TestExpandPath(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	t.Setenv("MM_ROOT", "/srv/mm")

	cases := []struct{ in, want string }{
		{"", ""},
		{"~", "/home/tester"},
		{"~/Mail/job-search", "/home/tester/Mail/job-search"},
		{"$MM_ROOT/state", "/srv/mm/state"},
		{"${MM_ROOT}/state", "/srv/mm/state"},
		{"$HOME/Mail", "/home/tester/Mail"},
		{"/absolute/path", "/absolute/path"},
		{"~other/Mail", "~other/Mail"}, // ~user is not supported
		{"relative/path", "relative/path"},
		{"$MM_UNSET/x", "/x"}, // shell semantics: undefined expands empty
	}

	for _, tc := range cases {
		require.Equalf(t, tc.want, ExpandPath(tc.in), "ExpandPath(%q)", tc.in)
	}
}

func TestLoadExpandsTildeAndEnvInEveryPathField(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	t.Setenv("MM_DOMAINS", "/srv/jobsearch")

	cfg, err := Load(write(t, `
state_dir: ~/state
accounts:
  - name: personal
    gmail:
      credentials_file: ~/.config/mail-muncher/credentials.json
      token_file: $HOME/token.json
      query: "from:$notavariable"
rules:
  - name: job-search
    match:
      any:
        - from_domains_file: ~/domains.txt
        - all:
            - from_domains_file: $MM_DOMAINS/more.txt
            - subject_regex: "~not-a-path"
    dest: ~/Mail/job-search
`))
	require.NoError(t, err)

	require.Equal(t, "/home/tester/state", cfg.StateDir)
	require.Equal(t, "/home/tester/.config/mail-muncher/credentials.json", cfg.Accounts[0].Gmail.CredentialsFile)
	require.Equal(t, "/home/tester/token.json", cfg.Accounts[0].Gmail.TokenFile)
	require.Equal(t, "/home/tester/Mail/job-search", cfg.Rules[0].Dest)

	// Non-path fields are left completely alone.
	require.Equal(t, "from:$notavariable", cfg.Accounts[0].Gmail.Query)

	refs := collectFilePathRefs(&cfg.Rules[0].Match)
	require.Len(t, refs, 2)
	require.Equal(t, "any[0].from_domains_file", refs[0].Path)
	require.Equal(t, "/home/tester/domains.txt", refs[0].Value)
	require.Equal(t, "any[1].all[0].from_domains_file", refs[1].Path)
	require.Equal(t, "/srv/jobsearch/more.txt", refs[1].Value)

	// A regex that happens to start with ~ is not a path and is untouched.
	var tree map[string]any
	require.NoError(t, cfg.Rules[0].Match.Decode(&tree))
	nested := tree["any"].([]any)[1].(map[string]any)["all"].([]any)[1].(map[string]any)
	require.Equal(t, "~not-a-path", nested["subject_regex"])
}

// hasProblem reports whether any problem of the given severity mentions field.
func hasProblem(ps Problems, sev Severity, field string) bool {
	for _, p := range ps {
		if p.Severity == sev && p.Field == field {
			return true
		}
	}
	return false
}

func loadForValidation(t *testing.T, body string) *Config {
	t.Helper()
	cfg, err := Load(write(t, body))
	require.NoError(t, err)
	return cfg
}

func TestValidateDuplicateRuleName(t *testing.T) {
	cfg := loadForValidation(t, `
accounts:
  - name: personal
    gmail: {credentials_file: /a, token_file: /b}
rules:
  - name: dupe
    match: {has_attachment: true}
    dest: /one
  - name: dupe
    match: {has_attachment: false}
    dest: /two
`)

	ps := Validate(cfg)
	require.True(t, ps.HasErrors())
	require.True(t, hasProblem(ps, SeverityError, "rules[1].name"))
	require.ErrorContains(t, ps.Err(), `duplicate rule name "dupe"`)
}

func TestValidateDuplicateAccountName(t *testing.T) {
	cfg := loadForValidation(t, `
accounts:
  - name: personal
    gmail: {credentials_file: /a, token_file: /b}
  - name: personal
    gmail: {credentials_file: /c, token_file: /d}
rules:
  - name: r
    match: {has_attachment: true}
    dest: /one
`)

	ps := Validate(cfg)
	require.True(t, hasProblem(ps, SeverityError, "accounts[1].name"))
	require.ErrorContains(t, ps.Err(), `duplicate account name "personal"`)
}

func TestValidateBadFormat(t *testing.T) {
	cfg := loadForValidation(t, `
accounts:
  - name: personal
    gmail: {credentials_file: /a, token_file: /b}
rules:
  - name: r
    match: {has_attachment: true}
    dest: /one
    formats: [eml, pdf]
  - name: dupes
    match: {has_attachment: true}
    dest: /two
    formats: [eml, EML]
`)

	ps := Validate(cfg)
	require.True(t, ps.HasErrors())
	require.True(t, hasProblem(ps, SeverityError, "rules[0].formats[1]"))
	require.ErrorContains(t, ps.Err(), `unknown format "pdf"`)

	// Case is normalized, so eml/EML is a duplicate — a warning, not an error.
	require.True(t, hasProblem(ps, SeverityWarning, "rules[1].formats[1]"))
}

func TestValidateUnknownAccountReference(t *testing.T) {
	cfg := loadForValidation(t, `
accounts:
  - name: personal
    gmail: {credentials_file: /a, token_file: /b}
rules:
  - name: r
    account: nonexistent
    match: {has_attachment: true}
    dest: /one
`)

	ps := Validate(cfg)
	require.True(t, hasProblem(ps, SeverityError, "rules[0].account"))
	require.ErrorContains(t, ps.Err(), `unknown account "nonexistent"`)
}

func TestValidateRequiredFields(t *testing.T) {
	cfg := loadForValidation(t, `
accounts:
  - name: ""
    gmail: {credentials_file: "", token_file: ""}
  - name: bad-provider
    provider: imap
  - name: no-gmail-block
    provider: gmail
rules:
  - name: ""
    dest: ""
`)

	ps := Validate(cfg)
	require.True(t, ps.HasErrors())
	for _, field := range []string{
		"accounts[0].name",
		"accounts[0].gmail.credentials_file",
		"accounts[0].gmail.token_file",
		"accounts[1].provider",
		"accounts[2].gmail",
		"rules[0].name",
		"rules[0].dest",
		"rules[0].match",
	} {
		require.Truef(t, hasProblem(ps, SeverityError, field), "expected an error for %s, got %v", field, ps)
	}
}

func TestValidateNoAccountsIsAnError(t *testing.T) {
	cfg := loadForValidation(t, "rules: []\n")
	ps := Validate(cfg)
	require.True(t, hasProblem(ps, SeverityError, "accounts"))
	// No rules is only a warning: the config is usable, just inert.
	require.True(t, hasProblem(ps, SeverityWarning, "rules"))
}

func TestValidateInitialLookback(t *testing.T) {
	cfg := loadForValidation(t, `
accounts:
  - name: bad
    gmail: {credentials_file: /a, token_file: /b, initial_lookback: "thirty days"}
  - name: negative
    gmail: {credentials_file: /a, token_file: /b, initial_lookback: "-1h"}
rules:
  - name: r
    match: {has_attachment: true}
    dest: /one
`)

	ps := Validate(cfg)
	require.True(t, hasProblem(ps, SeverityError, "accounts[0].gmail.initial_lookback"))
	require.True(t, hasProblem(ps, SeverityError, "accounts[1].gmail.initial_lookback"))

	// The accessor still yields the default rather than a zero duration.
	require.Equal(t, 720*time.Hour, cfg.Accounts[0].Gmail.InitialLookbackDuration())
}

func TestValidateMissingDomainsFileIsAWarningNotAnError(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present.txt")
	require.NoError(t, os.WriteFile(present, []byte("example.com\n"), 0o600))
	absent := filepath.Join(dir, "absent.txt")

	creds := filepath.Join(dir, "credentials.json")
	require.NoError(t, os.WriteFile(creds, []byte("{}"), 0o600))
	token := filepath.Join(dir, "token.json")
	require.NoError(t, os.WriteFile(token, []byte("{}"), 0o600))

	cfg := loadForValidation(t, `
accounts:
  - name: personal
    gmail:
      credentials_file: `+creds+`
      token_file: `+token+`
rules:
  - name: present
    match: {from_domains_file: `+present+`}
    dest: /one
  - name: absent
    match: {from_domains_file: `+absent+`}
    dest: /two
`)

	ps := Validate(cfg)
	require.False(t, ps.HasErrors(), "unexpected errors: %v", ps.Errors())
	require.NoError(t, ps.Err(), "a missing domains file must not fail validation")

	require.Len(t, ps.Warnings(), 1)
	require.True(t, hasProblem(ps, SeverityWarning, "rules[1].match.from_domains_file"))
	require.Contains(t, ps.Warnings()[0].Message, absent)
}

func TestValidateMissingCredentialAndTokenFilesAreWarnings(t *testing.T) {
	cfg := loadForValidation(t, minimalConfig)
	ps := Validate(cfg)
	require.False(t, ps.HasErrors(), "unexpected errors: %v", ps.Errors())
	require.True(t, hasProblem(ps, SeverityWarning, "accounts[0].gmail.credentials_file"))
	require.True(t, hasProblem(ps, SeverityWarning, "accounts[0].gmail.token_file"))
}

func TestValidateMatchValidatorHook(t *testing.T) {
	cfg := loadForValidation(t, minimalConfig)

	require.Nil(t, MatchValidator, "the hook must default to nil")
	t.Cleanup(func() { MatchValidator = nil })

	var seen []string
	MatchValidator = func(r *Rule) error {
		seen = append(seen, r.Name)
		return errNotCompilable
	}

	ps := Validate(cfg)
	require.Equal(t, []string{"job-search"}, seen)
	require.True(t, hasProblem(ps, SeverityError, "rules[0].match"))
	require.ErrorContains(t, ps.Err(), "cannot compile")
}

var errNotCompilable = errTest("cannot compile")

type errTest string

func (e errTest) Error() string { return string(e) }

func TestValidateNilConfig(t *testing.T) {
	ps := Validate(nil)
	require.True(t, ps.HasErrors())
	require.Error(t, ps.Err())
}

func TestProblemFormatting(t *testing.T) {
	require.Equal(t, "error: rules[0].dest: must not be empty",
		Problem{Severity: SeverityError, Field: "rules[0].dest", Message: "must not be empty"}.String())
	require.Equal(t, "warning: something",
		Problem{Severity: SeverityWarning, Message: "something"}.String())

	var empty Problems
	require.NoError(t, empty.Err())
	require.False(t, empty.HasErrors())
}

func TestLoadAndValidateTestdataConfig(t *testing.T) {
	// The checked-in example must parse, and must only ever produce
	// warnings — the `validate` end-to-end check depends on it exiting 0.
	t.Setenv("HOME", t.TempDir())

	cfg, ps, err := LoadAndValidate(filepath.Join("testdata", "config.yml"))
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.False(t, ps.HasErrors(), "testdata/config.yml must be error-free: %v", ps.Errors())
	require.NotEmpty(t, ps.Warnings(), "testdata/config.yml is expected to warn about files it does not ship")

	require.Len(t, cfg.Accounts, 1)
	require.Len(t, cfg.Rules, 2)
	require.Equal(t, "job-search", cfg.Rules[0].Name)
	require.Equal(t, 720*time.Hour, cfg.Accounts[0].Gmail.InitialLookbackDuration())
}

func TestLoadAndValidateReturnsErrorForInvalidConfig(t *testing.T) {
	_, ps, err := LoadAndValidate(write(t, "rules: []\n"))
	require.Error(t, err)
	require.True(t, ps.HasErrors())

	_, _, err = LoadAndValidate(filepath.Join(t.TempDir(), "missing.yml"))
	require.Error(t, err)
}

// TestLoadDefaultsCyclePolicies pins the two failure policies to the safe
// choice. Both defaults exist to make sure a surprise — a message that will not
// parse, a domain file that is not there yet — costs duplicate work rather than
// mail.
func TestLoadDefaultsCyclePolicies(t *testing.T) {
	cfg, err := Load(write(t, minimalConfig))
	require.NoError(t, err)

	require.Equal(t, MessageFailureQuarantine, cfg.OnMessageFailure)
	require.Equal(t, DegradedFilterHold, cfg.OnDegradedFilter)
	require.Equal(t, MessageFailureQuarantine, cfg.MessageFailure())
	require.Equal(t, DegradedFilterHold, cfg.DegradedFilter())

	require.Equal(t, filepath.Join(cfg.StateDir, "quarantine"), cfg.QuarantineDir)
	require.Equal(t, cfg.QuarantineDir, cfg.QuarantineRoot())

	require.False(t, Validate(cfg).HasErrors())
}

// TestLoadCyclePoliciesFromFile: the keys are top-level, beside state_dir, and
// are normalized the way every other enum in this file is.
func TestLoadCyclePoliciesFromFile(t *testing.T) {
	t.Setenv("HOME", "/home/tester")

	cfg, err := Load(write(t, `
state_dir: /var/lib/mail-muncher
quarantine_dir: ~/parked
on_message_failure: " Abort "
on_degraded_filter: FAIL
`+minimalConfig))
	require.NoError(t, err)

	require.Equal(t, MessageFailureAbort, cfg.MessageFailure())
	require.Equal(t, DegradedFilterFail, cfg.DegradedFilter())
	require.Equal(t, "/home/tester/parked", cfg.QuarantineRoot(), "quarantine_dir is path-expanded")
}

// TestValidateRejectsUnknownPolicies: a typo must never silently run a policy
// the user did not ask for, since the whole point of the keys is choosing what
// happens to mail.
func TestValidateRejectsUnknownPolicies(t *testing.T) {
	cfg := loadForValidation(t, "on_message_failure: retry\n"+minimalConfig)
	ps := Validate(cfg)
	require.True(t, ps.HasErrors())
	require.True(t, hasProblem(ps, SeverityError, "on_message_failure"))
	require.ErrorContains(t, ps.Err(), "retry")
	require.ErrorContains(t, ps.Err(), "quarantine")

	cfg = loadForValidation(t, "on_degraded_filter: carry_on\n"+minimalConfig)
	ps = Validate(cfg)
	require.True(t, ps.HasErrors())
	require.True(t, hasProblem(ps, SeverityError, "on_degraded_filter"))
	require.ErrorContains(t, ps.Err(), "hold")
}

// TestValidateWarnsAboutProceed: the one policy that accepts losing mail says
// so out loud.
func TestValidateWarnsAboutProceed(t *testing.T) {
	cfg := loadForValidation(t, "on_degraded_filter: proceed\n"+minimalConfig)
	ps := Validate(cfg)
	require.False(t, ps.HasErrors(), "proceed is legal, just dangerous: %v", ps.Errors())
	require.True(t, hasProblem(ps, SeverityWarning, "on_degraded_filter"))
}

// TestPolicyAccessorsDefaultForConfigsBuiltInCode: nothing may depend on Load
// having run, or the MCP server and the tests would behave differently from
// the CLI.
func TestPolicyAccessorsDefaultForConfigsBuiltInCode(t *testing.T) {
	cfg := &Config{StateDir: "/state"}
	require.Equal(t, DefaultMessageFailure, cfg.MessageFailure())
	require.Equal(t, DefaultDegradedFilter, cfg.DegradedFilter())
	require.Equal(t, filepath.Join("/state", "quarantine"), cfg.QuarantineRoot())

	var nilCfg *Config
	require.Equal(t, DefaultMessageFailure, nilCfg.MessageFailure())
	require.Equal(t, DefaultDegradedFilter, nilCfg.DegradedFilter())
	require.Empty(t, nilCfg.QuarantineRoot())
}
