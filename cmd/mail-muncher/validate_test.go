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
