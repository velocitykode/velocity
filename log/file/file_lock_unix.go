//go:build unix

package file

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFile acquires an advisory exclusive (LOCK_EX) lock on f. The
// returned release function unlocks the file. Used by FileLogger.log
// when WithFileLock is set so multi-process deployments do not
// interleave bytes mid-record.
//
// Blocks until the lock is acquired so writes on one process wait for
// the in-flight write on a sibling to complete; this is intentional
// (the caller already holds the in-process mu, so the kernel-level
// wait is the only contention path).
func lockFile(f *os.File) (func(), error) {
	fd := int(f.Fd())
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		return func() {}, err
	}
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
	}, nil
}
