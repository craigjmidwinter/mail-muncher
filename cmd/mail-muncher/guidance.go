package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/pipeline"
	"github.com/craigjmidwinter/mail-muncher/internal/provider/gmail"
)

// docsURL is the published documentation site. It is the one URL any guidance
// message is allowed to send someone to, so it lives here rather than being
// retyped.
const docsURL = "https://craigjmidwinter.github.io/mail-muncher/"

// Setup states. Each is a state a user or an installing agent can be in that a
// single named command gets them out of.
//
// The tokens are stable and are what tests assert on, so the prose below can be
// rewritten without rewriting the test table.
const (
	// kindNoConfig: nothing at the resolved config path.
	kindNoConfig = "no-config"
	// kindNoAccounts: a config file that parses but configures no mailbox.
	kindNoAccounts = "no-accounts"
	// kindNotAuthorized: an account whose token file has never been written.
	kindNotAuthorized = "not-authorized"
	// kindTokenRejected: a stored token the provider refused; on Gmail this is
	// usually the 7-day Testing-mode refresh-token expiry.
	kindTokenRejected = "token-rejected"
)

// setupError is a failure the user fixes by running a named command, carrying
// the whole guidance text rather than a one-line cause.
//
// It exists so main can print that text verbatim instead of the "error: ..."
// form every other failure takes. This output is a user interface: a person
// meets it as the first thing mail-muncher ever says to them, and an agent
// installing the plugin reads it off stderr and acts on it without fetching
// documentation. Both need the exact path, the exact command, and the exact
// config keys, which a single-line error has no room for.
type setupError struct {
	// Kind is one of the kind* constants above.
	Kind string
	// Path is the config file the command resolved, always absolute-as-given.
	Path string
	// Text is the whole message, ready to print, with no trailing newline.
	Text string
	// Err is the underlying cause, if there was one.
	Err error
}

func (e *setupError) Error() string { return e.Text }

func (e *setupError) Unwrap() error { return e.Err }

// setupFailure wraps a setupError in the exit status it deserves, so callers
// can return it straight from a RunE.
func setupFailure(code int, se *setupError) error {
	return &pipeline.ExitCodeError{Code: code, Err: se}
}

// reportedError marks an error whose text has already been written to the
// user, so main exits with its status without printing it a second time.
//
// The `mcp` command needs this: an unconfigured server prints its guidance at
// startup, because a client that launched it reads stderr as it goes and would
// otherwise see nothing until the session ended.
type reportedError struct{ err error }

func (e *reportedError) Error() string { return e.err.Error() }

func (e *reportedError) Unwrap() error { return e.err }

// requireConfigFile turns "there is no config file" into the distinct state it
// is, rather than letting it arrive as an open(2) failure from the YAML loader.
//
// Every command that needs config calls this first. It is the whole point of
// the exercise: first run cannot "just work" — mail-muncher ships no OAuth
// client and never will — so the unconfigured state is the first thing most
// people and every installing agent will see, and it has to do real work.
func requireConfigFile(path string) error {
	info, err := os.Stat(path)
	switch {
	case err == nil && info.IsDir():
		return setupFailure(pipeline.ExitConfig, &setupError{
			Kind: kindNoConfig,
			Path: path,
			Text: noConfigGuidance(path),
			Err:  fmt.Errorf("%s is a directory, not a config file", path),
		})
	case errors.Is(err, fs.ErrNotExist):
		return setupFailure(pipeline.ExitConfig, &setupError{
			Kind: kindNoConfig,
			Path: path,
			Text: noConfigGuidance(path),
			Err:  err,
		})
	case err != nil:
		// Unreadable for some other reason — permissions, a broken symlink, a
		// filesystem that went away. There is nothing to teach here beyond the
		// path and the reason, and inventing setup guidance would be wrong: the
		// file may be perfectly good.
		return &pipeline.ExitCodeError{Code: pipeline.ExitConfig, Err: fmt.Errorf("config file %s: %w", path, err)}
	}
	return nil
}

// noConfigGuidance is the first-contact message: what is missing, what to run,
// and what the two provider paths honestly cost.
//
// Neither provider is presented as the good one. IMAP is faster to set up and
// works with far more mailboxes, but an app password is a broader credential
// than a read-only OAuth token. Gmail's scope is enforced by Google, but it
// costs ten minutes of Cloud Console and expires weekly. Those are real
// trade-offs and the reader is the only one who can weigh them.
func noConfigGuidance(path string) string {
	return fmt.Sprintf(`mail-muncher is not configured.
  missing config file: %s
  next command:        mail-muncher init
  then:                mail-muncher validate && mail-muncher run --dry-run

init asks which provider to use. Both are supported; the costs differ.
  provider: imap   ~2 min. Gmail, Fastmail, Proton Bridge, work accounts,
    self-hosted. Needs an app password, which is a broader credential than a
    read-only OAuth token; mail-muncher only ever issues BODY.PEEK.
  provider: gmail  ~10 min in the Google Cloud Console: gmail.readonly is a
    Google restricted scope, so mail-muncher ships no OAuth client and you
    register your own. Google enforces read-only, but on a Testing-mode
    consent screen the refresh token expires every 7 days, so
    "mail-muncher auth" must be re-run weekly. Read docs/gmail-setup.md.
Docs: %s`, path, docsURL)
}

// noAccountsGuidance covers a config file that parses but names no mailbox.
func noAccountsGuidance(path string, cause error) string {
	return fmt.Sprintf(`mail-muncher has a config file but no accounts, so there is nothing to fetch.
  config file:  %s
  problem:      the accounts: list is empty or missing
  next command: mail-muncher init --force --provider imap --account personal
                (or --provider gmail; see docs/gmail-setup.md for what that costs)
Adding the account by hand works too: every key is listed in docs/configuration.md.
  cause: %s`, path, cause)
}

// notAuthorizedGuidance covers a configured Gmail account that has never been
// through the consent flow, so its token file does not exist yet.
func notAuthorizedGuidance(configPath, account, tokenFile string, cause error) string {
	msg := fmt.Sprintf(`mail-muncher account %q has never been authorized.
  config file:  %s
  token file:   %s (does not exist)
  next command: mail-muncher auth --account %s
auth prints a Google consent URL and writes the token once you approve it. On a
consent screen still in Testing mode the refresh token expires every 7 days, so
that command has to be re-run weekly. Setup steps: docs/gmail-setup.md`,
		account, configPath, tokenFile, account)
	if cause != nil {
		msg += fmt.Sprintf("\n  cause: %s", cause)
	}
	return msg
}

// tokenRejectedGuidance covers a stored token the provider refused. The 7-day
// expiry named here is structural — it follows from mail-muncher shipping no
// OAuth client — so the message says how to live with it rather than implying
// a fix exists.
func tokenRejectedGuidance(configPath, account string, cause error) string {
	return fmt.Sprintf(`mail-muncher account %q has a stored token the provider rejected.
  config file:  %s
  next command: mail-muncher auth --account %s
The usual cause is the 7-day refresh-token expiry Google applies to every
consent screen still in Testing mode; revoking mail-muncher's access from your
Google Account does the same thing. Re-running auth restores it for another 7
days. There is no setting that removes the expiry: see docs/gmail-setup.md.
  cause: %s`, account, configPath, account, cause)
}

// noRulesGuidance covers a config that would fetch mail and then throw all of
// it away. It is advice rather than a failure — the config is legal — but it is
// worth a block of text, because the symptom is an empty directory and no error
// at all.
func noRulesGuidance(configPath string) string {
	return fmt.Sprintf(`mail-muncher has no rules, so nothing will ever be stored.
  config file: %s
  problem:     the rules: list is empty; every fetched message is discarded
  next step:   add a rule under rules:, then run mail-muncher validate
A rule that matches everything from the last three days, as a smoke test:
  rules:
    - name: recent
      match:
        newer_than: 72h
      dest: ~/Mail/mail-muncher
      formats: [eml, markdown]
The whole match language is documented in docs/filters.md.`, configPath)
}

// configFailure grades an error from loading or validating the config, turning
// the states that have a named next command into guidance and leaving
// everything else — a YAML syntax error, an unknown key, a match tree that will
// not compile — as the precise diagnostic it already is.
//
// cfg may be nil, which is what a file that would not parse looks like.
func configFailure(path string, cfg *config.Config, err error) error {
	if cfg != nil && len(cfg.Accounts) == 0 {
		return setupFailure(pipeline.ExitConfig, &setupError{
			Kind: kindNoAccounts,
			Path: path,
			Text: noAccountsGuidance(path, err),
			Err:  err,
		})
	}
	return &pipeline.ExitCodeError{Code: pipeline.ExitConfig, Err: err}
}

// cycleFailure grades the error a cycle ended with, attaching setup guidance to
// the two auth states a user can act on.
//
// The exit status is unchanged: pipeline.ExitCode still decides it, so a token
// problem is still a provider failure (2) and cron keeps branching the way it
// always did. Only the message grows.
func cycleFailure(cfg *config.Config, err error) error {
	if err == nil {
		return nil
	}
	code := pipeline.ExitCode(err)
	path, account := configPathOf(cfg), accountOf(err)

	switch {
	case errors.Is(err, gmail.ErrTokenRejected):
		return setupFailure(code, &setupError{
			Kind: kindTokenRejected,
			Path: path,
			Text: tokenRejectedGuidance(path, account, err),
			Err:  err,
		})
	case errors.Is(err, gmail.ErrNoToken):
		return setupFailure(code, &setupError{
			Kind: kindNotAuthorized,
			Path: path,
			Text: notAuthorizedGuidance(path, account, tokenFileOf(cfg, account), err),
			Err:  err,
		})
	}
	return pipeline.Exit(err)
}

// accountOf recovers the account name a provider failure happened under, so
// the guidance can name the exact `auth --account` invocation rather than
// telling a two-account user to work it out.
func accountOf(err error) string {
	var pe *pipeline.ProviderError
	if errors.As(err, &pe) && pe.Account != "" {
		return pe.Account
	}
	return "<name>"
}

// tokenFileOf returns the named account's configured token path, or a
// placeholder when it cannot be resolved.
func tokenFileOf(cfg *config.Config, account string) string {
	if cfg == nil {
		return "(not configured)"
	}
	if a := cfg.Account(account); a != nil && a.Gmail != nil && a.Gmail.TokenFile != "" {
		return a.Gmail.TokenFile
	}
	return "(not configured)"
}

// configPathOf is the config's path, tolerating a nil Config so callers on an
// error path do not have to.
func configPathOf(cfg *config.Config) string {
	if cfg == nil {
		return "(no config loaded)"
	}
	return cfg.Path
}

// reportSetupAdvice writes guidance for the partially-configured states that
// are legal but almost certainly not what the user meant: a config that would
// discard every message, and an account that has never been authorized.
//
// It writes to w (stderr for every caller) and never fails the command. The run
// that follows is allowed to proceed, because both states are recoverable and
// because a multi-account config where one account is unauthorized should still
// fetch the others.
func reportSetupAdvice(w io.Writer, cfg *config.Config) {
	if cfg == nil {
		return
	}
	if len(cfg.Rules) == 0 {
		// Best-effort: this is advisory output on stderr, and a failed write here
		// (a closed pipe, say) is not a reason to fail the command.
		_, _ = fmt.Fprintln(w, noRulesGuidance(cfg.Path))
	}
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		if a.Provider != config.ProviderGmail || a.Gmail == nil {
			continue
		}
		if strings.TrimSpace(a.Gmail.TokenFile) == "" || fileExists(a.Gmail.TokenFile) {
			continue
		}
		// Only once the OAuth client is actually in place. Before that the next
		// step is the Google Cloud Console rather than `auth`, and the provider's
		// own error says so precisely; naming a command that cannot yet work
		// would send the reader down the wrong path.
		if !fileExists(a.Gmail.CredentialsFile) {
			continue
		}
		// Best-effort, same as the no-rules case above.
		_, _ = fmt.Fprintln(w, notAuthorizedGuidance(cfg.Path, a.Name, a.Gmail.TokenFile, nil))
	}
}

// fileExists reports whether path names an existing, non-directory file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
