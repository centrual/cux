//go:build !windows

package registry

import (
	"os"
	"syscall"
)

// processAlive probes pid with the null signal. EPERM means the process
// exists but belongs to another user — alive, not reapable.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return err == syscall.EPERM
}
