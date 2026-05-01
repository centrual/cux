// Package atomicfile writes files in a way that survives crashes and
// concurrent readers. The contract: a reader either sees the old content
// or the new content, never a partial write.
//
// Approach: write to a temp file in the same directory, fsync it, then
// rename over the destination. Same-directory rename is atomic on every
// filesystem cux targets. fsync before rename means a power loss after
// the rename leaves the new content fully on disk, not zero-length.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write writes data to path with the given mode, atomically.
//
// The temp file is created in the same directory as the destination so
// the rename stays on the same filesystem. If the destination directory
// does not exist, Write returns an error rather than creating it — the
// caller is expected to set up directories explicitly with the right
// permissions.
func Write(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("atomicfile: parent dir not ready: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".cux-tmp-*")
	if err != nil {
		return fmt.Errorf("atomicfile: create temp: %w", err)
	}
	tmpName := tmp.Name()

	// Best-effort cleanup. After a successful rename, removing tmpName
	// is a no-op (it no longer exists under that name); after a failure
	// it's the right thing to do.
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("atomicfile: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("atomicfile: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("atomicfile: close: %w", err)
	}
	// Set perms before rename so the file is never visible at the
	// destination with the temp file's default mode.
	if err := os.Chmod(tmpName, mode); err != nil {
		cleanup()
		return fmt.Errorf("atomicfile: chmod: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("atomicfile: rename: %w", err)
	}
	return nil
}
