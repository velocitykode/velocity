// Package maintpath resolves the absolute filesystem path of the maintenance
// down-file. Both the runtime middleware (root velocity package) and the
// console down/up commands share this helper so a single env var and a single
// resolution policy keep them in sync.
//
// Resolution policy (first match wins):
//  1. The VELOCITY_MAINTENANCE_ROOT env var, if set. Must be an absolute path
//     and must not contain any `..` segment. Symlinked roots are accepted
//     because the file inside may not exist yet (vel down creates it), but
//     traversal segments are rejected at the input layer per CLAUDE.md rule 4.
//  2. The directory of os.Executable() when available.
//  3. The current working directory as a last resort.
//
// The resolved path is computed once on first call and cached for the lifetime
// of the process. The cache deliberately ignores subsequent env mutations so
// the runtime path cannot drift if a test or hot-reload code path mutates the
// environment after boot.
package maintpath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// EnvVar is the env variable name an operator sets to override the default
// resolution. Exported for documentation and test purposes.
const EnvVar = "VELOCITY_MAINTENANCE_ROOT"

// MarkerDir is the directory inside the resolved root that holds the down
// marker file. Kept exported so callers writing the marker (console.Down)
// agree with callers reading it (runtime middleware).
const MarkerDir = ".vel"

// MarkerFile is the name of the marker file inside MarkerDir.
const MarkerFile = "down"

// ErrInvalidRoot is returned when VELOCITY_MAINTENANCE_ROOT is set to a
// path that fails validation (relative, contains `..`, etc).
var ErrInvalidRoot = errors.New("maintpath: invalid VELOCITY_MAINTENANCE_ROOT")

var (
	once      sync.Once
	cachedDir string
	cachedErr error
	// sourceLabel records which resolution branch was taken. Surfaced via
	// Source() so callers can log it once for operator visibility.
	sourceLabel string
)

// Reset clears the cached resolution. Test-only. Production code must not
// call this because it would allow env mutation to swing the resolved path.
func Reset() {
	once = sync.Once{}
	cachedDir = ""
	cachedErr = nil
	sourceLabel = ""
}

// Root returns the absolute directory holding the .vel marker dir. The first
// call performs resolution; subsequent calls return the same value (or error)
// regardless of env changes.
func Root() (string, error) {
	once.Do(resolve)
	return cachedDir, cachedErr
}

// Source returns a short string describing how Root was resolved
// ("env"/"executable"/"cwd"). Useful for the one-time WARN log on first
// reference. Empty before Root is called.
func Source() string {
	return sourceLabel
}

// MarkerPath returns the absolute path of the down-file. Equivalent to
// filepath.Join(Root(), MarkerDir, MarkerFile). Returns the error from Root
// untouched.
func MarkerPath() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, MarkerDir, MarkerFile), nil
}

// MarkerDirPath returns the absolute path of the .vel directory. Used by
// the console writer so MkdirAll targets the same place the reader reads.
func MarkerDirPath() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, MarkerDir), nil
}

// resolve runs the policy described in the package doc and populates the
// cache. Never returns an error from the executable / cwd branches because
// both are last-resort fallbacks; only the env branch can fail validation.
func resolve() {
	if raw, ok := os.LookupEnv(EnvVar); ok && raw != "" {
		cleaned, err := validateEnvRoot(raw)
		if err != nil {
			cachedErr = err
			sourceLabel = "env-invalid"
			return
		}
		cachedDir = cleaned
		sourceLabel = "env"
		return
	}
	// Default to the cwd captured at first reference (sync.Once-gated).
	// This is the "project root" in every common workflow: an operator
	// runs the binary from the project directory, so cwd is stable
	// across the CLI (`vel down`) and the server process even when they
	// are separate binaries with different executable paths. Using
	// filepath.Dir(os.Executable()) instead would split the writer and
	// reader whenever `go run ./cmd/cli down` ran alongside a compiled
	// server, or whenever the CLI and server lived in separate binaries
	// under different paths; the marker would land somewhere the
	// server's middleware never looks. Operators with a non-standard
	// layout (containers with WORKDIR drift, systemd units that chdir
	// after startup) set VELOCITY_MAINTENANCE_ROOT to pin an absolute
	// path.
	if wd, err := os.Getwd(); err == nil {
		cachedDir = wd
		sourceLabel = "cwd"
		return
	}
	if exe, err := os.Executable(); err == nil {
		// Last-resort fallback when Getwd fails (extremely rare; the
		// cwd was deleted out from under the process). filepath.Dir
		// preserves absolute-ness. Symlinks are intentionally not
		// resolved so the marker lives next to the invoked binary
		// path, not the symlink target.
		cachedDir = filepath.Dir(exe)
		sourceLabel = "executable"
		return
	}
	// Both Getwd() and Executable() failed. Fall back to "." so callers
	// do not panic; this will then fail open when the file is written
	// somewhere the reader cannot find, which is preferable to crashing.
	cachedDir = "."
	sourceLabel = "fallback"
}

// validateEnvRoot enforces the operator-supplied root is safe. CLAUDE.md
// rule 4 demands every operator-controlled filesystem path be validated
// against traversal; this is the input gate for VELOCITY_MAINTENANCE_ROOT.
func validateEnvRoot(raw string) (string, error) {
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("%w: must be absolute, got %q", ErrInvalidRoot, raw)
	}
	// Reject any `..` segment up front. filepath.Clean would collapse them
	// silently which is undesirable: operators should see their config
	// fails, not silently get a path different from what they typed.
	for _, seg := range strings.Split(filepath.ToSlash(raw), "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: must not contain .. segment, got %q", ErrInvalidRoot, raw)
		}
	}
	cleaned := filepath.Clean(raw)
	// A null byte in a path is rejected by most syscalls but the validation
	// belongs here, not at the syscall boundary, so operators see a clean
	// error rather than a kernel ENOENT.
	if strings.ContainsRune(cleaned, '\x00') {
		return "", fmt.Errorf("%w: must not contain NUL byte", ErrInvalidRoot)
	}
	return cleaned, nil
}
