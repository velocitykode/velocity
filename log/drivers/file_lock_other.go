//go:build !unix

package drivers

import "os"

// lockFile is a no-op on non-Unix platforms. Windows does not expose
// a portable equivalent to flock through golang.org/x/sys; callers
// needing cross-process coordination on Windows should serialise at
// the application layer.
func lockFile(_ *os.File) (func(), error) {
	return func() {}, nil
}
