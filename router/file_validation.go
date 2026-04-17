package router

import (
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ErrSymlinkEscape is returned by ValidateFilePathWithin when the path
// is (or resolves through) a symlink that leaves the allowed root.
var ErrSymlinkEscape = errors.New("velocity/router: file path escapes allowed root")

// ErrPathOutsideRoot is returned by ValidateFilePathWithin when the
// cleaned path, without following links, is not contained in root.
var ErrPathOutsideRoot = errors.New("velocity/router: file path outside allowed root")

// FileValidationOption configures file validation behavior.
type FileValidationOption func(*fileValidationConfig)

type fileValidationConfig struct {
	maxSize    int64
	extensions []string
	mimeTypes  []string
}

// MaxFileSize constrains the uploaded file to the given number of bytes.
func MaxFileSize(bytes int64) FileValidationOption {
	return func(cfg *fileValidationConfig) {
		cfg.maxSize = bytes
	}
}

// AllowedExtensions restricts uploads to the listed extensions (case-insensitive).
// Extensions should include the leading dot (e.g., ".jpg", ".png").
func AllowedExtensions(exts ...string) FileValidationOption {
	return func(cfg *fileValidationConfig) {
		lower := make([]string, len(exts))
		for i, e := range exts {
			lower[i] = strings.ToLower(e)
		}
		cfg.extensions = lower
	}
}

// AllowedMIMETypes restricts uploads to files whose detected content type
// matches one of the given MIME types. Detection uses http.DetectContentType
// on the first 512 bytes of the file content (not the user-supplied
// Content-Type header). Parameters like charset are ignored during matching,
// so "text/plain" matches "text/plain; charset=utf-8".
func AllowedMIMETypes(types ...string) FileValidationOption {
	return func(cfg *fileValidationConfig) {
		normalized := make([]string, 0, len(types))
		for _, t := range types {
			mt, _, _ := mime.ParseMediaType(t)
			if mt != "" {
				normalized = append(normalized, strings.ToLower(mt))
			} else {
				normalized = append(normalized, strings.ToLower(strings.TrimSpace(t)))
			}
		}
		cfg.mimeTypes = normalized
	}
}

// ValidateFile checks an uploaded file header against the provided constraints.
// Returns nil if all checks pass, or an error describing the first failure.
func (c *Context) ValidateFile(fh *multipart.FileHeader, opts ...FileValidationOption) error {
	var cfg fileValidationConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	// Check file size
	if cfg.maxSize > 0 && fh.Size > cfg.maxSize {
		return fmt.Errorf("file size %d exceeds maximum allowed size %d", fh.Size, cfg.maxSize)
	}

	// Check extension
	if len(cfg.extensions) > 0 {
		ext := strings.ToLower(filepath.Ext(fh.Filename))
		allowed := false
		for _, e := range cfg.extensions {
			if ext == e {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("file extension %q is not allowed", ext)
		}
	}

	// Check MIME type by reading file content
	if len(cfg.mimeTypes) > 0 {
		f, err := fh.Open()
		if err != nil {
			return fmt.Errorf("failed to open file for MIME detection: %w", err)
		}
		defer f.Close()

		buf := make([]byte, 512)
		n, _ := f.Read(buf)
		raw := http.DetectContentType(buf[:n])
		detected, _, _ := mime.ParseMediaType(raw)
		detected = strings.ToLower(detected)

		allowed := false
		for _, mt := range cfg.mimeTypes {
			if detected == mt {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("detected MIME type %q is not allowed", detected)
		}
	}

	return nil
}

// safeFilenameRe matches characters that are NOT alphanumeric, dot, hyphen, or underscore.
var safeFilenameRe = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// SanitizeFilename cleans a filename for safe storage:
//   - Strips directory components
//   - Removes null bytes
//   - Replaces non-alphanumeric characters (except . - _) with underscore
//   - Limits total length to 255 characters
func SanitizeFilename(name string) string {
	// Strip directory components
	name = filepath.Base(name)

	// Remove null bytes
	name = strings.ReplaceAll(name, "\x00", "")

	// Replace unsafe characters
	name = safeFilenameRe.ReplaceAllString(name, "_")

	// Limit length to 255 characters
	if utf8.RuneCountInString(name) > 255 {
		runes := []rune(name)
		name = string(runes[:255])
	}

	return name
}

// ValidateFilePathWithin verifies that path is contained within the
// root directory, both by lexical comparison and by resolving symlinks.
// Returns the fully-resolved absolute path on success.
//
// Layered checks:
//  1. Reject any ".." segment after Clean.
//  2. Lexically ensure path is prefixed by root (filepath.Rel must not
//     escape).
//  3. os.Lstat the joined path to catch symlinks whose own metadata
//     is fine, but whose target escapes root — we follow the symlink
//     via filepath.EvalSymlinks and re-check containment.
//
// Callers should pass an absolute root. A relative root is resolved
// via filepath.Abs against the current working directory.
func ValidateFilePathWithin(path, root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("velocity/router: file validation: empty root")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("velocity/router: file validation: resolve root: %w", err)
	}
	absRoot = filepath.Clean(absRoot)

	// Resolve root's own symlinks so EvalSymlinks of any child produces
	// a path that can be meaningfully compared. macOS aliases /var to
	// /private/var; automounters expose similar patterns elsewhere.
	resolvedRoot := absRoot
	if resolved, rerr := filepath.EvalSymlinks(absRoot); rerr == nil {
		resolvedRoot = resolved
	}

	cleanedPath := filepath.Clean(path)
	if strings.Contains(cleanedPath, "..") {
		return "", fmt.Errorf("%w: contains '..': %q", ErrPathOutsideRoot, path)
	}

	joined := cleanedPath
	if !filepath.IsAbs(joined) {
		joined = filepath.Join(absRoot, cleanedPath)
	}
	joined = filepath.Clean(joined)

	// Accept the path if it is contained in either the lexical or the
	// resolved form of root. This keeps the helper usable on macOS
	// where tempdirs live under /var -> /private/var.
	if !pathWithinRoot(joined, absRoot) && !pathWithinRoot(joined, resolvedRoot) {
		return "", fmt.Errorf("%w: %q", ErrPathOutsideRoot, path)
	}

	// Lstat to detect a symlink at the leaf. Intermediate symlinks are
	// caught by EvalSymlinks below. If the path does not exist yet
	// (e.g. a write target), only lexical containment is enforced.
	info, err := os.Lstat(joined)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return joined, nil
		}
		return "", fmt.Errorf("velocity/router: file validation: lstat: %w", err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		resolvedPath, resolveErr := filepath.EvalSymlinks(joined)
		if resolveErr != nil {
			return "", fmt.Errorf("%w: resolve: %v", ErrSymlinkEscape, resolveErr)
		}
		if !pathWithinRoot(resolvedPath, absRoot) && !pathWithinRoot(resolvedPath, resolvedRoot) {
			return "", fmt.Errorf("%w: %q -> %q", ErrSymlinkEscape, path, resolvedPath)
		}
		return resolvedPath, nil
	}

	return joined, nil
}

// pathWithinRoot reports whether p is contained in (or equal to) root.
// Uses filepath.Rel rather than strings.HasPrefix to avoid the classic
// "/etc/passwd_evil" bypass on "/etc/passwd".
func pathWithinRoot(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, "..") && !strings.Contains(rel, string(filepath.Separator)+"..")
}
