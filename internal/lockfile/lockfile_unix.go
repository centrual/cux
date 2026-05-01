//go:build !windows

package lockfile

import (
	"errors"
	"os"
	"syscall"
)

func tryLock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

func isWouldBlock(err error) bool {
	// EWOULDBLOCK and EAGAIN are the same value on every Unix; matching
	// either keeps the package portable across BSDs and Linux.
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
