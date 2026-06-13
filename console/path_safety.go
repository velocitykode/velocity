package console

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// validateMakeName rejects scaffolder name arguments that would let a caller
// escape the intended output directory or smuggle arbitrary text into
// generated source. The CLAUDE.md security rules require that any filesystem
// path built from user input be validated against traversal, and the make:*
// commands historically built directories straight from the name argument
// with no checks. A name like "../../tmp/owned" was accepted and produced
// writes outside the project root. Names also flow through toPascalCase /
// toSnakeCase into text/template with no escaping, so characters like quotes,
// backticks, semicolons, and newlines would survive into generated .go files.
//
// A name is rejected when it:
//   - is empty,
//   - is an absolute path,
//   - contains a ".." segment (parent traversal),
//   - contains a backslash (Windows separator, ambiguous on unix),
//   - has any segment that starts with "." (hidden dirs, "." itself),
//   - contains a NUL byte,
//   - has any segment not matching [A-Za-z][A-Za-z0-9_-]* (ASCII letters,
//     digits, underscore, hyphen; must start with a letter),
//   - contains a "/".
//
// Slashes are rejected because most make:* names become Go or proto
// identifiers (struct names, rpc names, package names), where a "/" that
// survived validation would reach templating as an invalid identifier. The
// callers that intentionally support nested output paths (make:handler
// names, make:grpc:service --dir) use validateMakeNestedName instead.
func validateMakeName(name string) error {
	if err := validateMakeNestedName(name); err != nil {
		return err
	}
	if strings.Contains(name, "/") {
		return fmt.Errorf("invalid name %q: must be a single path segment (no '/')", name)
	}
	return nil
}

// validateMakeNestedName applies the same rules as validateMakeName except
// that slashes are allowed: legitimate namespaced names (e.g. "Admin/Users")
// rely on them, and each slash-separated segment is validated independently.
// Only callers that intentionally build nested output directories from the
// value may use this; identifier-bound inputs go through validateMakeName.
func validateMakeNestedName(name string) error {
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
			// filepath.IsAbs already rejected a leading slash, so an empty
			// segment here means a doubled or trailing slash.
			return fmt.Errorf("invalid name %q: empty path segment not allowed", name)
		}
		if seg == ".." {
			return fmt.Errorf("invalid name %q: parent traversal not allowed", name)
		}
		if strings.HasPrefix(seg, ".") {
			return fmt.Errorf("invalid name %q: segment %q starts with '.'", name, seg)
		}
		if err := validateNameSegment(name, seg); err != nil {
			return err
		}
	}
	return nil
}

// validateNameSegment enforces the identifier charset on a single
// slash-separated segment of a make:* name: an ASCII letter followed by ASCII
// letters, digits, underscores, or hyphens. Anything else (quotes, backticks,
// semicolons, whitespace, unicode confusables, ...) is rejected with an error
// naming the offending character, because segments are written into generated
// Go source via text/template with no escaping.
func validateNameSegment(name, seg string) error {
	for i, r := range seg {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			// letters allowed anywhere
		case i > 0 && ((r >= '0' && r <= '9') || r == '_' || r == '-'):
			// digits, '_', '-' allowed after the leading letter
		default:
			if i == 0 {
				return fmt.Errorf("invalid name %q: segment %q must start with a letter (A-Z, a-z), found %q", name, seg, r)
			}
			return fmt.Errorf("invalid name %q: character %q not allowed (segments must match [A-Za-z][A-Za-z0-9_-]*)", name, r)
		}
	}
	return nil
}

// resolveMakeDir resolves the output directory a make:* generator should
// write to. When override (the value of the --dir flag) is empty the caller's
// defaultDir is returned unchanged. Otherwise the override is treated as a
// project-relative directory: validated against the make:* name charset
// (nested segments allowed, traversal/absolute/dot-prefixed segments
// rejected), cleaned, and confirmed to stay inside the working tree before it
// is returned. It does NOT create the directory; callers MkdirAll the result.
//
// Centralising this keeps the path-traversal guard (CLAUDE.md security rule 4)
// in one audited place rather than duplicated across every generator.
func resolveMakeDir(defaultDir, override string) (string, error) {
	if override == "" {
		return defaultDir, nil
	}
	if err := validateMakeNestedName(override); err != nil {
		return "", fmt.Errorf("invalid --dir %q: %w", override, err)
	}
	dir := filepath.Clean(override)
	if err := ensureWithinRoot(".", dir); err != nil {
		return "", fmt.Errorf("invalid --dir %q: %w", override, err)
	}
	// ensureWithinRoot only compares lexical absolute paths; it does not follow
	// symlinks. A pre-existing symlink component (e.g. custom -> /tmp/outside)
	// would pass that check yet redirect the eventual write outside the tree.
	// Reject any existing symlink in the override's path so the sandbox holds.
	if err := ensureNoSymlinkComponents(dir); err != nil {
		return "", fmt.Errorf("invalid --dir %q: %w", override, err)
	}
	return dir, nil
}

// ensureWritableTarget rejects when the final output file path is already
// occupied. It uses os.Lstat rather than os.Stat so a symlink at the target,
// including a dangling one (which Stat reports as not-existing), is detected
// and refused instead of being silently followed by the subsequent write,
// which would place the generated file wherever the link points, outside the
// project root. A genuinely absent path returns nil so the caller may write.
// kind names the artifact for the diagnostic.
func ensureWritableTarget(path, kind string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect %s path %q: %w", kind, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s path %q is a symlink", kind, path)
	}
	return fmt.Errorf("%s already exists: %s", kind, path)
}

// ensureNoSymlinkComponents walks the existing leading components of a
// project-relative directory and rejects any that is a symlink. Once a
// component does not yet exist the walk stops: the remaining path will be
// materialised by MkdirAll as real directories, which cannot redirect a write
// elsewhere. This closes the symlink-traversal gap that a purely lexical
// within-root check leaves open.
func ensureNoSymlinkComponents(dir string) error {
	cur := "."
	for _, seg := range strings.Split(dir, string(os.PathSeparator)) {
		cur = filepath.Join(cur, seg)
		info, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("inspect path %q: %w", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component %q is a symlink", cur)
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
