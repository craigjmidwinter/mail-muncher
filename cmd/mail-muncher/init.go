package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/pipeline"
)

// Defaults `init` fills in for everything it does not ask about. Only three
// answers cannot be defaulted — the provider, the account name, and where mail
// should land — so those are the only three questions.
const (
	// defaultInitAccount names the first account. It is only an identifier:
	// rules refer to it, and nothing else does.
	defaultInitAccount = "personal"
	// defaultInitDest is where the starter rule writes. It is expanded like
	// any other path field, so the tilde survives into the file.
	defaultInitDest = "~/Mail/mail-muncher"
	// starterRuleAge is the starter rule's window. Three days is chosen so the
	// first run is guaranteed to match something in any live mailbox: a first
	// run that stores nothing is indistinguishable from a broken install.
	starterRuleAge = "72h"
)

// initProviders are the providers `init` can write a config for, in the order
// a newcomer should consider them.
var initProviders = []string{"imap", "gmail"}

// Secret-manager commands `init` can seed `imap.password_cmd` with. Each one
// prints the secret on stdout and nothing else, which is the contract
// password_cmd has to satisfy.
const (
	// keychainPasswordCmd reads from the macOS login keychain, which is present
	// on every Mac with no install step.
	keychainPasswordCmd = "security find-generic-password -s mail-muncher -w"
	// secretToolPasswordCmd reads from a libsecret keyring (GNOME Keyring,
	// KWallet via the libsecret backend) — the closest thing Linux has to a
	// keychain that is already there.
	secretToolPasswordCmd = "secret-tool lookup service mail-muncher"
	// passPasswordCmd is the portable fallback named in the generated comment:
	// `pass` runs anywhere, including where no keyring daemon does.
	passPasswordCmd = "pass show mail/mail-muncher"
)

// defaultPasswordCmd is the `imap.password_cmd` init seeds the generated config
// with, for the platform it is running on.
//
// It is a pure function of goos rather than a probe of the filesystem: the
// generated line is a starting point the user edits anyway, and guessing wrong
// from a `which` is no better than guessing wrong from GOOS. What matters is
// that the default is not a command that does not exist on the platform — on
// Linux the macOS `security` binary fails with a shell-shaped error that says
// nothing about mail, which is a miserable first experience.
//
// Every branch is listed in the generated comment regardless, so the file
// documents the alternatives whichever platform wrote it.
func defaultPasswordCmd(goos string) string {
	switch goos {
	case "darwin":
		return keychainPasswordCmd
	case "linux", "freebsd", "openbsd", "netbsd", "dragonfly":
		return secretToolPasswordCmd
	default:
		// Windows, plan9, and anything else: `pass` is the one option here that
		// does not assume a platform-specific secret service.
		return passPasswordCmd
	}
}

// newInitCommand builds the `init` subcommand: write a valid config and say
// what to run next.
//
// It is interactive by default and fully non-interactive with flags, because
// an agent installing the plugin will run this and cannot answer a prompt.
func newInitCommand() *cobra.Command {
	opts := &initOptions{}

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starter config and print the next command to run",
		Long: "Write a valid config file and print exactly what to run next.\n\n" +
			"Interactive by default; fully non-interactive when --provider, --account\n" +
			"and --dest are given (or with --yes, which takes the default for every\n" +
			"answer except --provider).\n\n" +
			"An existing config is never overwritten: init prints its path and exits 1.\n" +
			"Only --force overwrites.\n\n" +
			"The generated config carries one starter rule matching everything newer\n" +
			"than " + starterRuleAge + ", so the first run is guaranteed to store something. Narrow\n" +
			"it once you have seen mail land; see docs/filters.md.\n\n" +
			"Exit status: 0 written, 1 a config already exists or the answers do not\n" +
			"produce a valid config.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Nothing below is a usage problem, so never answer with usage.
			cmd.SilenceUsage = true

			path, err := cmd.Flags().GetString("config")
			if err != nil {
				return err
			}
			opts.configPath = path
			return runInit(cmd, opts)
		},
	}

	cmd.Flags().StringVar(&opts.provider, "provider", "", "provider to configure: "+strings.Join(initProviders, " or "))
	cmd.Flags().StringVar(&opts.account, "account", "", "name for the account (default "+defaultInitAccount+")")
	cmd.Flags().StringVar(&opts.dest, "dest", "", "directory matched mail is written to (default "+defaultInitDest+")")
	cmd.Flags().BoolVar(&opts.assumeYes, "yes", false, "never prompt; take the default for every answer except --provider")
	cmd.Flags().BoolVar(&opts.force, "force", false, "overwrite an existing config file")

	return cmd
}

// initOptions are the answers `init` needs, whether they arrived as flags or
// as replies to a prompt.
type initOptions struct {
	configPath string
	provider   string
	account    string
	dest       string
	assumeYes  bool
	force      bool
}

// runInit is the whole command: refuse to clobber, collect the answers, render
// the config, prove it validates, write it, and say what to run next.
func runInit(cmd *cobra.Command, opts *initOptions) error {
	out := cmd.OutOrStdout()

	// Checked before anything is asked, so an interactive run does not walk
	// someone through five questions only to refuse at the end.
	if err := refuseExistingConfig(opts); err != nil {
		return err
	}

	if err := opts.resolve(cmd.InOrStdin(), out); err != nil {
		return err
	}

	body, err := renderConfig(opts)
	if err != nil {
		return err
	}

	// The generated file must pass `mail-muncher validate` — so prove it here,
	// against the real loader and the real validator, rather than trusting a
	// template. A config that init wrote and validate rejects would be the
	// worst possible first impression.
	if err := validateGenerated(body, opts.provider); err != nil {
		return err
	}

	if err := writeConfigFile(opts.configPath, body, opts.force); err != nil {
		return &pipeline.ExitCodeError{Code: pipeline.ExitConfig, Err: err}
	}

	fmt.Fprintf(out, "Wrote %s\n\n", opts.configPath)
	fmt.Fprintln(out, nextSteps(opts))
	return nil
}

// refuseExistingConfig is the one hard rule: a config file that already exists
// is never overwritten without --force. Somebody's rules, destinations and
// credential paths live in that file.
func refuseExistingConfig(opts *initOptions) error {
	if opts.force {
		return nil
	}
	if _, err := os.Stat(opts.configPath); err != nil {
		return nil // missing, or unreadable — writeConfigFile reports the latter
	}
	return &pipeline.ExitCodeError{
		Code: pipeline.ExitConfig,
		Err: fmt.Errorf(`a config file already exists; nothing was written
  config file:  %s
  next command: mail-muncher validate
Pass --force to overwrite it, or --config <path> to write somewhere else`, opts.configPath),
	}
}

// resolve fills in every answer, from flags where they were given and from the
// prompt otherwise.
//
// --yes suppresses the prompt entirely and takes the documented default —
// except for the provider, which has no honest default: the two paths cost
// different things, and picking one on the user's behalf would be exactly the
// editorializing this command exists to avoid.
func (o *initOptions) resolve(in io.Reader, out io.Writer) error {
	r := bufio.NewReader(in)

	if o.provider == "" && o.assumeYes {
		return &pipeline.ExitCodeError{
			Code: pipeline.ExitConfig,
			Err: fmt.Errorf("--provider is required with --yes (one of: %s); the two paths cost "+
				"different things, so there is no default to take. Run `mail-muncher init` without "+
				"--yes to see the trade-off", strings.Join(initProviders, ", ")),
		}
	}
	if o.provider == "" {
		fmt.Fprintln(out, providerChoiceText())
		answer, err := prompt(r, out, "Provider ("+strings.Join(initProviders, "/")+")", "")
		if err != nil {
			return err
		}
		o.provider = answer
	}
	o.provider = strings.ToLower(strings.TrimSpace(o.provider))
	if !knownProvider(o.provider) {
		return &pipeline.ExitCodeError{
			Code: pipeline.ExitConfig,
			Err:  fmt.Errorf("unknown provider %q (want one of: %s)", o.provider, strings.Join(initProviders, ", ")),
		}
	}

	// Said before any further questions, not after the file is written: ten
	// minutes of Google Cloud Console is a real cost, it is structural — see
	// the no-bundled-oauth-client decision — and surprising someone with it
	// after they have invested is the avoidable part.
	if o.provider == "gmail" {
		fmt.Fprintln(out, gmailCostWarning())
	}

	if o.account == "" && !o.assumeYes {
		answer, err := prompt(r, out, "Account name", defaultInitAccount)
		if err != nil {
			return err
		}
		o.account = answer
	}
	if strings.TrimSpace(o.account) == "" {
		o.account = defaultInitAccount
	}

	if o.dest == "" && !o.assumeYes {
		answer, err := prompt(r, out, "Write matched mail to", defaultInitDest)
		if err != nil {
			return err
		}
		o.dest = answer
	}
	if strings.TrimSpace(o.dest) == "" {
		o.dest = defaultInitDest
	}

	o.account = strings.TrimSpace(o.account)
	o.dest = strings.TrimSpace(o.dest)
	return nil
}

// prompt asks one question and returns the answer, or def when the answer is
// blank. A closed stdin is not an error: it means nobody is there to answer, so
// the default stands.
func prompt(r *bufio.Reader, out io.Writer, question, def string) (string, error) {
	if def == "" {
		fmt.Fprintf(out, "%s: ", question)
	} else {
		fmt.Fprintf(out, "%s [%s]: ", question, def)
	}
	line, err := r.ReadString('\n')
	answer := strings.TrimSpace(line)
	if err != nil && answer == "" {
		if def == "" {
			return "", &pipeline.ExitCodeError{
				Code: pipeline.ExitConfig,
				Err: fmt.Errorf("%s: no answer and no default; pass --provider, --account and --dest "+
					"to run non-interactively", question),
			}
		}
		fmt.Fprintln(out)
		return def, nil
	}
	if answer == "" {
		return def, nil
	}
	return answer, nil
}

func knownProvider(name string) bool {
	for _, p := range initProviders {
		if p == name {
			return true
		}
	}
	return false
}

// providerChoiceText states the trade before the question, in the same terms
// the unconfigured guidance uses. Neither option is recommended over the other.
func providerChoiceText() string {
	return `Which provider should mail-muncher read from? Both are supported; the costs differ.

  imap   ~2 min. Gmail, Fastmail, Proton Bridge, work accounts, self-hosted.
         Needs an app password, which is a broader credential than a read-only
         OAuth token; mail-muncher only ever issues BODY.PEEK.
  gmail  ~10 min in the Google Cloud Console. Google enforces the read-only
         scope, but the token expires every 7 days on a Testing-mode consent
         screen.
`
}

// gmailCostWarning is printed the moment gmail is chosen and before anything
// else is asked.
func gmailCostWarning() string {
	return `
Before you go further: the gmail provider needs about 10 minutes in the Google
Cloud Console before it can fetch a single message. mail-muncher ships no OAuth
client and never will, because gmail.readonly is a Google restricted scope, so
you register your own Google Cloud project and Desktop-app client and download
its JSON. The step-by-step is docs/gmail-setup.md, and it is worth opening now.
The imap path needs an app password instead, and takes about two minutes.
`
}

// renderConfig produces the config file body for the chosen provider.
func renderConfig(opts *initOptions) (string, error) {
	dir := filepath.Dir(opts.configPath)

	switch opts.provider {
	case "gmail":
		return fmt.Sprintf(gmailConfigTemplate,
			opts.account,
			filepath.Join(dir, "credentials.json"),
			filepath.Join(dir, "token.json"),
			opts.dest,
			starterRuleAge,
			docsURL,
		), nil
	case "imap":
		return fmt.Sprintf(imapConfigTemplate,
			opts.account,
			opts.dest,
			starterRuleAge,
			docsURL,
			defaultPasswordCmd(runtime.GOOS),
		), nil
	}
	return "", fmt.Errorf("unknown provider %q", opts.provider)
}

// validateGenerated runs the real loader and the real validator over the body
// about to be written, so "the generated config passes `mail-muncher validate`"
// is true by construction rather than only by test.
//
// Warnings are expected and ignored: the credential files it points at do not
// exist yet, which is the entire reason the next command is `auth`.
func validateGenerated(body, provider string) error {
	f, err := os.CreateTemp("", "mail-muncher-init-*.yml")
	if err != nil {
		return &pipeline.ExitCodeError{Code: pipeline.ExitConfig, Err: fmt.Errorf("checking the generated config: %w", err)}
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()

	_, writeErr := io.WriteString(f, body)
	closeErr := f.Close()
	if err := errFirst(writeErr, closeErr); err != nil {
		return &pipeline.ExitCodeError{Code: pipeline.ExitConfig, Err: fmt.Errorf("checking the generated config: %w", err)}
	}

	cfg, err := config.Load(name)
	if err == nil {
		err = config.Validate(cfg).Err()
	}
	if err == nil {
		return nil
	}

	// The likeliest cause by far is a provider this build does not know about
	// yet, so say so instead of showing a schema error and stopping.
	hint := ""
	if !providerKnownToConfig(provider) {
		hint = fmt.Sprintf("\nThis build of mail-muncher does not support `provider: %s` yet. "+
			"Use --provider gmail, or upgrade.", provider)
	}
	return &pipeline.ExitCodeError{
		Code: pipeline.ExitConfig,
		Err:  fmt.Errorf("the generated config did not validate, so nothing was written: %w%s", err, hint),
	}
}

// providerKnownToConfig reports whether internal/config recognizes the provider
// name. It is asked only to phrase a failure, never to gate one.
//
// It probes the validator rather than carrying a list of its own, so `init`
// tracks the providers this build actually has: a provider added to
// internal/config needs no edit here, and one that has not landed yet produces
// an honest "not in this build" instead of a schema error.
func providerKnownToConfig(provider string) bool {
	problems := config.Validate(&config.Config{
		StateDir: "probe",
		Accounts: []config.Account{{Name: "probe", Provider: provider}},
	})
	for _, p := range problems.Errors() {
		if strings.Contains(p.Message, "unknown provider") {
			return false
		}
	}
	return true
}

// errFirst returns the first non-nil error.
func errFirst(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// writeConfigFile creates the config directory and writes the file, mode 0600: an
// imap config carries the command that retrieves a mail password, and a gmail
// config carries the path to a credential.
func writeConfigFile(path, body string, force bool) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	_, writeErr := io.WriteString(f, body)
	return errFirst(writeErr, f.Close())
}

// nextSteps is the last thing init prints, and the right sequence for the
// provider that was chosen — not a generic "see the docs".
func nextSteps(opts *initOptions) string {
	dir := filepath.Dir(opts.configPath)

	if opts.provider == "gmail" {
		return fmt.Sprintf(`Next, for provider gmail:
  1. Create a Google Cloud project and a Desktop-app OAuth client, and save the
     downloaded client JSON as:
       %s
     Step by step (about 10 minutes): docs/gmail-setup.md
  2. mail-muncher auth --account %s
     Re-run this weekly: on a Testing-mode consent screen Google expires the
     refresh token every 7 days.
  3. mail-muncher validate
  4. mail-muncher run --dry-run     then     mail-muncher run
Matched mail lands in %s
Docs: %s`,
			filepath.Join(dir, "credentials.json"), opts.account, opts.dest, docsURL)
	}

	return fmt.Sprintf(`Next, for provider imap:
  1. Edit imap.host, imap.username and imap.password_cmd in:
       %s
     Create an app password with your mail provider and store it where
     password_cmd can read it.
  2. Run that same password_cmd in a shell and check it prints the password
     and nothing else, for example:
       %s | cat -A
     Anything else on stdout - a prompt, a warning, a trailing blank line -
     becomes part of the password and the login fails.
  3. mail-muncher validate
  4. mail-muncher run --dry-run     then     mail-muncher run
Matched mail lands in %s
Docs: %s`, opts.configPath, defaultPasswordCmd(runtime.GOOS), opts.dest, docsURL)
}

// sortedProviders is used by tests to iterate every branch this command can
// write, so a provider added to initProviders is covered without editing them.
func sortedProviders() []string {
	out := append([]string(nil), initProviders...)
	sort.Strings(out)
	return out
}

// gmailConfigTemplate is the generated file for `provider: gmail`.
//
// The comments are part of the deliverable: this file is the only
// documentation some people will read, and every value in it that carries a
// cost says so where it is set.
const gmailConfigTemplate = `# mail-muncher configuration, written by "mail-muncher init".
#
# Every key and every default is documented at:
#   %[6]s
# state_dir is not set here, so it defaults to ~/.local/state/mail-muncher.

accounts:
  - name: %[1]s
    provider: gmail
    gmail:
      # The OAuth client JSON downloaded from the Google Cloud Console.
      # mail-muncher ships no client of its own and never will: gmail.readonly
      # is a Google restricted scope. See docs/gmail-setup.md.
      credentials_file: %[2]s
      # Written by "mail-muncher auth --account %[1]s". On a consent screen
      # still in Testing mode, Google expires the refresh token every 7 days,
      # so that command has to be re-run weekly.
      token_file: %[3]s

rules:
  # STARTER RULE, meant to be narrowed.
  #
  # newer_than: %[5]s matches every message from the last three days, so the
  # first run is guaranteed to store something -- which is what proves the
  # install works. Once you have seen mail land, replace this match with what
  # you actually want. The full language is in docs/filters.md.
  - name: recent
    match:
      newer_than: %[5]s
    dest: %[4]s
    formats: [eml, markdown]
`

// imapConfigTemplate is the generated file for `provider: imap`.
const imapConfigTemplate = `# mail-muncher configuration, written by "mail-muncher init".
#
# Every key and every default is documented at:
#   %[4]s
# state_dir is not set here, so it defaults to ~/.local/state/mail-muncher.

accounts:
  - name: %[1]s
    provider: imap
    imap:
      # Your provider's IMAP endpoint and your address. Examples:
      #   Gmail     imap.gmail.com
      #   Fastmail  imap.fastmail.com
      #   Proton    127.0.0.1 via Proton Mail Bridge (port 1143, tls: false)
      host: imap.example.com
      port: 993
      tls: true
      username: you@example.com
      # Never a plaintext password. This command is run to fetch the app
      # password and must print it and nothing else. The line below is the one
      # for the platform that wrote this file; swap in whichever you use:
      #   macOS   ` + keychainPasswordCmd + `
      #   Linux   ` + secretToolPasswordCmd + `
      #   pass    ` + passPasswordCmd + `
      #   1Password / Bitwarden / anything else: any command that prints the
      #           secret on stdout works, including a pipeline such as
      #           ` + passPasswordCmd + ` | head -n 1
      #
      # An app password is a full mail credential -- broader than a read-only
      # OAuth token. mail-muncher only ever issues BODY.PEEK and never sets the
      # \Seen flag, but that restraint is in this program rather than enforced
      # by your provider.
      password_cmd: %[5]s
      mailboxes: [INBOX]

rules:
  # STARTER RULE, meant to be narrowed.
  #
  # newer_than: %[3]s matches every message from the last three days, so the
  # first run is guaranteed to store something -- which is what proves the
  # install works. Once you have seen mail land, replace this match with what
  # you actually want. The full language is in docs/filters.md.
  - name: recent
    match:
      newer_than: %[3]s
    dest: %[2]s
    formats: [eml, markdown]
`
