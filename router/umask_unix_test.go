//go:build !windows

package router

import "syscall"

// umaskSet wraps syscall.Umask for the router-side file-mode tests
// (test-only; never used from non-test code). Returns the previous
// umask so callers can restore it.
func umaskSet(mask int) int {
	return syscall.Umask(mask)
}
