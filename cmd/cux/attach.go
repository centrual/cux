//go:build !windows

package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/inulute/cux/internal/paths"
	"github.com/inulute/cux/internal/ptyhost"
	"github.com/inulute/cux/internal/registry"
	"golang.org/x/term"
)

const detachKey = 0x1c // Ctrl+\

// cmdAttach mirrors a running cux session into this terminal — the
// tmux-attach experience without tmux. With no argument it attaches to
// the only attachable session; with one it takes the wrapper PID shown
// by `cux sessions`.
func cmdAttach(args []string) int {
	pid, err := pickSession(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cux:", err)
		return 1
	}
	conn, err := net.Dial("unix", paths.AttachSock(pid))
	if err != nil {
		fmt.Fprintf(os.Stderr, "cux: cannot attach to %d: %v\n(is the session running a cux build with attach support?)\n", pid, err)
		return 1
	}
	defer conn.Close()

	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cux: attach needs a terminal:", err)
		return 1
	}
	defer term.Restore(fd, old)
	fmt.Printf("cux: attached to %d — detach with Ctrl+\\\r\n", pid)

	sendSize := func() {
		if cols, rows, err := term.GetSize(fd); err == nil {
			p := []byte{byte(rows >> 8), byte(rows), byte(cols >> 8), byte(cols)}
			_ = ptyhost.WriteFrame(conn, ptyhost.FrameResize, p)
		}
	}
	sendSize()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			sendSize()
		}
	}()

	done := make(chan struct{})
	go func() { // socket → terminal
		defer close(done)
		for {
			typ, payload, err := ptyhost.ReadFrame(conn)
			if err != nil {
				return
			}
			if typ == ptyhost.FrameOut {
				_, _ = os.Stdout.Write(payload)
			}
		}
	}()

	go func() { // terminal → socket, scanning for the detach key
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			for i := 0; i < n; i++ {
				if buf[i] == detachKey {
					_ = conn.Close()
					return
				}
			}
			if err := ptyhost.WriteFrame(conn, ptyhost.FrameInput, buf[:n]); err != nil {
				return
			}
		}
	}()

	<-done
	fmt.Print("\r\ncux: detached\r\n")
	return 0
}

// pickSession resolves the target wrapper PID: an explicit argument
// wins; otherwise the registry must hold exactly one attachable entry.
func pickSession(args []string) (int, error) {
	if len(args) > 0 {
		var pid int
		if _, err := fmt.Sscanf(args[0], "%d", &pid); err != nil {
			return 0, fmt.Errorf("attach: %q is not a pid (see `cux sessions`)", args[0])
		}
		return pid, nil
	}
	var attachable []registry.Entry
	for _, e := range registry.List() {
		if e.Attachable {
			attachable = append(attachable, e)
		}
	}
	switch len(attachable) {
	case 0:
		return 0, fmt.Errorf("no attachable sessions (see `cux sessions`)")
	case 1:
		return attachable[0].PID, nil
	default:
		return 0, fmt.Errorf("%d attachable sessions — pick one: `cux attach <pid>` (see `cux sessions`)", len(attachable))
	}
}
