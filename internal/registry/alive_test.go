package registry

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
)

// spawnDead runs a trivial process to completion and returns its (now
// dead) PID, portably: `true` doesn't exist on Windows.
func spawnDead(t *testing.T) int {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "exit 0")
	} else {
		cmd = exec.Command("true")
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	return pid
}

// TestProcessAliveSelf pins the probe's positive case on every platform.
// On Windows this is the regression test for the Signal(0) probe, which
// always errored there — reporting the calling process itself as dead
// and making List reap live wrappers' entries.
func TestProcessAliveSelf(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("processAlive(self) = false, want true")
	}
}

func TestProcessAliveDead(t *testing.T) {
	if pid := spawnDead(t); processAlive(pid) {
		t.Errorf("processAlive(%d) = true for an exited process, want false", pid)
	}
}
