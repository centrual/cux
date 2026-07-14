package wrapper

import (
	"os"
	"os/exec"
)

// child is the running claude process as launch() needs to drive it. It
// exists so the Windows ConPTY path — which can't go through os/exec —
// can plug in beside the Unix os/exec path without the swap/poll/wait
// logic knowing which one it's talking to.
type child interface {
	// Pid is the OS process id (0 before start / after reap).
	Pid() int
	// Signal asks the process to exit (os.Interrupt for a graceful stop).
	Signal(os.Signal) error
	// Kill force-terminates the process.
	Kill() error
	// Exited reports whether the process has been reaped (Wait returned).
	Exited() bool
	// Wait blocks until the process exits and returns its wait error
	// (an *exec.ExitError for a non-zero exit).
	Wait() error
}

// execChild is the Unix (and non-attach) child: a plain os/exec process.
type execChild struct{ cmd *exec.Cmd }

func (c *execChild) Pid() int {
	if c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

func (c *execChild) Signal(s os.Signal) error {
	if c.cmd.Process == nil {
		return nil
	}
	return c.cmd.Process.Signal(s)
}

func (c *execChild) Kill() error {
	if c.cmd.Process == nil {
		return nil
	}
	return c.cmd.Process.Kill()
}

func (c *execChild) Exited() bool { return c.cmd.ProcessState != nil }

func (c *execChild) Wait() error { return c.cmd.Wait() }
