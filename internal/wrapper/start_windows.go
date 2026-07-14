//go:build windows

package wrapper

import (
	"os"
	"os/exec"

	"github.com/inulute/cux/internal/ptyhost"
)

// startChild launches claude on Windows. The ConPTY attach path is wired
// in once ptyhost_windows.go grows a real implementation; until then the
// host is always nil and the child inherits the wrapper's stdio.
func startChild(claudeBin string, argv, env []string, host *ptyhost.Host) (child, error) {
	cmd := exec.Command(claudeBin, argv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execChild{cmd: cmd}, nil
}
