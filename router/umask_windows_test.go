//go:build windows

package router

// umaskSet is a no-op on Windows; file-mode tests skip there.
func umaskSet(mask int) int { return 0 }
