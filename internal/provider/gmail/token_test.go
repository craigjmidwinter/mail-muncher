package gmail

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// fakeTokenSource is a scripted oauth2.TokenSource: no network, no clock.
type fakeTokenSource struct {
	mu     sync.Mutex
	tokens []*oauth2.Token
	err    error
	calls  int
}

func (f *fakeTokenSource) Token() (*oauth2.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if len(f.tokens) == 0 {
		return nil, errors.New("fakeTokenSource: exhausted")
	}
	tok := f.tokens[0]
	if len(f.tokens) > 1 {
		f.tokens = f.tokens[1:]
	}
	return tok, nil
}

func (f *fakeTokenSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func testToken(access string, expiry time.Time) *oauth2.Token {
	return &oauth2.Token{
		AccessToken:  access,
		RefreshToken: "refresh-1",
		TokenType:    "Bearer",
		Expiry:       expiry,
	}
}

func TestSaveTokenWritesOwnerOnlyFile(t *testing.T) {
	// The token file is written into a directory that does not exist yet, the
	// normal case on a first `auth`.
	path := filepath.Join(t.TempDir(), "nested", "token.json")
	tok := testToken("access-1", time.Now().Add(time.Hour).Round(time.Second))

	require.NoError(t, SaveToken(path, tok))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "token file must be owner-only")

	dirInfo, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm(), "created token dir must be owner-only")

	got, err := LoadToken(path)
	require.NoError(t, err)
	require.Equal(t, tok.AccessToken, got.AccessToken)
	require.Equal(t, tok.RefreshToken, got.RefreshToken)
	require.Equal(t, tok.TokenType, got.TokenType)
	require.True(t, tok.Expiry.Equal(got.Expiry), "expiry %s != %s", tok.Expiry, got.Expiry)
}

func TestSaveTokenIsAtomicAndLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token.json")

	require.NoError(t, SaveToken(path, testToken("access-1", time.Now().Add(time.Hour))))
	require.NoError(t, SaveToken(path, testToken("access-2", time.Now().Add(2*time.Hour))))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "temp files must not survive a successful write")
	require.Equal(t, "token.json", entries[0].Name())

	got, err := LoadToken(path)
	require.NoError(t, err)
	require.Equal(t, "access-2", got.AccessToken, "the second write must have replaced the first")

	// Permissions survive an overwrite (rename replaces the inode, so the mode
	// comes from the temp file, not the old destination).
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestSaveTokenFailureLeavesPreviousTokenIntact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not block writes")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "token.json")
	require.NoError(t, SaveToken(path, testToken("good", time.Now().Add(time.Hour))))

	// Make the directory unwritable so the temp-file step fails.
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := SaveToken(path, testToken("clobbered", time.Now().Add(time.Hour)))
	require.Error(t, err)

	got, err := LoadToken(path)
	require.NoError(t, err)
	require.Equal(t, "good", got.AccessToken, "a failed write must not damage the stored token")
}

func TestSaveTokenRejectsNil(t *testing.T) {
	require.Error(t, SaveToken(filepath.Join(t.TempDir(), "token.json"), nil))
}

func TestLoadTokenMissingFile(t *testing.T) {
	_, err := LoadToken(filepath.Join(t.TempDir(), "absent.json"))
	require.ErrorIs(t, err, ErrNoToken)
}

func TestLoadTokenRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o600))

	_, err := LoadToken(path)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNoToken, "a corrupt file is not the same as a missing one")
}

func TestLoadTokenRejectsEmptyToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"token_type":"Bearer"}`), 0o600))

	_, err := LoadToken(path)
	require.ErrorIs(t, err, ErrNoToken)
}

func TestPersistingTokenSourcePersistsRefreshedToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	first := testToken("access-1", time.Now().Add(time.Hour).Round(time.Second))
	second := &oauth2.Token{
		AccessToken:  "access-2",
		RefreshToken: "refresh-2", // Google rotated the refresh token.
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(2 * time.Hour).Round(time.Second),
	}
	fake := &fakeTokenSource{tokens: []*oauth2.Token{first, second}}

	src := PersistingTokenSource(fake, path, "work")

	got, err := src.Token()
	require.NoError(t, err)
	require.Equal(t, "access-1", got.AccessToken)

	stored, err := LoadToken(path)
	require.NoError(t, err)
	require.Equal(t, "access-1", stored.AccessToken)

	got, err = src.Token()
	require.NoError(t, err)
	require.Equal(t, "access-2", got.AccessToken)

	stored, err = LoadToken(path)
	require.NoError(t, err)
	require.Equal(t, "access-2", stored.AccessToken)
	require.Equal(t, "refresh-2", stored.RefreshToken, "a rotated refresh token must be persisted")
}

func TestPersistingTokenSourceSkipsUnchangedToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	tok := testToken("access-1", time.Now().Add(time.Hour).Round(time.Second))
	fake := &fakeTokenSource{tokens: []*oauth2.Token{tok}}

	src := PersistingTokenSource(fake, path, "work")

	_, err := src.Token()
	require.NoError(t, err)
	require.FileExists(t, path)

	// Removing the file is the cheapest way to observe a write: if the wrapper
	// wrote on every call rather than only on change, it would reappear.
	require.NoError(t, os.Remove(path))

	_, err = src.Token()
	require.NoError(t, err)
	require.NoFileExists(t, path, "an unchanged token must not be rewritten")
}

func TestPersistingTokenSourceRetriesAfterFailedWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not block writes")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "token.json")
	tok := testToken("access-1", time.Now().Add(time.Hour).Round(time.Second))
	fake := &fakeTokenSource{tokens: []*oauth2.Token{tok}}
	src := PersistingTokenSource(fake, path, "work")

	require.NoError(t, os.Chmod(dir, 0o500))

	// A persistence failure must not fail the caller's request for a token.
	got, err := src.Token()
	require.NoError(t, err)
	require.Equal(t, "access-1", got.AccessToken)
	require.NoFileExists(t, path)

	require.NoError(t, os.Chmod(dir, 0o700))

	_, err = src.Token()
	require.NoError(t, err)
	require.FileExists(t, path, "a token that failed to persist must be retried")
}

func TestPersistingTokenSourceReportsRejectedRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	fake := &fakeTokenSource{err: &oauth2.RetrieveError{
		Response:  &http.Response{StatusCode: http.StatusBadRequest},
		ErrorCode: "invalid_grant",
	}}

	_, err := PersistingTokenSource(fake, path, "work").Token()
	require.ErrorIs(t, err, ErrTokenRejected)
	require.Contains(t, err.Error(), "mail-muncher auth --account work",
		"a revoked grant must tell the user exactly what to re-run")
}

func TestPersistingTokenSourceKeepsTransientErrorsTransient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")

	cases := map[string]error{
		"network":     errors.New("dial tcp: connection refused"),
		"server side": &oauth2.RetrieveError{Response: &http.Response{StatusCode: http.StatusServiceUnavailable}},
	}
	for name, srcErr := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := PersistingTokenSource(&fakeTokenSource{err: srcErr}, path, "work").Token()
			require.Error(t, err)
			require.NotErrorIs(t, err, ErrTokenRejected,
				"a transient failure must not send the user off to re-authenticate")
		})
	}
}

func TestPersistingTokenSourceIsConcurrencySafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	tok := testToken("access-1", time.Now().Add(time.Hour).Round(time.Second))
	fake := &fakeTokenSource{tokens: []*oauth2.Token{tok}}
	src := PersistingTokenSource(fake, path, "work")

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := src.Token()
			require.NoError(t, err)
		}()
	}
	wg.Wait()

	require.Equal(t, 16, fake.callCount())
	stored, err := LoadToken(path)
	require.NoError(t, err)
	require.Equal(t, "access-1", stored.AccessToken)
}
