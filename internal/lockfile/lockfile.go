// Package lockfile provides an advisory exclusive lock keyed to a file
// path. cux uses it to serialise state-modifying commands (add, remove,
// switch) so two terminals can't corrupt state.json by racing.
//
// The lock is advisory: only processes that try to acquire it cooperate.
// That's fine here — cux is the only writer of its own state directory.
package lockfile

import (
	"fmt"
	"os"
	"time"
)

// Lock is a held lock. Release with Unlock.
type Lock struct {
	f    *os.File
	path string
}

// Acquire opens path (creating it if needed) and takes an exclusive lock
// with the given timeout. The lock file itself stays empty; only its
// open file descriptor matters.
func Acquire(path string, timeout time.Duration) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lockfile: open %s: %w", path, err)
	}

	deadline := time.Now().Add(timeout)
	for {
		if err := tryLock(f); err == nil {
			return &Lock{f: f, path: path}, nil
		} else if !isWouldBlock(err) {
			_ = f.Close()
			return nil, fmt.Errorf("lockfile: lock %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("lockfile: timeout waiting for %s", path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Unlock releases the lock and closes the file. Safe to call once.
func (l *Lock) Unlock() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unlock(l.f)
	cerr := l.f.Close()
	l.f = nil
	if err != nil {
		return err
	}
	return cerr
}
