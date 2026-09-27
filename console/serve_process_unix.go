//go:build unix

package console

import (
	"os/exec"
	"syscall"
)

// startInOwnProcessGroup makes cmd start as the leader of a new process
// group, so stopProcessGroup reaches every process it spawns.
func startInOwnProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// stopProcessGroup sends SIGTERM to the process group cmd leads (see
// startInOwnProcessGroup).
func stopProcessGroup(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}
