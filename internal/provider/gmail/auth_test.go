package gmail

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/craigmidwinter/mail-muncher/internal/config"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestLoadOAuthConfigParsesDesktopClient(t *testing.T) {
	cfg, err := LoadOAuthConfig(filepath.Join("testdata", "credentials.json"))
	require.NoError(t, err)
	require.Equal(t, "1234567890-testclient.apps.googleusercontent.com", cfg.ClientID)
	require.Equal(t, "TEST-not-a-real-secret", cfg.ClientSecret)
	require.Equal(t, []string{Scope}, cfg.Scopes, "only the read-only scope may ever be requested")
	require.Equal(t, "https://www.googleapis.com/auth/gmail.readonly", Scope)
	require.Equal(t, "https://oauth2.googleapis.com/token", cfg.Endpoint.TokenURL)
}

func TestLoadOAuthConfigMissingFilePointsAtSetupDoc(t *testing.T) {
	_, err := LoadOAuthConfig(filepath.Join(t.TempDir(), "absent.json"))
	require.ErrorIs(t, err, ErrCredentialsMissing)
	require.Contains(t, err.Error(), SetupDoc)
	require.Contains(t, err.Error(), "absent.json")
}

func TestLoadOAuthConfigRejectsNonClientJSON(t *testing.T) {
	_, err := LoadOAuthConfig(filepath.Join("testdata", "credentials-bad.json"))
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrCredentialsMissing)
	require.Contains(t, err.Error(), SetupDoc)
}

func TestOptionsFromAccount(t *testing.T) {
	good := &config.Account{
		Name:     "work",
		Provider: config.ProviderGmail,
		Gmail: &config.GmailConfig{
			CredentialsFile: "/creds.json",
			TokenFile:       "/token.json",
		},
	}

	opts, err := OptionsFromAccount(good)
	require.NoError(t, err)
	require.Equal(t, Options{Account: "work", CredentialsFile: "/creds.json", TokenFile: "/token.json"}, opts)

	_, err = OptionsFromAccount(nil)
	require.Error(t, err)

	_, err = OptionsFromAccount(&config.Account{Name: "work", Provider: "imap"})
	require.ErrorContains(t, err, "imap")

	_, err = OptionsFromAccount(&config.Account{Name: "work", Provider: config.ProviderGmail})
	require.ErrorContains(t, err, "gmail:")

	_, err = OptionsFromAccount(&config.Account{
		Name:     "work",
		Provider: config.ProviderGmail,
		Gmail:    &config.GmailConfig{TokenFile: "/token.json"},
	})
	require.ErrorContains(t, err, "credentials_file")
}

func TestNewAuthorizerRequiresCredentials(t *testing.T) {
	_, err := NewAuthorizer(Options{
		Account:         "work",
		CredentialsFile: filepath.Join(t.TempDir(), "absent.json"),
		TokenFile:       filepath.Join(t.TempDir(), "token.json"),
	})
	require.ErrorIs(t, err, ErrCredentialsMissing)
}

func TestTokenSourceWithoutStoredTokenTellsUserToAuth(t *testing.T) {
	_, err := TokenSource(context.Background(), Options{
		Account:         "work",
		CredentialsFile: filepath.Join("testdata", "credentials.json"),
		TokenFile:       filepath.Join(t.TempDir(), "token.json"),
	})
	require.ErrorIs(t, err, ErrNoToken)
	require.Contains(t, err.Error(), "mail-muncher auth --account work")
}

// tokenEndpoint is a stand-in for Google's token endpoint: it records the form
// values it was posted and hands back a fixed token.
type tokenEndpoint struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []url.Values
}

func newTokenEndpoint(t *testing.T, body func(form url.Values) string) *tokenEndpoint {
	t.Helper()

	te := &tokenEndpoint{}
	te.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		form, err := url.ParseQuery(string(raw))
		require.NoError(t, err)

		te.mu.Lock()
		te.requests = append(te.requests, form)
		te.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body(form))
	}))
	t.Cleanup(te.server.Close)
	return te
}

func (te *tokenEndpoint) lastRequest(t *testing.T) url.Values {
	t.Helper()

	te.mu.Lock()
	defer te.mu.Unlock()
	require.NotEmpty(t, te.requests, "token endpoint was never called")
	return te.requests[len(te.requests)-1]
}

// loopbackConfig is an OAuth config whose token endpoint is the local test
// server. The auth endpoint is never fetched — the fake browser only parses
// the URL — so it can be anything.
func loopbackConfig(tokenURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Scopes:       []string{Scope},
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.example.invalid/o/oauth2/auth",
			TokenURL: tokenURL,
		},
	}
}

// fakeBrowser stands in for the user's browser: it takes the consent URL, and
// hits the loopback redirect with whatever query parameters Google would have
// sent back. It returns the parsed consent URL so tests can assert on it.
func fakeBrowser(t *testing.T, params func(consent url.Values) url.Values) (func(context.Context, string) error, *url.Values) {
	t.Helper()

	var consent url.Values
	return func(_ context.Context, rawURL string) error {
		parsed, err := url.Parse(rawURL)
		require.NoError(t, err)
		consent = parsed.Query()

		redirect, err := url.Parse(consent.Get("redirect_uri"))
		require.NoError(t, err)
		redirect.RawQuery = params(consent).Encode()

		resp, err := http.Get(redirect.String())
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		_, err = io.ReadAll(resp.Body)
		require.NoError(t, err)
		return nil
	}, &consent
}

func TestAuthorizerRunCompletesLoopbackFlow(t *testing.T) {
	te := newTokenEndpoint(t, func(url.Values) string {
		return `{"access_token":"access-1","refresh_token":"refresh-1","token_type":"Bearer","expires_in":3600}`
	})

	tokenFile := filepath.Join(t.TempDir(), "nested", "token.json")
	browser, consent := fakeBrowser(t, func(c url.Values) url.Values {
		return url.Values{"code": {"auth-code-1"}, "state": {c.Get("state")}}
	})

	out := &bytes.Buffer{}
	auth := &Authorizer{
		Config:      loopbackConfig(te.server.URL),
		TokenFile:   tokenFile,
		Account:     "work",
		Out:         out,
		OpenBrowser: browser,
		Timeout:     10 * time.Second,
	}

	tok, err := auth.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "access-1", tok.AccessToken)
	require.Equal(t, "refresh-1", tok.RefreshToken)

	// The consent URL asks for offline access, forces the consent screen (so a
	// re-auth always yields a refresh token), carries a PKCE challenge, and
	// redirects to the loopback interface only.
	require.Equal(t, Scope, consent.Get("scope"))
	require.Equal(t, "offline", consent.Get("access_type"))
	require.Equal(t, "consent", consent.Get("prompt"))
	require.Equal(t, "S256", consent.Get("code_challenge_method"))
	require.NotEmpty(t, consent.Get("code_challenge"))
	require.NotEmpty(t, consent.Get("state"))
	require.True(t, strings.HasPrefix(consent.Get("redirect_uri"), "http://127.0.0.1:"),
		"redirect must stay on the loopback interface, got %q", consent.Get("redirect_uri"))
	require.True(t, strings.HasSuffix(consent.Get("redirect_uri"), callbackPath))

	// The exchange sends the code, the matching redirect URI, and the PKCE
	// verifier.
	form := te.lastRequest(t)
	require.Equal(t, "authorization_code", form.Get("grant_type"))
	require.Equal(t, "auth-code-1", form.Get("code"))
	require.Equal(t, consent.Get("redirect_uri"), form.Get("redirect_uri"))
	require.NotEmpty(t, form.Get("code_verifier"))

	// And the token landed on disk, owner-only.
	info, err := os.Stat(tokenFile)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	stored, err := LoadToken(tokenFile)
	require.NoError(t, err)
	require.Equal(t, "access-1", stored.AccessToken)
	require.Equal(t, "refresh-1", stored.RefreshToken)

	require.Contains(t, out.String(), "https://accounts.example.invalid/o/oauth2/auth",
		"the consent URL must be printed for users whose browser does not open")
}

func TestAuthorizerRunRejectsStateMismatch(t *testing.T) {
	te := newTokenEndpoint(t, func(url.Values) string {
		return `{"access_token":"should-never-be-issued","token_type":"Bearer"}`
	})

	tokenFile := filepath.Join(t.TempDir(), "token.json")
	browser, _ := fakeBrowser(t, func(url.Values) url.Values {
		return url.Values{"code": {"auth-code-1"}, "state": {"not-the-state-we-sent"}}
	})

	auth := &Authorizer{
		Config:      loopbackConfig(te.server.URL),
		TokenFile:   tokenFile,
		Account:     "work",
		Out:         io.Discard,
		OpenBrowser: browser,
		Timeout:     10 * time.Second,
	}

	_, err := auth.Run(context.Background())
	require.ErrorContains(t, err, "state")
	require.NoFileExists(t, tokenFile)

	te.mu.Lock()
	defer te.mu.Unlock()
	require.Empty(t, te.requests, "a mismatched state must never reach the token endpoint")
}

func TestAuthorizerRunReportsDeniedConsent(t *testing.T) {
	te := newTokenEndpoint(t, func(url.Values) string { return `{}` })

	tokenFile := filepath.Join(t.TempDir(), "token.json")
	browser, _ := fakeBrowser(t, func(c url.Values) url.Values {
		return url.Values{
			"error":             {"access_denied"},
			"error_description": {"The user denied access"},
			"state":             {c.Get("state")},
		}
	})

	auth := &Authorizer{
		Config:      loopbackConfig(te.server.URL),
		TokenFile:   tokenFile,
		Account:     "work",
		Out:         io.Discard,
		OpenBrowser: browser,
		Timeout:     10 * time.Second,
	}

	_, err := auth.Run(context.Background())
	require.ErrorContains(t, err, "access_denied")
	require.ErrorContains(t, err, "The user denied access")
	require.NoFileExists(t, tokenFile)
}

func TestAuthorizerRunReportsExchangeFailure(t *testing.T) {
	te := newTokenEndpoint(t, func(url.Values) string { return `{"error":"invalid_grant"}` })

	tokenFile := filepath.Join(t.TempDir(), "token.json")
	browser, _ := fakeBrowser(t, func(c url.Values) url.Values {
		return url.Values{"code": {"stale-code"}, "state": {c.Get("state")}}
	})

	auth := &Authorizer{
		Config:      loopbackConfig(te.server.URL),
		TokenFile:   tokenFile,
		Account:     "work",
		Out:         io.Discard,
		OpenBrowser: browser,
		Timeout:     10 * time.Second,
	}

	_, err := auth.Run(context.Background())
	require.ErrorContains(t, err, "exchange authorization code")
	require.NoFileExists(t, tokenFile)
}

func TestAuthorizerRunTimesOutWaitingForTheBrowser(t *testing.T) {
	auth := &Authorizer{
		Config:      loopbackConfig("https://token.example.invalid/token"),
		TokenFile:   filepath.Join(t.TempDir(), "token.json"),
		Account:     "work",
		Out:         io.Discard,
		OpenBrowser: func(context.Context, string) error { return nil },
		Timeout:     50 * time.Millisecond,
	}

	_, err := auth.Run(context.Background())
	require.ErrorContains(t, err, "timed out")
	require.ErrorContains(t, err, "mail-muncher auth --account work")
}

func TestAuthorizerRunHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	auth := &Authorizer{
		Config:    loopbackConfig("https://token.example.invalid/token"),
		TokenFile: filepath.Join(t.TempDir(), "token.json"),
		Account:   "work",
		Out:       io.Discard,
		OpenBrowser: func(context.Context, string) error {
			cancel()
			return nil
		},
	}

	_, err := auth.Run(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.NotContains(t, err.Error(), "timed out")
}

func TestAuthorizerRunNeedsConfigAndTokenFile(t *testing.T) {
	_, err := (&Authorizer{TokenFile: "/token.json"}).Run(context.Background())
	require.ErrorContains(t, err, "OAuth config")

	_, err = (&Authorizer{Config: loopbackConfig("https://example.invalid")}).Run(context.Background())
	require.ErrorContains(t, err, "token file")
}

// TestNewClientRefreshesAndPersists exercises the whole runtime path offline:
// credentials on disk (pointing at a local token endpoint), an expired cached
// token, a refresh, and the refreshed token written back.
func TestNewClientRefreshesAndPersists(t *testing.T) {
	te := newTokenEndpoint(t, func(url.Values) string {
		return `{"access_token":"access-2","refresh_token":"refresh-2","token_type":"Bearer","expires_in":3600}`
	})

	dir := t.TempDir()
	credsFile := filepath.Join(dir, "credentials.json")
	credentials := fmt.Sprintf(`{"installed":{"client_id":"test-client-id","client_secret":"test-secret",`+
		`"auth_uri":"https://accounts.example.invalid/o/oauth2/auth","token_uri":%q,`+
		`"redirect_uris":["http://localhost"]}}`, te.server.URL)
	require.NoError(t, os.WriteFile(credsFile, []byte(credentials), 0o600))

	tokenFile := filepath.Join(dir, "token.json")
	require.NoError(t, SaveToken(tokenFile, &oauth2.Token{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(-time.Hour), // expired: forces a refresh
	}))

	opts := Options{Account: "work", CredentialsFile: credsFile, TokenFile: tokenFile}
	client, err := NewClient(context.Background(), opts)
	require.NoError(t, err)

	// One authenticated request against a server that echoes the header back.
	var gotAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	t.Cleanup(api.Close)

	resp, err := client.Get(api.URL)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, "Bearer access-2", gotAuth)

	form := te.lastRequest(t)
	require.Equal(t, "refresh_token", form.Get("grant_type"))
	require.Equal(t, "refresh-1", form.Get("refresh_token"))

	stored, err := LoadToken(tokenFile)
	require.NoError(t, err)
	require.Equal(t, "access-2", stored.AccessToken)
	require.Equal(t, "refresh-2", stored.RefreshToken)
}
