//go:build !windows

package storage

import "syscall"

// setUmask updates the process umask and returns the previous value
// so test cleanup can restore it. Test helpers route through this so
// the file-mode tests can prove the fix is umask-independent.
func setUmask(mask int) int {
	return syscall.Umask(mask)
}
