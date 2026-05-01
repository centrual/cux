package wrapper

// SlashSwitch implements `cux __slash-switch <target>`, the body the
// /switch slash command shells out to.
//
// In v0.1 this scheduled a 1-second SIGTERM directly. In v0.2 it just
// writes a switch-requested signal — the wrapper's poll loop picks it
// up, defers the actual exit until the next Stop hook fires (which
// guarantees the transcript has been flushed), and then performs the
// swap. The user-visible cadence is similar (~2 second blip), but
// there's no SIGTERM-mid-turn race to caveat any more.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/inulute/cux/internal/signals"
	"github.com/inulute/cux/internal/store"
)

// SlashSwitch is invoked by the slash command's bash block. target may
// be empty — that means "rotate per the configured strategy", which
// the wrapper resolves once it sees the signal.
func SlashSwitch(target string, w io.Writer) error {
	if os.Getenv(envWrapped) != "1" {
		return errors.New("/switch requires cux as the entry point — start your session with `cux` instead of `claude`")
	}

	pidStr := os.Getenv(envWrapperPID)
	if pidStr == "" {
		return errors.New("CUX_WRAPPER_PID not set; cannot route switch")
	}
	wrapperPID, err := strconv.Atoi(pidStr)
	if err != nil || wrapperPID <= 0 {
		return fmt.Errorf("invalid CUX_WRAPPER_PID: %q", pidStr)
	}

	target = strings.TrimSpace(target)

	// Validate now so the user gets immediate feedback rather than a
	// silent failure 100ms later when the wrapper rejects the target.
	state, err := store.Load()
	if err != nil {
		return err
	}
	if len(state.Accounts) < 2 {
		return errors.New("need at least two managed accounts — run `cux add` after logging into another account")
	}
	if target != "" {
		resolved, err := state.Resolve(target)
		if err != nil {
			return err
		}
		if resolved.Slot == state.ActiveSlot {
			fmt.Fprintf(w, "cux: already on %s, nothing to do\n", resolved.Email)
			return nil
		}
		fmt.Fprintf(w, "Switching to %s — reconnecting after this turn ends.\n", resolved.Email)
	} else {
		fmt.Fprintln(w, "Rotating to next account — reconnecting after this turn ends.")
	}

	return signals.Write(wrapperPID, signals.SwitchRequested, signals.SwitchRequestedPayload{
		Target:    target,
		Timestamp: time.Now().UTC(),
	})
}
