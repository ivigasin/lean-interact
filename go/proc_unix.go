//go:build unix

package leaninteract

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts the REPL in its own process group (Python's
// start_new_session) so the whole tree can be signalled at once.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateTree(pid int) { _ = syscall.Kill(-pid, syscall.SIGTERM) }

func killTree(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }
