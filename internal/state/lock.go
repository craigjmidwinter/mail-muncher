package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// LockFileName is the shared cycle lock inside the state dir. Both `run` and
// `daemon` take it for the duration of a cycle so a cron run and a daemon can
// never race on the same state files.
const LockFileName = "mail-muncher.lock"

// ErrLocked reports that another process holds the cycle lock.
var ErrLocked = errors.New("state: lock held by another process")

// Lock is a held cycle lock. Release it with Unlock.
type Lock struct {
	fl *flock.Flock
}

// LockPath is the path of the store's cycle lock file.
func (s *Store) LockPath() string {
	return filepath.Join(s.dir, LockFileName)
}

// TryLock takes the cycle lock without waiting. It returns ErrLocked when
// another process already holds it.
func (s *Store) TryLock() (*Lock, error) {
	fl, err := s.newFlock()
	if err != nil {
		return nil, err
	}
	ok, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("state: lock %s: %w", fl.Path(), err)
	}
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrLocked, fl.Path())
	}
	return &Lock{fl: fl}, nil
}

// LockWait takes the cycle lock, retrying every retryDelay until it is
// acquired or ctx is done. A zero retryDelay means 100ms.
func (s *Store) LockWait(ctx context.Context, retryDelay time.Duration) (*Lock, error) {
	if retryDelay <= 0 {
		retryDelay = 100 * time.Millisecond
	}
	fl, err := s.newFlock()
	if err != nil {
		return nil, err
	}
	ok, err := fl.TryLockContext(ctx, retryDelay)
	if err != nil {
		return nil, fmt.Errorf("state: lock %s: %w", fl.Path(), err)
	}
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrLocked, fl.Path())
	}
	return &Lock{fl: fl}, nil
}

// Unlock releases the lock. It is safe to call more than once.
func (l *Lock) Unlock() error {
	if l == nil || l.fl == nil {
		return nil
	}
	fl := l.fl
	l.fl = nil
	if err := fl.Unlock(); err != nil {
		return fmt.Errorf("state: unlock %s: %w", fl.Path(), err)
	}
	return nil
}

// Path is the locked file's path.
func (l *Lock) Path() string {
	if l == nil || l.fl == nil {
		return ""
	}
	return l.fl.Path()
}

func (s *Store) newFlock() (*flock.Flock, error) {
	if err := os.MkdirAll(s.dir, dirPerm); err != nil {
		return nil, fmt.Errorf("state: create %s: %w", s.dir, err)
	}
	return flock.New(s.LockPath(), flock.SetPermissions(filePerm)), nil
}
