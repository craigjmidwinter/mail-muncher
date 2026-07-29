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

func TestValidateReportsUnknownKey(t *testing.T) {
	path := writeConfig(t, "bogus_key: 1\n")

	_, err := execute(t, "validate", "--config", path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found in type")
}

func TestValidateMissingConfigFile(t *testing.T) {
	_, err := execute(t, "validate", "--config", filepath.Join(t.TempDir(), "nope.yml"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "open config")
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
