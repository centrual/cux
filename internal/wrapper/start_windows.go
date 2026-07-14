//go:build windows

package wrapper

import (
	"os"
	"os/exec"

	"github.com/inulute/cux/internal/ptyhost"
)

// startChild launches claude on Windows. With an attach host it runs on
// the host's ConPTY (os/exec can't attach one, so the host owns the
// spawn); otherwise it inherits the wrapper's stdio.
func startChild(claudeBin string, argv, env []string, host *ptyhost.Host) (child, error) {
	if host != nil {
		return host.StartAttached(claudeBin, argv, env)
	}
	cmd := exec.Command(claudeBin, argv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execChild{cmd: cmd}, nil
}
