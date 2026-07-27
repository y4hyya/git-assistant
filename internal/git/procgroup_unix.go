//go:build unix

package git

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command in a process group of its own, so that
// killing it can reach the transport children git spawns (ssh,
// git-remote-https) rather than only git itself. Without this the deadline in
// runNetworkCtx kills the parent and leaves the child holding the pipes — and,
// worse, still writing to the repository the user was just told had timed out.
//
// The side effect is deliberate: a child in another process group is not in the
// terminal's foreground group, so an ssh that tries to prompt for a passphrase
// stops on SIGTTIN instead of fighting the TUI for the keyboard. It is then
// killed by the deadline like any other stall, which is exactly the outcome
// this package promises.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup SIGKILLs the whole group setProcessGroup created. The
// negative pid is the group: with Setpgid the child's pid IS its group id.
//
// Falls back to killing the single process when the group signal fails (the
// setpgid never happened, or the group is already gone), and returns
// os.ErrProcessDone in that case so exec does not report an already-finished
// command as a cancellation failure.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
