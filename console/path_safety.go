package console

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// validateMakeName rejects scaffolder name arguments that would let a caller
// escape the intended output directory. The CLAUDE.md security rules require
// that any filesystem path built from user input be validated against
// traversal, and the make:* commands historically built directories straight
// from the name argument with no checks. A name like "../../tmp/owned" was
// accepted and produced writes outside the project root.
//
// A name is rejected when it:
//   - is empty,
//   - is an absolute path,
//   - contains a ".." segment (parent traversal),
//   - contains a backslash (Windows separator, ambiguous on unix),
//   - has any segment that starts with "." (hidden dirs, "." itself),
//   - contains a NUL byte.
//
// Slashes are allowed because legitimate namespaced names (e.g. "Admin/Users")
// rely on them.
func validateMakeName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if strings.ContainsRune(name, 0) {
		return fmt.Errorf("invalid name %q: contains NUL byte", name)
	}
	if strings.Contains(name, "\\") {
		return fmt.Errorf("invalid name %q: backslash not allowed", name)
	}
	if filepath.IsAbs(name) {
		return fmt.Errorf("invalid name %q: absolute paths not allowed", name)
	}
	// Use forward slash splitting explicitly so platform separators do not
	// change the semantics. filepath.IsAbs covers the leading-slash case
	// already; we still need to inspect every segment for ".." and dot
	// prefixes regardless of OS.
	for _, seg := range strings.Split(name, "/") {
		if seg == "" {
			continue
		}
		if seg == ".." {
			return fmt.Errorf("invalid name %q: parent traversal not allowed", name)
		}
		if strings.HasPrefix(seg, ".") {
			return fmt.Errorf("invalid name %q: segment %q starts with '.'", name, seg)
		}
	}
	return nil
}

// ensureWithinRoot verifies that a path resolves inside the given root after
// any "." / ".." cleaning. Both arguments are resolved to absolute paths
// before comparison so a working directory containing symlinks still produces
// a consistent answer. The check guards every filesystem write driven by
// user-supplied scaffolder names.
func ensureWithinRoot(root, path string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root %q: %w", root, err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path %q: %w", path, err)
	}
	// Append a separator to the root so /foo/bar does not appear to live
	// inside /foo/barbaz. The exact-equality check covers the case where
	// path == root.
	prefix := absRoot + string(os.PathSeparator)
	if absPath != absRoot && !strings.HasPrefix(absPath, prefix) {
		return fmt.Errorf("path %q escapes root %q", absPath, absRoot)
	}
	return nil
}
