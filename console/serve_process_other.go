//go:build !unix

package console

import "os/exec"

// startInOwnProcessGroup is a no-op where the platform has no POSIX
// process groups; cmd starts as a plain child process.
func startInOwnProcessGroup(cmd *exec.Cmd) {}

// stopProcessGroup kills cmd's process where the platform has no POSIX
// process groups or SIGTERM delivery; the processes it spawned are not
// signalled.
func stopProcessGroup(cmd *exec.Cmd) {
	_ = cmd.Process.Kill()
}
