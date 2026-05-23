// Package codexcreds reads and writes authentication for the Codex CLI.
//
// Codex stores its live credentials at ~/.codex/auth.json. cux backs them
// up per-account inside its own data directory, mirroring what the creds
// package does for Claude Code credentials.
package codexcreds

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/inulute/cux/internal/atomicfile"
	"github.com/inulute/cux/internal/paths"
)

// ErrNotFound is returned when no Codex credentials exist.
var ErrNotFound = errors.New("codexcreds: auth.json not found")

// authDoc is the shape of ~/.codex/auth.json.
type authDoc struct {
	AuthMode    string     `json:"auth_mode"`
	Tokens      authTokens `json:"tokens"`
	LastRefresh string     `json:"last_refresh,omitempty"`
}

type authTokens struct {
	AccountID    string `json:"account_id"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
}

// ReadLive reads ~/.codex/auth.json and returns the raw blob and the
// account_id extracted from it.
func ReadLive() (blob string, accountID string, err error) {
	b, err := os.ReadFile(paths.CodexAuthFile())
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", ErrNotFound
		}
		return "", "", fmt.Errorf("codexcreds: read %s: %w", paths.CodexAuthFile(), err)
	}
	var doc authDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", "", fmt.Errorf("codexcreds: parse auth.json: %w", err)
	}
	if doc.Tokens.AccountID == "" {
		return "", "", fmt.Errorf("codexcreds: auth.json has no tokens.account_id")
	}
	return string(b), doc.Tokens.AccountID, nil
}

// backupPath returns the per-slot backup path for a Codex credential blob.
func backupPath(slot int, accountID string) string {
	short := accountID
	if len(short) > 8 {
		short = short[:8]
	}
	return filepath.Join(paths.AccountDir(slot, "codex:"+short), "codex-auth.json")
}

// WriteBackup saves the Codex auth blob for one slot.
func WriteBackup(slot int, accountID, blob string) error {
	if blob == "" {
		return errors.New("codexcreds: refusing to write empty backup")
	}
	dir := filepath.Dir(backupPath(slot, accountID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("codexcreds: mkdir %s: %w", dir, err)
	}
	return atomicfile.Write(backupPath(slot, accountID), []byte(blob), 0o600)
}

// ReadBackup loads the saved Codex auth blob for one slot.
func ReadBackup(slot int, accountID string) (string, error) {
	b, err := os.ReadFile(backupPath(slot, accountID))
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("codexcreds: read backup: %w", err)
	}
	return string(b), nil
}

// RestoreLive writes the backed-up auth blob back to ~/.codex/auth.json.
func RestoreLive(slot int, accountID string) error {
	blob, err := ReadBackup(slot, accountID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(paths.CodexAuthFile()), 0o700); err != nil {
		return fmt.Errorf("codexcreds: mkdir codex dir: %w", err)
	}
	return atomicfile.Write(paths.CodexAuthFile(), []byte(blob), 0o600)
}

// DeleteBackup removes the backup for one slot. Missing entries are not an error.
func DeleteBackup(slot int, accountID string) error {
	err := os.Remove(backupPath(slot, accountID))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("codexcreds: remove backup: %w", err)
	}
	return nil
}
