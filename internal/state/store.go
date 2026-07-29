package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/craigjmidwinter/mail-muncher/internal/provider"
)

// File permissions: state can reveal which accounts exist and which message ids
// were seen, so keep it owner-only.
const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

// ErrInvalidAccount is returned for account names that cannot be turned into a
// safe filename (empty, or containing a path separator or "..").
var ErrInvalidAccount = errors.New("state: invalid account name")

// Store persists one provider.SyncState per account as JSON at
// <dir>/<account>.json. Writes are atomic (temp file + rename), so a crashed or
// killed run never leaves a half-written state file behind.
type Store struct {
	dir string
}

// NewStore returns a Store rooted at dir. The directory is created lazily on
// the first Save.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Dir returns the store's root directory.
func (s *Store) Dir() string { return s.dir }

// Path returns the state file path for an account.
func (s *Store) Path(account string) (string, error) {
	if err := validAccount(account); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, account+".json"), nil
}

// Load reads an account's state. A missing file is not an error: it yields the
// zero SyncState, which every provider reads as "never synced".
func (s *Store) Load(account string) (provider.SyncState, error) {
	var st provider.SyncState

	path, err := s.Path(account)
	if err != nil {
		return st, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return provider.SyncState{}, nil
		}
		return st, fmt.Errorf("state: read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return provider.SyncState{}, fmt.Errorf("state: parse %s: %w", path, err)
	}
	st.SeenIDs = provider.TrimSeenIDs(st.SeenIDs)
	return st, nil
}

// Save writes an account's state atomically: the JSON goes to a temp file in
// the same directory, is fsynced, and is then renamed over the destination. A
// failure anywhere before the rename leaves the previous state file untouched
// and removes the temp file.
//
// The seen-id set is trimmed to provider.MaxSeenIDs on the way out, so a
// caller that appended without bound cannot grow the file forever.
func (s *Store) Save(account string, st provider.SyncState) error {
	path, err := s.Path(account)
	if err != nil {
		return err
	}
	st.SeenIDs = provider.TrimSeenIDs(st.SeenIDs)

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("state: encode %s: %w", account, err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(s.dir, dirPerm); err != nil {
		return fmt.Errorf("state: create %s: %w", s.dir, err)
	}
	return writeFileAtomic(path, data, filePerm)
}

// Delete removes an account's state file. A missing file is not an error.
func (s *Store) Delete(account string) error {
	path, err := s.Path(account)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("state: remove %s: %w", path, err)
	}
	return nil
}

// writeFileAtomic writes data to a temp file in the destination's directory,
// fsyncs it, and renames it into place.
//
// writeAndSync is a package-level indirection so tests can simulate a failure
// partway through the write and assert nothing partial escapes.
var writeAndSync = func(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("state: create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		// No-op once the rename has succeeded.
		tmp.Close()
		os.Remove(tmpName)
	}()

	if err := writeAndSync(tmp, data); err != nil {
		return fmt.Errorf("state: write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fmt.Errorf("state: chmod %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("state: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("state: rename %s -> %s: %w", tmpName, path, err)
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
	defer d.Close()
	_ = d.Sync()
}

func validAccount(account string) error {
	switch {
	case account == "":
		return fmt.Errorf("%w: empty", ErrInvalidAccount)
	case account == "." || account == "..":
		return fmt.Errorf("%w: %q", ErrInvalidAccount, account)
	case strings.ContainsRune(account, os.PathSeparator),
		strings.ContainsRune(account, '/'),
		strings.ContainsRune(account, filepath.Separator):
		return fmt.Errorf("%w: %q contains a path separator", ErrInvalidAccount, account)
	case strings.Contains(account, ".."):
		return fmt.Errorf("%w: %q contains %q", ErrInvalidAccount, account, "..")
	case strings.ContainsRune(account, 0):
		return fmt.Errorf("%w: %q contains NUL", ErrInvalidAccount, account)
	}
	return nil
}
