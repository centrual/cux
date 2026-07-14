//go:build !windows

package wrapper

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/inulute/cux/internal/ptyhost"
)

// startChild launches claude. With an attach host it runs on a fresh PTY
// slave (dup per launch — see ptyhost.TTYDup) as its controlling
// terminal; otherwise it inherits the wrapper's stdio.
func startChild(claudeBin string, argv, env []string, host *ptyhost.Host) (child, error) {
	cmd := exec.Command(claudeBin, argv...)
	if host != nil {
		tty, err := host.TTYDup()
		if err != nil {
			return nil, fmt.Errorf("dup pty slave: %w", err)
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
		cmd.SysProcAttr = ptyhost.SysProcAttr()
	} else {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	}
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execChild{cmd: cmd}, nil
}
