//go:build !windows

package ptyhost

import (
	"bytes"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAttachRoundTrip runs `cat` on the host PTY, attaches over the
// Unix socket, types through the socket and expects the echo back —
// the full remote-input path in one test.
func TestAttachRoundTrip(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "a.sock")
	h, err := New(sock, true)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	go h.Pump()

	cmd := exec.Command("/bin/cat")
	tty := h.TTY()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = SysProcAttr()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := WriteFrame(conn, FrameResize, []byte{0, 30, 0, 100}); err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(conn, FrameInput, []byte("merhaba-cux\r")); err != nil {
		t.Fatal(err)
	}

	// Collect output frames until the echo shows up (tty echo + cat both
	// repeat the line, either counts).
	var got bytes.Buffer
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		typ, payload, err := ReadFrame(conn)
		if err == nil && typ == FrameOut {
			got.Write(payload)
			if strings.Contains(got.String(), "merhaba-cux") {
				return
			}
		}
	}
	t.Fatalf("echo did not arrive; got %q", got.String())
}

// TestInputGate verifies that a view-only host drops client keystrokes.
func TestInputGate(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "g.sock")
	h, err := New(sock, false) // input disabled
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	go h.Pump()

	cmd := exec.Command("/bin/cat")
	tty := h.TTY()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = SysProcAttr()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := WriteFrame(conn, FrameInput, []byte("sizmamali\r")); err != nil {
		t.Fatal(err)
	}

	var got bytes.Buffer
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		typ, payload, err := ReadFrame(conn)
		if err == nil && typ == FrameOut {
			got.Write(payload)
		}
	}
	if strings.Contains(got.String(), "sizmamali") {
		t.Fatalf("input leaked through a view-only host: %q", got.String())
	}
}
