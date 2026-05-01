// Package creds reads and writes OAuth tokens for Claude Code.
//
// Two distinct storage roles:
//
//   - "Live" credentials are wherever Claude Code itself reads from.
//     cux must write here to actually change the active account.
//     macOS:     Keychain, generic password, service "Claude Code-credentials".
//     Linux/Win: File at ~/.claude/.credentials.json, mode 0600.
//
//   - "Backup" credentials are cux's per-account stash. On macOS/Windows
//     they live in the OS keystore under our own service name "cux-backup"
//     so they're encrypted at rest by the OS. On Linux there is no
//     guaranteed keystore daemon, so we fall back to 0600-mode files
//     under our backup directory (the same trade-off cc-account-switcher
//     and claude-swap make).
//
// Tokens are opaque strings to most callers. The one exception is
// `ExtractAccessToken`, a tiny convenience that pulls the OAuth bearer
// out of a blob so the wrapper can call the usage API without
// re-implementing the parse in two places.
package creds

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/zalando/go-keyring"

	"github.com/inulute/cux/internal/atomicfile"
	"github.com/inulute/cux/internal/paths"
)

// macKeychainService is the service name Claude Code itself uses on macOS.
// We read and write under this exact string so a fresh `claude login`
// followed by `cux add` finds the credentials in the expected place.
const macKeychainService = "Claude Code-credentials"

// backupKeyringService is cux's own namespace inside the OS keystore on
// macOS/Windows. Distinct from claude-swap's "claude-code" so a user who
// has both tools installed sees no overlap.
const backupKeyringService = "cux-backup"

// ErrNotFound is returned by ReadLive when no live credentials exist
// (user never logged in, or just logged out).
var ErrNotFound = errors.New("creds: live credentials not found")

// ReadLive returns the active credential blob Claude Code is currently
// using. The format is an opaque JSON string — we don't parse it.
func ReadLive() (string, error) {
	if runtime.GOOS == "darwin" {
		return readLiveMacOS()
	}
	return readLiveFile()
}

// WriteLive replaces the live credential blob Claude Code reads.
// On macOS it goes to the keychain; on Linux/Windows it goes to the
// file at ~/.claude/.credentials.json with mode 0600.
func WriteLive(blob string) error {
	if blob == "" {
		return errors.New("creds: refusing to write empty live credentials")
	}
	if runtime.GOOS == "darwin" {
		return writeLiveMacOS(blob)
	}
	return writeLiveFile(blob)
}

// ReadBackup returns the saved credential blob for one account, or
// ErrNotFound if there is no backup for it.
func ReadBackup(slot int, email string) (string, error) {
	if runtime.GOOS == "linux" {
		return readBackupFile(slot, email)
	}
	return readBackupKeyring(slot, email)
}

// WriteBackup saves the credential blob for one account.
func WriteBackup(slot int, email, blob string) error {
	if blob == "" {
		return errors.New("creds: refusing to write empty backup credentials")
	}
	if runtime.GOOS == "linux" {
		return writeBackupFile(slot, email, blob)
	}
	return writeBackupKeyring(slot, email, blob)
}

// ExtractAccessToken pulls the OAuth bearer token out of a credentials
// blob (the same JSON shape Claude Code writes to .credentials.json).
// Returns ErrNotFound if the blob is missing the expected field.
//
// The token is never logged; this helper does not surface it in any
// error message that propagates out of the package.
func ExtractAccessToken(blob string) (string, error) {
	if blob == "" {
		return "", ErrNotFound
	}
	var doc struct {
		ClaudeAIOAuth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal([]byte(blob), &doc); err != nil {
		return "", fmt.Errorf("creds: parse blob: %w", err)
	}
	if doc.ClaudeAIOAuth.AccessToken == "" {
		return "", ErrNotFound
	}
	return doc.ClaudeAIOAuth.AccessToken, nil
}

// DeleteBackup removes the saved credential blob for one account.
// Missing entries are not an error — deletion is idempotent.
func DeleteBackup(slot int, email string) error {
	if runtime.GOOS == "linux" {
		return deleteBackupFile(slot, email)
	}
	return deleteBackupKeyring(slot, email)
}

// --- macOS live (security CLI) --------------------------------------------

// We shell out to `security` rather than going through go-keyring so we
// inherit Claude Code's exact keychain semantics (single-line generic
// password, no extra metadata) and don't risk the Go library prompting
// the user for keychain access on every read.

func readLiveMacOS() (string, error) {
	cmd := exec.Command("security", "find-generic-password",
		"-s", macKeychainService, "-w")
	out, err := cmd.Output()
	if err != nil {
		// `security` returns exit 44 when the item isn't found.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("creds: security find: %w", err)
	}
	return trimTrailingNewline(string(out)), nil
}

func writeLiveMacOS(blob string) error {
	user := os.Getenv("USER")
	cmd := exec.Command("security", "add-generic-password",
		"-U", // update if already present
		"-s", macKeychainService,
		"-a", user,
		"-w", blob,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("creds: security add: %w (%s)", err, out)
	}
	return nil
}

// --- Linux/Windows live (file) --------------------------------------------

func readLiveFile() (string, error) {
	b, err := os.ReadFile(paths.ClaudeCredentials())
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("creds: read %s: %w", paths.ClaudeCredentials(), err)
	}
	return string(b), nil
}

func writeLiveFile(blob string) error {
	if err := os.MkdirAll(paths.ClaudeDir(), 0o700); err != nil {
		return fmt.Errorf("creds: mkdir %s: %w", paths.ClaudeDir(), err)
	}
	return atomicfile.Write(paths.ClaudeCredentials(), []byte(blob), 0o600)
}

// --- Backup: keyring (macOS/Windows) --------------------------------------

func backupKeyringUser(slot int, email string) string {
	// Mirror cc-account-switcher / claude-swap convention so the data
	// shape is recognisable to a user who switches tools, but under our
	// own service name to avoid actual collisions.
	return fmt.Sprintf("account-%d-%s", slot, email)
}

func readBackupKeyring(slot int, email string) (string, error) {
	v, err := keyring.Get(backupKeyringService, backupKeyringUser(slot, email))
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("creds: keyring get: %w", err)
	}
	return v, nil
}

func writeBackupKeyring(slot int, email, blob string) error {
	if err := keyring.Set(backupKeyringService, backupKeyringUser(slot, email), blob); err != nil {
		return fmt.Errorf("creds: keyring set: %w", err)
	}
	return nil
}

func deleteBackupKeyring(slot int, email string) error {
	err := keyring.Delete(backupKeyringService, backupKeyringUser(slot, email))
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("creds: keyring delete: %w", err)
	}
	return nil
}

// --- Backup: file (Linux) -------------------------------------------------

func backupFilePath(slot int, email string) string {
	return filepath.Join(paths.AccountDir(slot, email), "credentials.json")
}

func readBackupFile(slot int, email string) (string, error) {
	b, err := os.ReadFile(backupFilePath(slot, email))
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("creds: read backup: %w", err)
	}
	return string(b), nil
}

func writeBackupFile(slot int, email, blob string) error {
	dir := paths.AccountDir(slot, email)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creds: mkdir %s: %w", dir, err)
	}
	return atomicfile.Write(backupFilePath(slot, email), []byte(blob), 0o600)
}

func deleteBackupFile(slot int, email string) error {
	err := os.Remove(backupFilePath(slot, email))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("creds: remove backup: %w", err)
	}
	return nil
}

func trimTrailingNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
