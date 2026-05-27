//go:build windows

package storage

// setUmask is a no-op on Windows; the file-mode tests skip there.
func setUmask(mask int) int { return 0 }
