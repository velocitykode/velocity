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
