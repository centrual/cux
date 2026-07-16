//go:build windows

package registry

import (
	"errors"

	"golang.org/x/sys/windows"
)

// stillActive is GetExitCodeProcess's "hasn't exited" sentinel
// (STILL_ACTIVE, 259) — x/sys/windows doesn't export it.
const stillActive = 259

// processAlive reports whether pid is a running process. The Unix
// probe — Signal(0) — always errors with "not supported by windows"
// here, which List misread as "dead" and reaped every entry, live ones
// included. Open the process and ask for its exit code instead:
// STILL_ACTIVE means running. An access-denied open means the process
// exists but is out of reach — alive, mirroring the Unix EPERM case —
// and an unqueryable handle is kept too: reaping must only ever act on
// certainty.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return true
	}
	return code == stillActive
}
