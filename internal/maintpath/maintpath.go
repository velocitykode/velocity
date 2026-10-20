// Package maintpath resolves the absolute filesystem path of the maintenance
// down-file. Both the runtime middleware (root velocity package) and the
// console down/up commands share this helper so a single configured root and a
// single resolution policy keep them in sync.
//
// Resolution policy (first match wins):
//  1. The configured root (maintenance.Config.Root, read from
//     VELOCITY_MAINTENANCE_ROOT by velocity.ConfigFromEnv), if set. Must be an absolute path
//     and must not contain any `..` segment. Symlinked roots are accepted
//     because the file inside may not exist yet (vel down creates it), but
//     traversal segments are rejected at the input layer per CLAUDE.md rule 4.
//  2. The current working directory captured at first reference. This is the
//     shared "project root" identity the CLI (`vel down`) and the running
//     server agree on in every common workflow; using the executable dir
//     instead would split the writer and reader whenever the two run from
//     different binary paths (go run vs. compiled, distinct CLI / server
//     binaries, containers with per-entrypoint WORKDIR).
//  3. The directory of os.Executable() as a last-resort fallback when
//     os.Getwd fails (extremely rare; cwd was deleted out from under the
//     process). Symlinks are intentionally not resolved.
//
// The resolved path is computed once on first call and cached for the lifetime
// of the process: the first caller's configured root wins, and later calls
// return the same value whatever root they pass, so the runtime path cannot
// drift after boot.
package maintpath

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/velocitykode/velocity/internal/errchain"
)

// MarkerDir is the directory inside the resolved root that holds the down
// marker file. Kept exported so callers writing the marker (console.Down)
// agree with callers reading it (runtime middleware).
const MarkerDir = ".vel"

// MarkerFile is the name of the marker file inside MarkerDir.
const MarkerFile = "down"

// ErrInvalidRoot is returned when the configured root fails validation
// (relative, contains `..`, etc).
var ErrInvalidRoot = errors.New("maintpath: invalid maintenance root (VELOCITY_MAINTENANCE_ROOT)")

var (
	once      sync.Once
	cachedDir string
	cachedErr error
	// sourceLabel records which resolution branch was taken. Surfaced via
	// Source() so callers can log it once for operator visibility.
	sourceLabel string
)

// Reset clears the cached resolution. Test-only. Production code must not
// call this because it would let a later root swing the resolved path.
func Reset() {
	once = sync.Once{}
	cachedDir = ""
	cachedErr = nil
	sourceLabel = ""
}

// Root returns the absolute directory holding the .vel marker dir. The first
// call resolves configured (see the package doc); subsequent calls return the
// same value (or error) whatever they pass.
func Root(configured string) (string, error) {
	once.Do(func() { resolve(configured) })
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
func MarkerPath(configured string) (string, error) {
	root, err := Root(configured)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, MarkerDir, MarkerFile), nil
}

// MarkerDirPath returns the absolute path of the .vel directory. Used by
// the console writer so MkdirAll targets the same place the reader reads.
func MarkerDirPath(configured string) (string, error) {
	root, err := Root(configured)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, MarkerDir), nil
}

// resolve runs the policy described in the package doc and populates the
// cache. Never returns an error from the executable / cwd branches because
// both are last-resort fallbacks; only the configured branch can fail
// validation.
func resolve(configured string) {
	if configured != "" {
		cleaned, err := validateEnvRoot(configured)
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
		return "", errchain.Errorf("%w: must be absolute, got %q", ErrInvalidRoot, raw)
	}
	// Reject any `..` segment up front. filepath.Clean would collapse them
	// silently which is undesirable: operators should see their config
	// fails, not silently get a path different from what they typed.
	for _, seg := range strings.Split(filepath.ToSlash(raw), "/") {
		if seg == ".." {
			return "", errchain.Errorf("%w: must not contain .. segment, got %q", ErrInvalidRoot, raw)
		}
	}
	cleaned := filepath.Clean(raw)
	// A null byte in a path is rejected by most syscalls but the validation
	// belongs here, not at the syscall boundary, so operators see a clean
	// error rather than a kernel ENOENT.
	if strings.ContainsRune(cleaned, '\x00') {
		return "", errchain.Errorf("%w: must not contain NUL byte", ErrInvalidRoot)
	}
	return cleaned, nil
}
