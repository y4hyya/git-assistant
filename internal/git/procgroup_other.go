//go:build !unix

package git

import (
	"os"
	"os/exec"
)

// Process groups are a POSIX notion; on the platforms without them the
// timeout's second mechanism carries the whole load. cmd.WaitDelay
// (runNetworkCtx) closes the parent's pipe ends after the kill, so a transport
// child that outlives git can no longer block the read forever — it is only
// left running, not left in the way.
func setProcessGroup(*exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Kill()
}
