//go:build !windows

package ptyhost

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestMasterSurvivesChildExit locks in the Setctty fix: after a child on
// the slave exits, the master (ptmx) must still be alive (a Read blocks
// waiting for the next launch) rather than returning EOF. If it returned
// EOF, Pump() would exit and the next relaunch (a rate-limit resume)
// would hang — exactly the "resuming on …" freeze this guards against.
func TestMasterSurvivesChildExit(t *testing.T) {
	h, err := New(filepath.Join(t.TempDir(), "s.sock"), false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer h.Close()

	tty, err := h.TTYDup()
	if err != nil {
		t.Fatalf("dup: %v", err)
	}
	cmd := exec.Command("true")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = SysProcAttr()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = cmd.Wait()
	_ = tty.Close()

	type res struct {
		n   int
		err error
	}
	ch := make(chan res, 1)
	go func() {
		buf := make([]byte, 64)
		n, e := h.ptmx.Read(buf)
		ch <- res{n, e}
	}()
	select {
	case r := <-ch:
		t.Fatalf("master.Read returned (%d, %v) after child exit — Pump would die and relaunch would hang; Setctty must stay off", r.n, r.err)
	case <-time.After(700 * time.Millisecond):
		// Still blocked → master alive → Pump survives → relaunch works.
	}
}