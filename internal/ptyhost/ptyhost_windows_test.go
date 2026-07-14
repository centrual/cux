//go:build windows

package ptyhost

import (
	"os"
	"path/filepath"
	"testing"
)

// TestConPTYRelaunch is the Windows counterpart to the Unix relaunch
// test: three back-to-back children on one ConPTY (the rate-limit swap
// path) must all start, run, and exit — the master (Pump) stays alive
// because the ConPTY outlives individual processes.
func TestConPTYRelaunch(t *testing.T) {
	h, err := New(filepath.Join(t.TempDir(), "s.sock"), true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer h.Close()
	go h.Pump()
	for i := 0; i < 3; i++ {
		ch, err := h.StartAttached(`C:\windows\system32\cmd.exe`, []string{"/c", "echo ok"}, os.Environ())
		if err != nil {
			t.Fatalf("launch %d: start: %v", i, err)
		}
		if err := ch.Wait(); err != nil {
			t.Fatalf("launch %d: wait: %v", i, err)
		}
	}
}
