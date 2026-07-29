package gmail

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/craigmidwinter/mail-muncher/internal/config"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	gmailapi "google.golang.org/api/gmail/v1"
)

// Scope is the only OAuth scope mail-muncher ever requests: read-only access
// to the mailbox. It cannot modify, send, or delete mail.
const Scope = gmailapi.GmailReadonlyScope

// SetupDoc is the repo-relative path of the Google Cloud setup walkthrough,
// quoted in errors that a user can only fix by going through it.
const SetupDoc = "docs/gmail-setup.md"

// callbackPath is the loopback redirect path. Google's desktop-app clients
// accept any path (and any port) on 127.0.0.1, so this is only about keeping
// the browser's stray requests — /favicon.ico and friends — off the handler
// that completes the flow.
const callbackPath = "/oauth2/callback"

// DefaultAuthTimeout bounds how long Authorizer.Run waits for the user to
// finish consenting in the browser before giving up.
const DefaultAuthTimeout = 5 * time.Minute

// ErrCredentialsMissing is returned when the account's OAuth client JSON is
// not on disk. It is a warning (not an error) at config-validation time, since
// a config is legitimately written before the client is downloaded, so it has
// to be a clear error here.
var ErrCredentialsMissing = errors.New("gmail: OAuth credentials file not found")

// Options identifies one account's Gmail credentials. It is the single input
// every entry point in this package takes, so callers thread one value through
// auth, token refresh, and fetching.
type Options struct {
	// Account is the config account name. It is used only in log lines and
	// error messages (notably to spell out the exact `auth` command to re-run).
	Account string
	// CredentialsFile is the OAuth client JSON downloaded from the Google
	// Cloud Console, already ~/$ENV-expanded by internal/config.
	CredentialsFile string
	// TokenFile is where the OAuth token is cached, already ~/$ENV-expanded by
	// internal/config. It is created by `mail-muncher auth` and rewritten
	// whenever a refresh produces a new token.
	TokenFile string
}

// Validate reports whether the options carry everything this package needs. It
// checks that the fields are populated, not that the files exist.
func (o Options) Validate() error {
	switch {
	case strings.TrimSpace(o.CredentialsFile) == "":
		return errors.New("gmail: credentials_file is empty")
	case strings.TrimSpace(o.TokenFile) == "":
		return errors.New("gmail: token_file is empty")
	}
	return nil
}

// OptionsFromAccount extracts the Gmail options from a config account,
// rejecting accounts that are not Gmail-backed.
func OptionsFromAccount(account *config.Account) (Options, error) {
	if account == nil {
		return Options{}, errors.New("gmail: nil account")
	}
	if account.Provider != config.ProviderGmail {
		return Options{}, fmt.Errorf("gmail: account %q uses provider %q, not %q",
			account.Name, account.Provider, config.ProviderGmail)
	}
	if account.Gmail == nil {
		return Options{}, fmt.Errorf("gmail: account %q has no `gmail:` section", account.Name)
	}

	opts := Options{
		Account:         account.Name,
		CredentialsFile: account.Gmail.CredentialsFile,
		TokenFile:       account.Gmail.TokenFile,
	}
	if err := opts.Validate(); err != nil {
		return Options{}, fmt.Errorf("account %q: %w", account.Name, err)
	}
	return opts, nil
}

// LoadOAuthConfig reads the downloaded OAuth client JSON and builds the
// oauth2 config for the read-only Gmail scope.
//
// A missing file yields an error matching ErrCredentialsMissing that points at
// the setup doc, because there is nothing the user can do about it except go
// create the client.
func LoadOAuthConfig(credentialsFile string) (*oauth2.Config, error) {
	data, err := os.ReadFile(credentialsFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s\n"+
				"Create a Desktop app OAuth client in the Google Cloud Console, download its JSON, "+
				"and save it there (or point `gmail.credentials_file` at it). See %s.",
				ErrCredentialsMissing, credentialsFile, SetupDoc)
		}
		return nil, fmt.Errorf("gmail: read credentials %s: %w", credentialsFile, err)
	}

	cfg, err := google.ConfigFromJSON(data, Scope)
	if err != nil {
		return nil, fmt.Errorf("gmail: parse credentials %s: %w (expected the OAuth client JSON "+
			"downloaded from the Google Cloud Console; see %s)", credentialsFile, err, SetupDoc)
	}
	return cfg, nil
}

// TokenSource returns a token source for the account: it starts from the
// cached token, refreshes it automatically when it expires, and writes any
// refreshed token back to the token file.
//
// This is the entry point for every non-interactive code path — the fetcher
// should call it (or NewClient) rather than touching the token file itself. If
// `auth` has never run, the error matches ErrNoToken.
func TokenSource(ctx context.Context, opts Options) (oauth2.TokenSource, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	cfg, err := LoadOAuthConfig(opts.CredentialsFile)
	if err != nil {
		return nil, err
	}

	tok, err := LoadToken(opts.TokenFile)
	if err != nil {
		if errors.Is(err, ErrNoToken) {
			return nil, fmt.Errorf("%w: run `mail-muncher auth --account %s` first", err, opts.Account)
		}
		return nil, err
	}

	return PersistingTokenSource(cfg.TokenSource(ctx, tok), opts.TokenFile, opts.Account), nil
}

// NewClient returns an HTTP client that authenticates every request as the
// account, refreshing and persisting the token as needed. Hand it to the Gmail
// API with option.WithHTTPClient.
//
// The returned client honours ctx for its lifetime (cancelling ctx breaks the
// client), so pass a context that outlives the API calls made with it.
func NewClient(ctx context.Context, opts Options) (*http.Client, error) {
	src, err := TokenSource(ctx, opts)
	if err != nil {
		return nil, err
	}
	return oauth2.NewClient(ctx, src), nil
}

// Authorizer runs the interactive loopback OAuth flow that `mail-muncher auth`
// is made of. Build one with NewAuthorizer; the exported fields are optional
// overrides, mostly for tests.
type Authorizer struct {
	// Config is the OAuth client the flow authenticates against. NewAuthorizer
	// fills it in from the credentials file; Run overwrites its RedirectURL
	// with the loopback address it actually listened on.
	Config *oauth2.Config
	// TokenFile is where Run writes the token it obtains.
	TokenFile string
	// Account names the account, for log and prompt text.
	Account string

	// ListenAddr is the address the loopback callback server binds. Empty
	// means 127.0.0.1 on an OS-assigned port, which is what desktop OAuth
	// clients are meant to use.
	ListenAddr string
	// Out receives the human-facing prompt (the consent URL). Nil means
	// os.Stdout.
	Out io.Writer
	// OpenBrowser is invoked with the consent URL. It is best effort: a
	// failure is logged and the user follows the printed URL by hand. Nil
	// means the platform's default opener.
	OpenBrowser func(ctx context.Context, url string) error
	// Timeout bounds the wait for the browser redirect. Zero means
	// DefaultAuthTimeout.
	Timeout time.Duration
}

// NewAuthorizer builds an Authorizer for the account described by opts,
// reading its OAuth client from opts.CredentialsFile.
func NewAuthorizer(opts Options) (*Authorizer, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	cfg, err := LoadOAuthConfig(opts.CredentialsFile)
	if err != nil {
		return nil, err
	}
	return &Authorizer{Config: cfg, TokenFile: opts.TokenFile, Account: opts.Account}, nil
}

// Run performs the loopback authorization-code flow and saves the resulting
// token to TokenFile (mode 0600, atomically).
//
// It listens on the loopback interface, points the OAuth redirect at that
// address, prints (and best-effort opens) the consent URL, waits for Google to
// redirect the browser back with a code, and exchanges it. The flow is bound
// by ctx and by Timeout, and PKCE plus a random `state` value guard the
// exchange.
//
// Offline access is requested explicitly, and consent is forced, so that a
// re-authorization always yields a fresh refresh token rather than an
// access-token-only grant that would stop working in an hour.
func (a *Authorizer) Run(ctx context.Context) (*oauth2.Token, error) {
	if a.Config == nil {
		return nil, errors.New("gmail: authorizer has no OAuth config")
	}
	if strings.TrimSpace(a.TokenFile) == "" {
		return nil, errors.New("gmail: authorizer has no token file")
	}

	addr := a.ListenAddr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("gmail: listen on %s for the OAuth redirect: %w", addr, err)
	}
	defer func() { _ = listener.Close() }()

	// Copy the config so the redirect URL we synthesize does not leak into a
	// config the caller may reuse.
	cfg := *a.Config
	cfg.RedirectURL = (&url.URL{
		Scheme: "http",
		Host:   listener.Addr().String(),
		Path:   callbackPath,
	}).String()

	state, err := randomState()
	if err != nil {
		return nil, err
	}
	verifier := oauth2.GenerateVerifier()

	authURL := cfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.ApprovalForce,
		oauth2.S256ChallengeOption(verifier),
	)

	results := make(chan callbackResult, 1)
	server := &http.Server{
		Handler:           a.callbackHandler(state, results),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case results <- callbackResult{err: fmt.Errorf("gmail: OAuth redirect server: %w", err)}:
			default:
			}
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	a.prompt(ctx, authURL)

	timeout := a.Timeout
	if timeout <= 0 {
		timeout = DefaultAuthTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var res callbackResult
	select {
	case res = <-results:
	case <-waitCtx.Done():
		if errors.Is(context.Cause(waitCtx), context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, fmt.Errorf("gmail: timed out after %s waiting for the browser redirect; "+
				"re-run `mail-muncher auth --account %s`", timeout, a.Account)
		}
		return nil, fmt.Errorf("gmail: authorization cancelled: %w", waitCtx.Err())
	}
	if res.err != nil {
		return nil, res.err
	}

	tok, err := cfg.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("gmail: exchange authorization code: %w", err)
	}
	if tok.RefreshToken == "" {
		// Without one, the grant dies with the access token in an hour. This
		// generally means the client is not a Desktop app client.
		slog.Warn("google returned no refresh token; unattended runs will stop working when the access token expires",
			"account", a.Account, "doc", SetupDoc)
	}

	if err := SaveToken(a.TokenFile, tok); err != nil {
		return nil, err
	}
	slog.Info("stored gmail token", "account", a.Account, "path", a.TokenFile)
	return tok, nil
}

// callbackResult is what the loopback handler hands back to Run.
type callbackResult struct {
	code string
	err  error
}

// callbackHandler serves the redirect Google sends the browser to. It delivers
// at most one result: later requests (a reload, a stray favicon fetch) get a
// short page and are otherwise ignored.
func (a *Authorizer) callbackHandler(state string, results chan<- callbackResult) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		var res callbackResult
		switch {
		case q.Get("error") != "":
			res.err = fmt.Errorf("gmail: authorization denied by Google: %s", authErrorText(q))
		case q.Get("state") != state:
			// Either a stale tab or a cross-site request; never exchange it.
			res.err = errors.New("gmail: authorization redirect had an unexpected state value; " +
				"re-run `mail-muncher auth` and use only the URL it prints")
		case q.Get("code") == "":
			res.err = errors.New("gmail: authorization redirect carried no code")
		default:
			res.code = q.Get("code")
		}

		select {
		case results <- res:
		default:
			// A result is already in flight; this is a duplicate request.
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if res.err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, failurePage, res.err.Error())
			return
		}
		fmt.Fprint(w, successPage)
	})
	return mux
}

const successPage = `<!doctype html><meta charset="utf-8"><title>mail-muncher</title>` +
	`<body style="font-family:system-ui,sans-serif;margin:4rem auto;max-width:32rem">` +
	`<h1>Authorized</h1><p>mail-muncher has your token. You can close this tab and return to the terminal.</p>`

const failurePage = `<!doctype html><meta charset="utf-8"><title>mail-muncher</title>` +
	`<body style="font-family:system-ui,sans-serif;margin:4rem auto;max-width:32rem">` +
	`<h1>Authorization failed</h1><p>%s</p><p>Return to the terminal and try again.</p>`

// authErrorText renders the OAuth error parameters Google redirects back with.
func authErrorText(q url.Values) string {
	msg := q.Get("error")
	if desc := q.Get("error_description"); desc != "" {
		msg += ": " + desc
	}
	return msg
}

// prompt prints the consent URL and best-effort opens a browser on it.
func (a *Authorizer) prompt(ctx context.Context, authURL string) {
	out := a.Out
	if out == nil {
		out = os.Stdout
	}

	fmt.Fprintf(out, "Authorizing Gmail account %q.\n", a.Account)
	fmt.Fprintf(out, "Open this URL in a browser and grant read-only access:\n\n%s\n\n", authURL)
	fmt.Fprintln(out, "Waiting for the browser to redirect back...")

	open := a.OpenBrowser
	if open == nil {
		open = openBrowser
	}
	if err := open(ctx, authURL); err != nil {
		slog.Debug("could not open a browser automatically", "error", err)
	}
}

// openBrowser asks the desktop to open url. It is best effort by design:
// headless machines and SSH sessions have no browser, and the URL is printed
// regardless.
func openBrowser(ctx context.Context, url string) error {
	var name string
	var args []string

	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		name = "xdg-open"
	}

	path, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("no browser opener: %w", err)
	}
	return exec.CommandContext(ctx, path, append(args, url)...).Start()
}

// randomState returns the anti-CSRF `state` value for one authorization.
func randomState() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("gmail: generate OAuth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
