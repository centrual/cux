package switcher

import "os"

// osMkdirAll mirrors os.MkdirAll but enforces 0700 — every directory
// cux creates is private to the user. Centralised here so callers can't
// accidentally create world-readable directories.
func osMkdirAll(path string) error {
	return os.MkdirAll(path, 0o700)
}
