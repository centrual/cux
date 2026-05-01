// Package monitor stitches store, creds, and usage together to keep
// the on-disk usage cache fresh.
//
// Call sites:
//   - The wrapper triggers RefreshActive(email) after each Stop signal
//     so the cache mirrors reality without flooding the API.
//   - The wrapper triggers RefreshAll() once at startup, in a
//     background goroutine, so threshold checks have something to
//     work with on the first turn.
//   - `cux usage refresh` and `cux list` call RefreshAll() on demand.
//
// In v0.2 there is no background polling daemon (deferred to v0.3
// alongside the systemd/launchd integration). All refreshes here are
// triggered, not periodic.
package monitor

import (
	"errors"
	"fmt"

	"github.com/inulute/cux/internal/creds"
	"github.com/inulute/cux/internal/store"
	"github.com/inulute/cux/internal/usage"
)

// RefreshAll fetches usage for every managed account and writes the
// merged cache to disk. Returns the resulting cache plus any per-account
// errors so the caller can surface them without aborting the whole
// refresh — one expired token shouldn't poison the others.
func RefreshAll() (usage.Cache, []error) {
	state, err := store.Load()
	if err != nil {
		return nil, []error{err}
	}
	cache, _ := usage.LoadCache()
	if cache == nil {
		cache = usage.Cache{}
	}
	var errs []error
	for slot, a := range state.Accounts {
		entry, err := refreshOne(slot, a.Email)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.Email, err))
			// On token-expired we still cache the marker so `cux list`
			// surfaces the state to the user.
			if entry.TokenExpired {
				cache[a.Email] = entry
			}
			continue
		}
		cache[a.Email] = entry
	}
	if err := usage.SaveCache(cache); err != nil {
		errs = append(errs, err)
	}
	return cache, errs
}

// RefreshActive refreshes one account by email. Used by the wrapper
// after each Stop signal to keep the active account's cache entry
// current before threshold evaluation.
func RefreshActive(email string) error {
	if email == "" {
		return errors.New("monitor: empty email")
	}
	state, err := store.Load()
	if err != nil {
		return err
	}
	slot := state.FindByEmail(email)
	if slot == 0 {
		return fmt.Errorf("monitor: %s not managed by cux", email)
	}
	entry, err := refreshOne(slot, email)
	cache, _ := usage.LoadCache()
	if cache == nil {
		cache = usage.Cache{}
	}
	if err != nil && !entry.TokenExpired {
		// Network blips shouldn't blow away the prior entry.
		return err
	}
	cache[email] = entry
	return usage.SaveCache(cache)
}

// refreshOne reads the account's stored credentials, extracts the
// bearer token, and queries the usage API. Errors at any stage are
// returned; the caller decides whether to keep going.
func refreshOne(slot int, email string) (usage.AccountUsage, error) {
	blob, err := creds.ReadBackup(slot, email)
	if err != nil {
		return usage.AccountUsage{}, err
	}
	token, err := creds.ExtractAccessToken(blob)
	if err != nil {
		return usage.AccountUsage{}, err
	}
	u, err := usage.Fetch(token)
	if err != nil {
		return u, err
	}
	return u, nil
}
