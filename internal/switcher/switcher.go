// Package switcher orchestrates the high-level operations on top of
// store, creds, and claudecfg: adding the currently-logged-in account,
// switching the active account, removing one. Each operation runs under
// the on-disk lock so two terminals cannot corrupt state.json by racing.
package switcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/inulute/cux/internal/claudecfg"
	"github.com/inulute/cux/internal/claudeconv"
	"github.com/inulute/cux/internal/codexcreds"
	"github.com/inulute/cux/internal/creds"
	"github.com/inulute/cux/internal/lockfile"
	"github.com/inulute/cux/internal/paths"
	"github.com/inulute/cux/internal/store"
)

const lockTimeout = 10 * time.Second

// AddCurrent reads the currently-logged-in account from Claude Code's
// live storage and registers it in cux. If the account is already
// managed, its credential and oauth backups are refreshed in place
// rather than rejected — this is the natural way to refresh a stale
// token (`claude login` again, then `cux add`).
func AddCurrent(preferredSlot int) (added store.Account, refreshed bool, err error) {
	if err := ensureBackupRoot(); err != nil {
		return store.Account{}, false, err
	}
	lk, err := lockfile.Acquire(paths.LockFile(), lockTimeout)
	if err != nil {
		return store.Account{}, false, err
	}
	defer lk.Unlock()

	state, err := store.Load()
	if err != nil {
		return store.Account{}, false, err
	}

	liveCreds, err := creds.ReadLive()
	if err != nil {
		if errors.Is(err, creds.ErrNotFound) {
			return store.Account{}, false, errors.New("no active Claude Code login found — run `claude login` first")
		}
		return store.Account{}, false, err
	}

	rawOAuth, parsed, err := claudecfg.ReadOAuthBlock()
	if err != nil {
		return store.Account{}, false, err
	}
	if parsed.EmailAddress == "" {
		return store.Account{}, false, errors.New("oauthAccount block has no emailAddress")
	}
	if err := store.ValidateEmail(parsed.EmailAddress); err != nil {
		return store.Account{}, false, err
	}

	if existing := state.FindByIdentity(parsed.EmailAddress, parsed.OrganizationUUID); existing != 0 {
		// Refresh: overwrite backups for this slot, no state shape change.
		acct := state.Accounts[existing]
		if err := creds.WriteBackup(existing, acct.Email, liveCreds); err != nil {
			return store.Account{}, false, err
		}
		if err := store.WriteOAuthBlockBackup(existing, acct.Email, rawOAuth); err != nil {
			return store.Account{}, false, err
		}
		acct.LastUsed = time.Now().UTC()
		state.Accounts[existing] = acct
		state.ActiveSlot = existing
		if err := state.Save(); err != nil {
			return store.Account{}, false, err
		}
		return acct, true, nil
	}

	slot := preferredSlot
	if slot <= 0 {
		slot = state.NextSlot()
	} else if _, taken := state.Accounts[slot]; taken {
		return store.Account{}, false, fmt.Errorf("slot %d already in use", slot)
	}

	if err := creds.WriteBackup(slot, parsed.EmailAddress, liveCreds); err != nil {
		return store.Account{}, false, err
	}
	if err := store.WriteOAuthBlockBackup(slot, parsed.EmailAddress, rawOAuth); err != nil {
		// Roll back the credential backup so we don't leave half an account on disk.
		_ = creds.DeleteBackup(slot, parsed.EmailAddress)
		return store.Account{}, false, err
	}
	if err := state.Add(slot, parsed.EmailAddress, parsed.AccountUUID, parsed.OrganizationUUID); err != nil {
		_ = creds.DeleteBackup(slot, parsed.EmailAddress)
		_ = store.DeleteOAuthBlockBackup(slot, parsed.EmailAddress)
		return store.Account{}, false, err
	}
	state.ActiveSlot = slot
	if err := state.Save(); err != nil {
		return store.Account{}, false, err
	}
	return state.Accounts[slot], false, nil
}

// SwitchTo activates the target account: backs up the current account's
// (possibly refreshed) credentials, then writes the target's credentials
// and oauthAccount block to Claude Code's live storage.
//
// The operation is staged: target backups are read and validated before
// any live state is touched, so a missing or corrupt backup aborts
// without disturbing the running login.
func SwitchTo(identifier string) (from, to store.Account, err error) {
	if err := ensureBackupRoot(); err != nil {
		return store.Account{}, store.Account{}, err
	}
	lk, err := lockfile.Acquire(paths.LockFile(), lockTimeout)
	if err != nil {
		return store.Account{}, store.Account{}, err
	}
	defer lk.Unlock()

	state, err := store.Load()
	if err != nil {
		return store.Account{}, store.Account{}, err
	}
	if len(state.Accounts) == 0 {
		return store.Account{}, store.Account{}, store.ErrEmptyState
	}

	target, err := state.Resolve(identifier)
	if err != nil {
		return store.Account{}, store.Account{}, err
	}

	// Stage: read target backups before touching anything live.
	targetCreds, err := creds.ReadBackup(target.Slot, target.Email)
	if err != nil {
		return store.Account{}, store.Account{}, fmt.Errorf("target credentials missing: %w", err)
	}
	targetOAuth, err := store.ReadOAuthBlockBackup(target.Slot, target.Email)
	if err != nil {
		return store.Account{}, store.Account{}, fmt.Errorf("target oauthAccount missing: %w", err)
	}

	// Refresh-backup of current live state. We always do this — the
	// access token may have rotated since the last add, and we don't
	// want to overwrite a fresher copy with a stale one.
	currentLive, liveErr := creds.ReadLive()
	currentRaw, currentParsed, cfgErr := claudecfg.ReadOAuthBlock()
	var current store.Account
	if liveErr == nil && cfgErr == nil {
		if slot := state.FindByIdentity(currentParsed.EmailAddress, currentParsed.OrganizationUUID); slot != 0 {
			current = state.Accounts[slot]
			if err := creds.WriteBackup(slot, current.Email, currentLive); err != nil {
				return store.Account{}, store.Account{}, fmt.Errorf("backing up current creds: %w", err)
			}
			if err := store.WriteOAuthBlockBackup(slot, current.Email, currentRaw); err != nil {
				return store.Account{}, store.Account{}, fmt.Errorf("backing up current oauth: %w", err)
			}
		}
		// If the live account isn't managed, we silently proceed — we
		// just won't have a backup for it. Better than failing to switch.
	}

	// Snapshot live state so we can restore on failure mid-write.
	rollbackCreds := currentLive
	rollbackOAuth := currentRaw

	if err := creds.WriteLive(targetCreds); err != nil {
		return store.Account{}, store.Account{}, fmt.Errorf("writing live credentials: %w", err)
	}
	if err := claudecfg.WriteOAuthBlock(targetOAuth); err != nil {
		// Best-effort rollback of the credential write.
		if rollbackCreds != "" {
			_ = creds.WriteLive(rollbackCreds)
		}
		if len(rollbackOAuth) > 0 {
			_ = claudecfg.WriteOAuthBlock(rollbackOAuth)
		}
		return store.Account{}, store.Account{}, fmt.Errorf("writing oauthAccount: %w", err)
	}

	target.LastUsed = time.Now().UTC()
	state.Accounts[target.Slot] = target
	state.ActiveSlot = target.Slot
	if err := state.Save(); err != nil {
		// State save failed but the live swap succeeded — surface this
		// loudly. The next cux run will re-derive active from .claude.json.
		return current, target, fmt.Errorf("swap complete but state save failed: %w", err)
	}
	return current, target, nil
}

// Remove unregisters an account and deletes its credential + oauth backups.
// Refuses to remove the currently-active account unless force is set.
func Remove(identifier string, force bool) (store.Account, error) {
	lk, err := lockfile.Acquire(paths.LockFile(), lockTimeout)
	if err != nil {
		return store.Account{}, err
	}
	defer lk.Unlock()

	state, err := store.Load()
	if err != nil {
		return store.Account{}, err
	}
	target, err := state.Resolve(identifier)
	if err != nil {
		return store.Account{}, err
	}
	if state.ActiveSlot == target.Slot && !force {
		return store.Account{}, fmt.Errorf("slot %d (%s) is currently active — pass --force to remove anyway", target.Slot, target.Email)
	}

	if err := creds.DeleteBackup(target.Slot, target.Email); err != nil {
		return store.Account{}, err
	}
	if err := store.DeleteOAuthBlockBackup(target.Slot, target.Email); err != nil {
		return store.Account{}, err
	}
	if err := state.Remove(target.Slot); err != nil {
		return store.Account{}, err
	}
	if err := state.Save(); err != nil {
		return store.Account{}, err
	}
	return target, nil
}

// CurrentLiveEmail returns the email of whichever account is currently
// logged in to Claude Code, regardless of whether it's managed by cux.
func CurrentLiveEmail() (string, error) {
	_, parsed, err := claudecfg.ReadOAuthBlock()
	if err != nil {
		return "", err
	}
	return parsed.EmailAddress, nil
}

// CurrentLiveCacheKey returns the usage-cache key for the currently active
// account. When the account has an organizationUuid that is used; otherwise
// the email is returned for backward compatibility.
func CurrentLiveCacheKey() (string, error) {
	_, parsed, err := claudecfg.ReadOAuthBlock()
	if err != nil {
		return "", err
	}
	acct := store.Account{Email: parsed.EmailAddress, OrgUUID: parsed.OrganizationUUID}
	return acct.CacheKey(), nil
}

// AddCodexCurrent reads the currently-authenticated Codex account from
// ~/.codex/auth.json and registers it in cux. If the account is already
// managed, its backup is refreshed in place.
func AddCodexCurrent(preferredSlot int) (added store.Account, refreshed bool, err error) {
	if err := ensureBackupRoot(); err != nil {
		return store.Account{}, false, err
	}
	lk, err := lockfile.Acquire(paths.LockFile(), lockTimeout)
	if err != nil {
		return store.Account{}, false, err
	}
	defer lk.Unlock()

	blob, accountID, err := codexcreds.ReadLive()
	if err != nil {
		if errors.Is(err, codexcreds.ErrNotFound) {
			return store.Account{}, false, errors.New("no active Codex login found — run `codex login` first")
		}
		return store.Account{}, false, err
	}

	state, err := store.Load()
	if err != nil {
		return store.Account{}, false, err
	}

	if existing := state.FindCodexSlot(accountID); existing != 0 {
		acct := state.Accounts[existing]
		if err := codexcreds.WriteBackup(existing, accountID, blob); err != nil {
			return store.Account{}, false, err
		}
		acct.LastUsed = time.Now().UTC()
		state.Accounts[existing] = acct
		if err := state.Save(); err != nil {
			return store.Account{}, false, err
		}
		return acct, true, nil
	}

	slot := preferredSlot
	if slot <= 0 {
		slot = state.NextSlot()
	} else if _, taken := state.Accounts[slot]; taken {
		return store.Account{}, false, fmt.Errorf("slot %d already in use", slot)
	}

	if err := codexcreds.WriteBackup(slot, accountID, blob); err != nil {
		return store.Account{}, false, err
	}
	if err := state.AddCodexAccount(slot, accountID); err != nil {
		_ = codexcreds.DeleteBackup(slot, accountID)
		return store.Account{}, false, err
	}
	if err := state.Save(); err != nil {
		return store.Account{}, false, err
	}
	return state.Accounts[slot], false, nil
}

// MigrateToCodex reads the active Claude Code session, writes the conversation
// to ~/.codex/memories/ as a markdown file (for fallback context injection) and
// also creates a native Codex session JSONL that can be resumed with
// `codex resume <codexSessionID>`. Both paths are returned; codexSessionID is
// empty if native session creation failed (non-fatal — memory file suffices).
func MigrateToCodex(cwd, sessionID string) (memoryFile, codexSessionID string, err error) {
	jsonlPath := claudeconv.SessionJSONLPath(cwd, sessionID)
	msgs, err := claudeconv.ExtractMessages(jsonlPath)
	if err != nil {
		return "", "", fmt.Errorf("switcher: read session: %w", err)
	}
	if len(msgs) == 0 {
		return "", "", fmt.Errorf("switcher: no messages found in session %s", sessionID)
	}

	memoriesDir := paths.CodexMemoriesDir()
	if err := os.MkdirAll(memoriesDir, 0o700); err != nil {
		return "", "", fmt.Errorf("switcher: mkdir memories: %w", err)
	}

	name := claudeconv.MemoryFileName()
	dest := filepath.Join(memoriesDir, name)
	content := claudeconv.FormatMemory(msgs, cwd, sessionID)
	if err := os.WriteFile(dest, []byte(content), 0o600); err != nil {
		return "", "", fmt.Errorf("switcher: write memory: %w", err)
	}

	// Also write a native Codex session — non-fatal if it fails.
	codexID, _, nErr := claudeconv.WriteNativeCodexSession(msgs, cwd)
	if nErr == nil {
		codexSessionID = codexID
	}

	return dest, codexSessionID, nil
}

// MigrateFromCodex reads the given Codex session, converts it to a native
// Claude Code session JSONL, and returns the new Claude session ID and the
// CWD from the original Codex session. The caller should launch
// `claude --resume <claudeSessionID>` in that CWD.
func MigrateFromCodex(codexSessionID string) (claudeSessionID, cwd string, err error) {
	jsonlPath, err := claudeconv.FindCodexSessionPath(codexSessionID)
	if err != nil {
		return "", "", fmt.Errorf("switcher: %w", err)
	}

	sourceCWD, msgs, err := claudeconv.ReadCodexSession(jsonlPath)
	if err != nil {
		return "", "", fmt.Errorf("switcher: read codex session: %w", err)
	}
	if len(msgs) == 0 {
		return "", "", fmt.Errorf("switcher: no messages found in codex session %s", codexSessionID)
	}

	if sourceCWD == "" {
		sourceCWD, _ = os.Getwd()
	}

	claudeID, _, err := claudeconv.WriteNativeClaudeSession(msgs, sourceCWD)
	if err != nil {
		return "", "", fmt.Errorf("switcher: write claude session: %w", err)
	}
	return claudeID, sourceCWD, nil
}

// CodexFallbackSlot returns the slot of the first registered Codex account,
// or 0 if none are registered. Used by the wrapper for overflow switching.
func CodexFallbackSlot() int {
	state, err := store.Load()
	if err != nil {
		return 0
	}
	for _, a := range state.Accounts {
		if a.IsCodex() {
			return a.Slot
		}
	}
	return 0
}

func ensureBackupRoot() error {
	if err := osMkdirAll(paths.BackupRoot()); err != nil {
		return err
	}
	if err := osMkdirAll(paths.AccountsDir()); err != nil {
		return err
	}
	if err := osMkdirAll(paths.RuntimeDir()); err != nil {
		return err
	}
	return nil
}
