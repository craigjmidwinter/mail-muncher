package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeConfig drops a config into a temp dir and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestValidateOKWithWarningsExitsZero(t *testing.T) {
	// The credential/token files do not exist, which is a warning only.
	path := writeConfig(t, `
accounts:
  - name: personal
    provider: gmail
    gmail:
      credentials_file: /nonexistent/credentials.json
      token_file: /nonexistent/token.json
rules:
  - name: job-search
    account: personal
    match: {from_domains: [example.com]}
    dest: /tmp/mail/job-search
`)

	out, err := execute(t, "validate", "--config", path)
	require.NoError(t, err, out)
	require.Contains(t, out, "config: "+path)
	require.Contains(t, out, "1 account(s), 1 rule(s)")
	require.Contains(t, out, "warning: accounts[0].gmail.credentials_file")
	require.Contains(t, out, "OK with 2 warnings")
}

func TestValidateReportsErrorsAndFails(t *testing.T) {
	path := writeConfig(t, `
accounts:
  - name: personal
    provider: gmail
    gmail: {credentials_file: /a, token_file: /b}
rules:
  - name: dupe
    match: {has_attachment: true}
    dest: /one
    formats: [eml, pdf]
  - name: dupe
    account: nope
    match: {has_attachment: true}
    dest: ""
`)

	out, err := execute(t, "validate", "--config", path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "config is invalid")

	require.Contains(t, out, `error: rules[0].formats[1]: unknown format "pdf"`)
	require.Contains(t, out, `error: rules[1].name: duplicate rule name "dupe"`)
	require.Contains(t, out, `error: rules[1].account: unknown account "nope"`)
	require.Contains(t, out, "error: rules[1].dest: must not be empty")
	require.Contains(t, out, "FAILED: 4 errors")
}

// TestValidateRequiresProvider: an account with no `provider:` fails, and the
// message names both options and what each costs rather than only saying the
// key is required.
//
// The key used to default to gmail, so a hand-written config that said nothing
// silently took the Google Cloud Console path — and its weekly token expiry —
// without the author ever choosing it.
func TestValidateRequiresProvider(t *testing.T) {
	path := writeConfig(t, `
accounts:
  - name: personal
    gmail: {credentials_file: /a, token_file: /b}
rules:
  - name: r
    match: {has_attachment: true}
    dest: /one
`)

	out, err := execute(t, "validate", "--config", path)
	require.Error(t, err)
	require.Contains(t, out, `error: accounts[0].provider: required: want "imap" (app password, ~2 min) `+
		`or "gmail" (Google Cloud Console, ~10 min, plus a token to re-issue every 7 days)`)
	require.Contains(t, out, "FAILED: 1 error")
}

// TestValidateDoesNotRunPasswordCmd: checking whether a config parses must
// never execute the command that fetches the mail password.
//
// It is the difference between a check anyone can run on any machine — in CI,
// on a box where the keyring is locked, on a platform where the configured
// secret manager is not installed — and one that only works where the secret
// already lives. `mail-muncher run` is where the command is executed.
func TestValidateDoesNotRunPasswordCmd(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "password_cmd-was-run")
	path := writeConfig(t, `
accounts:
  - name: personal
    provider: imap
    imap:
      host: imap.example.com
      username: you@example.com
      password_cmd: touch `+sentinel+`
rules:
  - name: r
    match: {label: INBOX}
    dest: `+filepath.Join(dir, "mail")+`
`)

	out, err := execute(t, "validate", "--config", path)
	require.NoError(t, err, out)
	require.NoFileExists(t, sentinel, "validate must not execute password_cmd")
}

func TestValidateReportsUnknownKey(t *testing.T) {
	path := writeConfig(t, "bogus_key: 1\n")

	_, err := execute(t, "validate", "--config", path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found in type")
}

// TestValidateMissingConfigFile: "there is no config file" is a distinct state
// from "the config file is broken", and `validate` answers it with setup
// guidance rather than an open(2) failure. The full contract is asserted in
// guidance_test.go; this only pins that `validate` is on that path.
func TestValidateMissingConfigFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yml")

	_, err := execute(t, "validate", "--config", missing)
	require.Error(t, err)

	var se *setupError
	require.ErrorAs(t, err, &se)
	require.Equal(t, kindNoConfig, se.Kind)
	require.Contains(t, err.Error(), missing)
	require.Contains(t, err.Error(), "mail-muncher init")
}

func TestValidateTestdataConfigEndToEnd(t *testing.T) {
	// Mirrors the documented invocation:
	//   mail-muncher validate --config internal/config/testdata/config.yml
	t.Setenv("HOME", t.TempDir())

	path := filepath.Join("..", "..", "internal", "config", "testdata", "config.yml")
	out, err := execute(t, "validate", "--config", path)
	require.NoError(t, err, out)
	require.Contains(t, out, "1 account(s), 2 rule(s)")
	require.Contains(t, out, "warning: rules[0].match.any[0].from_domains_file")
	require.Contains(t, out, "OK with")
}
