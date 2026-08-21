package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/pipeline"
)

// runInitCLI drives the command tree with a stdin nobody is typing at, which is
// how an agent runs it.
func runInitCLI(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	var out, errBuf bytes.Buffer
	root := newRootCommand()
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)

	err = root.Execute()
	return out.String(), errBuf.String(), err
}

// TestInitProducesAConfigThatValidates is the acceptance criterion, asserted
// for every provider branch: whatever init writes, `mail-muncher validate` must
// accept. A config that init wrote and validate rejects is the worst possible
// first impression.
func TestInitProducesAConfigThatValidates(t *testing.T) {
	for _, provider := range sortedProviders() {
		t.Run(provider, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mail-muncher", "config.yml")

			args := []string{
				"init", "--config", path,
				"--provider", provider,
				"--account", "personal",
				"--dest", filepath.Join(t.TempDir(), "mail"),
				"--yes",
			}
			if provider == "imap" {
				// --yes takes no default for host and username, so a fully
				// non-interactive imap run has to name them.
				args = append(args, "--host", "imap.fastmail.com", "--username", "personal@fastmail.com")
			}

			stdout, _, err := runInitCLI(t, "", args...)

			if err != nil {
				// The imap provider may not exist in this build yet; if so, init
				// must refuse rather than write a config validate would reject.
				requireProviderUnavailable(t, provider, err)
				require.NoFileExists(t, path, "a config that would not validate must not be written")
				return
			}

			require.FileExists(t, path)
			require.Contains(t, stdout, "Wrote "+path)

			// The real command, over the real file.
			out, err := execute(t, "validate", "--config", path)
			require.NoError(t, err, "the generated config must pass `mail-muncher validate`:\n%s\n%s",
				out, readFileForTest(t, path))
			require.Contains(t, out, "1 account(s), 1 rule(s)")
		})
	}
}

// TestInitStarterRuleIsTheSmokeTest: the generated rule has to match something
// on the first run, because a first run that stores nothing is
// indistinguishable from a broken install. It also has to say it is a starter.
func TestInitStarterRuleIsTheSmokeTest(t *testing.T) {
	for _, provider := range sortedProviders() {
		t.Run(provider, func(t *testing.T) {
			opts := &initOptions{
				configPath: filepath.Join(t.TempDir(), "config.yml"),
				provider:   provider,
				account:    "personal",
				dest:       "~/Mail/mail-muncher",
			}
			if provider == "imap" {
				opts.host = "imap.example.org"
				opts.username = "personal@example.org"
				opts.passwordCmd = defaultPasswordCmd(runtime.GOOS)
			}
			body, err := renderConfig(opts)
			require.NoError(t, err)

			require.Contains(t, body, "newer_than: 72h")
			require.Contains(t, body, "STARTER RULE, meant to be narrowed")
			require.Contains(t, body, "docs/filters.md")
			require.Contains(t, body, "dest: ~/Mail/mail-muncher")
			require.Contains(t, body, "provider: "+provider)
		})
	}
}

// TestDefaultPasswordCmdIsPlatformAware pins the selection directly, for every
// GOOS this can run on — the whole point being that the branches this machine
// cannot execute are still checked.
//
// The old generated default was the macOS `security` command unconditionally,
// so on Linux the first `mail-muncher run` failed with a command-not-found that
// said nothing about mail.
func TestDefaultPasswordCmdIsPlatformAware(t *testing.T) {
	cases := map[string]string{
		"darwin":  keychainPasswordCmd,
		"linux":   secretToolPasswordCmd,
		"freebsd": secretToolPasswordCmd,
		"openbsd": secretToolPasswordCmd,
		"windows": passPasswordCmd,
		"plan9":   passPasswordCmd,
		"":        passPasswordCmd,
	}
	for goos, want := range cases {
		require.Equalf(t, want, defaultPasswordCmd(goos), "defaultPasswordCmd(%q)", goos)
	}

	// Whatever the platform, the command names a real secret manager and never
	// the macOS binary anywhere but macOS.
	for goos := range cases {
		got := defaultPasswordCmd(goos)
		require.NotEmpty(t, got)
		if goos != "darwin" {
			require.NotContains(t, got, "security find-generic-password",
				"%s must not be handed the macOS keychain command", goos)
		}
	}
}

// TestInitPasswordCmdDefaultAndAlternatives: the generated imap config carries
// the command for the platform that wrote it, and documents the others so the
// file is self-explanatory when it is copied to a different machine.
func TestInitPasswordCmdDefaultAndAlternatives(t *testing.T) {
	opts := &initOptions{
		configPath:  filepath.Join(t.TempDir(), "config.yml"),
		provider:    "imap",
		account:     defaultInitAccount,
		dest:        defaultInitDest,
		host:        "imap.fastmail.com",
		username:    "you@fastmail.com",
		passwordCmd: defaultPasswordCmd(runtime.GOOS),
	}
	body, err := renderConfig(opts)
	require.NoError(t, err)

	require.Contains(t, body, "password_cmd: "+defaultPasswordCmd(runtime.GOOS),
		"the seeded command must be the one for this platform")

	// Every alternative stays listed, so the file documents the choice either
	// way round.
	for _, alt := range []string{keychainPasswordCmd, secretToolPasswordCmd, passPasswordCmd} {
		require.Contains(t, body, alt)
	}
	require.Contains(t, body, "macOS")
	require.Contains(t, body, "Linux")

	// And the "check it prints the password and nothing else" step echoes the
	// same command, rather than one that does not exist here.
	require.Contains(t, nextSteps(opts), defaultPasswordCmd(runtime.GOOS)+" | cat -A")

	// The config every platform's branch would write must validate, not only
	// this one's — which is the part a darwin machine cannot otherwise prove.
	for _, goos := range []string{"darwin", "linux", "windows"} {
		body := fmt.Sprintf(imapConfigTemplate,
			opts.account, opts.dest, starterRuleAge, docsURL, opts.host, opts.username, defaultPasswordCmd(goos))
		require.Contains(t, body, "password_cmd: "+defaultPasswordCmd(goos))
		require.NoErrorf(t, validateGenerated(body, "imap"),
			"the config init writes on %s must pass validate:\n%s", goos, body)
	}
}

// TestInitNeverOverwrites is the hard rule: somebody's rules, destinations and
// credential paths live in that file.
func TestInitNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	const existing = "# hand-written, do not lose\n"
	require.NoError(t, os.WriteFile(path, []byte(existing), 0o600))

	_, _, err := runInitCLI(t, "", "init", "--config", path, "--provider", "gmail", "--yes")
	require.Error(t, err)
	require.Equal(t, pipeline.ExitConfig, exitCodeOf(t, err))
	require.Contains(t, err.Error(), path, "the path of the file it refused to touch")
	require.Contains(t, err.Error(), "--force")

	require.Equal(t, existing, readFileForTest(t, path), "the existing config must be untouched")
}

// TestInitForceOverwrites: --force, and only --force, may replace it.
func TestInitForceOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(path, []byte("# old\n"), 0o600))

	_, _, err := runInitCLI(t, "", "init", "--config", path, "--provider", "gmail", "--yes", "--force")
	require.NoError(t, err)

	body := readFileForTest(t, path)
	require.NotContains(t, body, "# old")
	require.Contains(t, body, "provider: gmail")

	_, err = execute(t, "validate", "--config", path)
	require.NoError(t, err)
}

// TestInitIsFullyNonInteractive: an agent runs this, and it must never block on
// a prompt or invent an answer it was not given.
func TestInitIsFullyNonInteractive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	dest := filepath.Join(dir, "archive")

	stdout, _, err := runInitCLI(t, "",
		"init", "--config", path, "--provider", "gmail", "--account", "work", "--dest", dest)
	require.NoError(t, err)

	body := readFileForTest(t, path)
	require.Contains(t, body, "name: work")
	require.Contains(t, body, "dest: "+dest)

	// Credential paths sit beside the config, named exactly.
	require.Contains(t, body, "credentials_file: "+filepath.Join(dir, "credentials.json"))
	require.Contains(t, body, "token_file: "+filepath.Join(dir, "token.json"))

	// And the next command is the right one for the provider, named in full.
	require.Contains(t, stdout, "mail-muncher auth --account work")
	require.NotContains(t, stdout, "Provider (", "a flag-complete invocation must not prompt")
}

// TestInitYesRequiresAProvider: --yes takes the default for everything that has
// one, and the provider does not have one. The two paths cost different things
// and picking for the user would be the editorializing this avoids.
func TestInitYesRequiresAProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")

	_, _, err := runInitCLI(t, "", "init", "--config", path, "--yes")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--provider is required")
	require.Contains(t, err.Error(), "imap")
	require.Contains(t, err.Error(), "gmail")
	require.NoFileExists(t, path)
}

// TestInitDefaults: --yes with a provider takes the documented defaults for the
// other two answers.
func TestInitDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")

	_, _, err := runInitCLI(t, "", "init", "--config", path, "--provider", "gmail", "--yes")
	require.NoError(t, err)

	body := readFileForTest(t, path)
	require.Contains(t, body, "name: "+defaultInitAccount)
	require.Contains(t, body, "dest: "+defaultInitDest)
}

// TestInitInteractive: answers typed at the prompt are used, and a bare return
// takes the default.
func TestInitInteractive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")

	stdout, _, err := runInitCLI(t, "gmail\nwork\n\n", "init", "--config", path)
	require.NoError(t, err)

	// The provider trade-off is stated before the question, not after.
	require.Contains(t, stdout, "Both are supported; the costs differ")
	require.Less(t, strings.Index(stdout, "Both are supported"), strings.Index(stdout, "Provider ("))

	// And the Cloud Console cost lands before any further question is asked.
	require.Contains(t, stdout, "about 10 minutes in the Google")
	require.Contains(t, stdout, "docs/gmail-setup.md")
	require.Less(t, strings.Index(stdout, "about 10 minutes in the Google"),
		strings.Index(stdout, "Account name"))

	body := readFileForTest(t, path)
	require.Contains(t, body, "name: work")
	require.Contains(t, body, "dest: "+defaultInitDest, "a bare return takes the default")
}

// TestInitRejectsUnknownProvider names the flag and the legal values.
func TestInitRejectsUnknownProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")

	_, _, err := runInitCLI(t, "", "init", "--config", path, "--provider", "pop3", "--yes")
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown provider "pop3"`)
	require.Contains(t, err.Error(), "imap, gmail")
	require.NoFileExists(t, path)
}

// TestInitNextStepsAreProviderSpecific: the last thing printed must be the
// right sequence for the provider that was chosen, not a generic "see the docs".
func TestInitNextStepsAreProviderSpecific(t *testing.T) {
	cases := map[string][]string{
		"gmail": {
			"mail-muncher auth --account personal",
			"docs/gmail-setup.md",
			"about 10 minutes",
			"every 7 days",
			"credentials.json",
			"mail-muncher validate",
			"mail-muncher run --dry-run",
		},
		"imap": {
			"password_cmd",
			"app password",
			"check it prints the password",
			"mail-muncher validate",
			"mail-muncher run --dry-run",
		},
	}

	for provider, want := range cases {
		t.Run(provider, func(t *testing.T) {
			opts := &initOptions{
				configPath: "/home/someone/.config/mail-muncher/config.yml",
				provider:   provider,
				account:    defaultInitAccount,
				dest:       defaultInitDest,
			}
			if provider == "imap" {
				opts.host = "imap.fastmail.com"
				opts.username = "you@fastmail.com"
				opts.passwordCmd = defaultPasswordCmd(runtime.GOOS)
			}
			got := nextSteps(opts)
			for _, w := range want {
				require.Contains(t, got, w)
			}
		})
	}
}

// TestInitWithImapValuesNeedsNoEditor: host, username and password_cmd
// supplied on the command line produce a config with no placeholders and
// next-steps guidance with no "edit this file" step — the fix for the
// ergonomics violation on mail-muncher's primary onboarding path.
func TestInitWithImapValuesNeedsNoEditor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	dest := filepath.Join(dir, "archive")

	stdout, _, err := runInitCLI(t, "",
		"init", "--config", path,
		"--provider", "imap",
		"--account", "personal",
		"--dest", dest,
		"--host", "imap.fastmail.com",
		"--username", "you@fastmail.com",
		"--password-cmd", "security find-generic-password -s mail-muncher -w",
	)
	require.NoError(t, err)
	require.NotContains(t, stdout, "IMAP host", "a flag-complete imap invocation must not prompt")

	body := readFileForTest(t, path)
	require.Contains(t, body, "host: imap.fastmail.com")
	require.Contains(t, body, "username: you@fastmail.com")
	require.Contains(t, body, "password_cmd: security find-generic-password -s mail-muncher -w")
	require.NotContains(t, body, "imap.example.com", "no placeholder host must survive into the file")
	require.NotContains(t, body, "you@example.com", "no placeholder username must survive into the file")

	require.NotContains(t, stdout, "Edit imap.host",
		"the values were supplied, so there is nothing left to edit")
	require.Contains(t, stdout, "security find-generic-password -s mail-muncher -w | cat -A")
	require.Contains(t, stdout, "mail-muncher validate")
	require.Contains(t, stdout, "mail-muncher run --dry-run")

	// And the file this produced is exactly as usable as the generic
	// acceptance test already proves for --yes: real values, not placeholders,
	// pass validate too.
	out, err := execute(t, "validate", "--config", path)
	require.NoError(t, err)
	require.Contains(t, out, "1 account(s), 1 rule(s)")
}

// TestInitInteractivePromptsForImapConnectionDetails: with no flags, an
// interactive imap run asks for host, username and password_cmd the same way
// it already asks for account and dest — host and username with no default,
// password_cmd with the platform one, and a bare return on password_cmd takes
// that default.
func TestInitInteractivePromptsForImapConnectionDetails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")

	stdin := "imap\nwork\n\nimap.fastmail.com\nyou@fastmail.com\n\n"
	stdout, _, err := runInitCLI(t, stdin, "init", "--config", path)
	require.NoError(t, err)

	require.Contains(t, stdout, "IMAP host: ")
	require.Contains(t, stdout, "IMAP username: ")
	require.Contains(t, stdout, "Password command ["+defaultPasswordCmd(runtime.GOOS)+"]: ")

	body := readFileForTest(t, path)
	require.Contains(t, body, "host: imap.fastmail.com")
	require.Contains(t, body, "username: you@fastmail.com")
	require.Contains(t, body, "password_cmd: "+defaultPasswordCmd(runtime.GOOS),
		"a bare return on the password command takes the platform default")

	require.NotContains(t, stdout, "Edit imap.host")
}

// TestInitYesRequiresHostAndUsernameForImap: --yes takes the platform default
// for password_cmd, exactly as it already takes defaultInitAccount and
// defaultInitDest, but host and username have no honest default — so, like a
// missing --provider, init refuses rather than writing a config that would
// still need manual editing.
func TestInitYesRequiresHostAndUsernameForImap(t *testing.T) {
	t.Run("neither supplied", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yml")

		_, _, err := runInitCLI(t, "", "init", "--config", path, "--provider", "imap", "--yes")
		require.Error(t, err)
		require.Contains(t, err.Error(), "--host")
		require.Contains(t, err.Error(), "--username")
		require.Contains(t, err.Error(), "required with --yes")
		require.NoFileExists(t, path)
	})

	t.Run("only host supplied", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yml")

		_, _, err := runInitCLI(t, "", "init", "--config", path, "--provider", "imap", "--yes",
			"--host", "imap.fastmail.com")
		require.Error(t, err)
		require.Contains(t, err.Error(), "--username")
		require.NotContains(t, err.Error(), "--host and", "host was already supplied")
		require.NoFileExists(t, path)
	})

	t.Run("host and username supplied, password-cmd defaulted", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yml")

		_, _, err := runInitCLI(t, "", "init", "--config", path, "--provider", "imap", "--yes",
			"--host", "imap.fastmail.com", "--username", "you@fastmail.com")
		require.NoError(t, err)

		body := readFileForTest(t, path)
		require.Contains(t, body, "host: imap.fastmail.com")
		require.Contains(t, body, "username: you@fastmail.com")
		require.Contains(t, body, "password_cmd: "+defaultPasswordCmd(runtime.GOOS))
	})
}

// TestInitWritesPrivately: an imap config carries the command that retrieves a
// mail password and a gmail config the path to a credential.
func TestInitWritesPrivately(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	path := filepath.Join(dir, "config.yml")

	_, _, err := runInitCLI(t, "", "init", "--config", path, "--provider", "gmail", "--yes")
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestInitCommandIsRegistered pins the flag set the docs and any installing
// agent depend on.
func TestInitCommandIsRegistered(t *testing.T) {
	root := newRootCommand()
	cmd := findCommand(t, root, "init")

	for _, name := range []string{"provider", "account", "dest", "host", "username", "password-cmd", "yes", "force"} {
		require.NotNil(t, cmd.Flags().Lookup(name), "init must expose --"+name)
	}
	require.Nil(t, cmd.Flags().Lookup("config"), "init takes the persistent --config flag")
}

// TestGeneratedConfigIsSelfValidating: the check init runs before writing is
// the thing that makes "the generated config validates" true by construction.
func TestGeneratedConfigIsSelfValidating(t *testing.T) {
	require.Error(t, validateGenerated("accounts: []\n", "gmail"),
		"a config with no accounts must be caught before it is written")

	err := validateGenerated("accounts:\n  - name: x\n    provider: nonesuch\n", "nonesuch")
	require.Error(t, err)
	require.Contains(t, err.Error(), "did not validate")
	require.Contains(t, err.Error(), "nothing was written")
}

// requireProviderUnavailable asserts that a provider init could not write for
// failed loudly and said what to do instead.
func requireProviderUnavailable(t *testing.T, provider string, err error) {
	t.Helper()
	require.False(t, providerKnownToConfig(provider),
		"provider %q is supported by internal/config, so init must be able to write it: %v", provider, err)
	require.Contains(t, err.Error(), "did not validate")
	require.Contains(t, err.Error(), "does not support")
	require.Contains(t, err.Error(), config.ProviderGmail)
}

func readFileForTest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}
