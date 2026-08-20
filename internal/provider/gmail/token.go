package gmail

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/oauth2"
)

// File permissions for the token cache. The token file carries a long-lived
// refresh token — anyone who can read it can read the mailbox — so both the
// file and the directory we create for it are owner-only.
const (
	tokenFilePerm os.FileMode = 0o600
	tokenDirPerm  os.FileMode = 0o700
)

// ErrNoToken is returned when the account has no cached token yet, i.e. `auth`
// has never been run for it.
var ErrNoToken = errors.New("gmail: no cached token")

// ErrTokenRejected is returned when the OAuth server refuses to refresh the
// cached token: the refresh token was revoked, expired, or invalidated. The
// only fix is re-running `mail-muncher auth`.
var ErrTokenRejected = errors.New("gmail: stored token was rejected")

// LoadToken reads a cached OAuth token from path.
//
// A missing file yields an error matching ErrNoToken, which callers should
// translate into "run `mail-muncher auth`" rather than treating as a crash.
func LoadToken(path string) (*oauth2.Token, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNoToken, path)
		}
		return nil, fmt.Errorf("gmail: read token %s: %w", path, err)
	}

	var tok oauth2.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("gmail: parse token %s: %w", path, err)
	}
	if tok.AccessToken == "" && tok.RefreshToken == "" {
		return nil, fmt.Errorf("%w: %s holds no access or refresh token", ErrNoToken, path)
	}
	return &tok, nil
}

// SaveToken writes tok to path as JSON with mode 0600.
//
// The write is atomic: the JSON goes to a temp file in the same directory, is
// chmodded and fsynced, and is only then renamed over the destination. An
// interrupted write therefore leaves the previous token in place rather than a
// truncated file that would force the user to re-authenticate. The parent
// directory is created (0700) if missing.
func SaveToken(path string, tok *oauth2.Token) error {
	if tok == nil {
		return errors.New("gmail: refusing to save a nil token")
	}

	//nolint:gosec // G117: this is the token *cache* -- persisting the OAuth
	// token to disk is SaveToken's whole job, not an accidental leak. It is
	// written to tokenFilePerm (0600) in a tokenDirPerm (0700) directory,
	// which is the actual mitigation (see the type doc above and
	// CONTRIBUTING.md's "Tokens and state are written 0600...").
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return fmt.Errorf("gmail: encode token: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, tokenDirPerm); err != nil {
		return fmt.Errorf("gmail: create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("gmail: create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Both are no-ops once the rename below has succeeded.
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	// Chmod before writing: the temp file is created 0600 by CreateTemp on
	// every platform we target, but be explicit so the mode is not umask- or
	// platform-dependent.
	if err := tmp.Chmod(tokenFilePerm); err != nil {
		return fmt.Errorf("gmail: chmod %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("gmail: write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("gmail: sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("gmail: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("gmail: rename %s -> %s: %w", tmpName, path, err)
	}
	syncDir(dir)
	return nil
}

// syncDir flushes the directory entry so the rename survives a power loss.
// Best effort: not every platform or filesystem allows opening a directory.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer func() { _ = d.Close() }()
	_ = d.Sync()
}

// PersistingTokenSource wraps src so that every token it hands out is compared
// against the last one seen and written to path whenever it differs — which is
// exactly when the underlying oauth2 token source has performed a refresh.
//
// Persisting matters for more than saving a round trip: Google may rotate the
// refresh token during a refresh, and dropping the new one on the floor would
// eventually leave the account unable to refresh at all.
//
// A failed write is logged and swallowed: the caller has a perfectly good
// token in hand, and the worst case is another refresh next run. A failed
// refresh, on the other hand, is returned — annotated via classifyRefreshError
// so a revoked grant tells the user to re-run `auth`.
//
// The returned source is safe for concurrent use.
func PersistingTokenSource(src oauth2.TokenSource, path string, account string) oauth2.TokenSource {
	return &persistingTokenSource{src: src, path: path, account: account}
}

type persistingTokenSource struct {
	src     oauth2.TokenSource
	path    string
	account string

	mu   sync.Mutex
	last *oauth2.Token
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.src.Token()
	if err != nil {
		return nil, classifyRefreshError(err, p.account)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if sameToken(p.last, tok) {
		return tok, nil
	}
	if err := SaveToken(p.path, tok); err != nil {
		slog.Warn("could not persist refreshed gmail token",
			"account", p.account, "path", p.path, "error", err)
		// Do not record it as persisted, so the next call retries the write.
		return tok, nil
	}
	slog.Debug("persisted refreshed gmail token", "account", p.account, "path", p.path)
	p.last = tok
	return tok, nil
}

// sameToken reports whether two tokens are interchangeable for storage
// purposes. Only the fields we persist and that a refresh can change are
// compared.
func sameToken(a, b *oauth2.Token) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.AccessToken == b.AccessToken &&
		a.RefreshToken == b.RefreshToken &&
		a.TokenType == b.TokenType &&
		a.Expiry.Equal(b.Expiry)
}

// classifyRefreshError turns an oauth2 refresh failure into an actionable
// error. A token endpoint that answers "invalid_grant" (or any 4xx) has
// rejected the refresh token itself, which no amount of retrying fixes;
// anything else (a timeout, a 5xx, DNS) is transient and must not send the
// user off to re-authenticate.
func classifyRefreshError(err error, account string) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		rejected := re.ErrorCode == "invalid_grant" ||
			re.ErrorCode == "invalid_client" ||
			re.ErrorCode == "unauthorized_client" ||
			(re.Response != nil && re.Response.StatusCode >= 400 && re.Response.StatusCode < 500)
		if rejected {
			return fmt.Errorf("%w for account %q (revoked, expired, or the OAuth client changed): "+
				"re-run `mail-muncher auth --account %s`: %w", ErrTokenRejected, account, account, err)
		}
	}
	return fmt.Errorf("gmail: refresh token for account %q: %w", account, err)
}
