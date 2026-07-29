package main

import (
	"bytes"
	"os"
	"path/filepath"
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

			stdout, _, err := runInitCLI(t, "",
				"init", "--config", path,
				"--provider", provider,
				"--account", "personal",
				"--dest", filepath.Join(t.TempDir(), "mail"),
				"--yes")

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
			got := nextSteps(opts)
			for _, w := range want {
				require.Contains(t, got, w)
			}
		})
	}
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

	for _, name := range []string{"provider", "account", "dest", "yes", "force"} {
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
