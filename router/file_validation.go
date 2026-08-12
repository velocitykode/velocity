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

// ErrPathOutsideRoot is returned by OpenFileIn when the requested path
// escapes the root directory via traversal or symlink. The underlying
// check is delegated to the kernel via os.Root, which on Linux uses
// openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS) and on other platforms
// uses the strongest equivalent the runtime can provide. This closes
// the TOCTOU window that the previous user-space implementation had
// between Lstat and Open.
var ErrPathOutsideRoot = errors.New("velocity/router: file path outside allowed root")

// ErrNilRoot is returned by OpenFileIn when the caller passes a nil
// *os.Root. The framework never constructs a nil Root internally; a nil
// value here indicates a caller bug (e.g. forgetting to run the module
// that opens the root) and must not panic library code.
var ErrNilRoot = errors.New("velocity/router: nil *os.Root")

// OpenFileIn opens relative against root, returning the open handle.
//
// Containment is kernel-enforced via (*os.Root).Open — on Linux it uses
// openat2 with RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS, and on other
// platforms the Go runtime provides the strongest equivalent. The open
// handle is returned so callers never re-resolve the path; re-opening
// the file from its string name would re-introduce the TOCTOU window
// that this API exists to eliminate.
//
// Error behaviour:
//   - A nil root returns ErrNilRoot.
//   - A path that escapes the root (traversal or symlink) is wrapped as
//     "velocity/router: path %q escapes root: %w" around ErrPathOutsideRoot.
//   - A nonexistent file returns the standard os error from os.Root.Open
//     unwrapped, so errors.Is(err, os.ErrNotExist) works.
//
// Callers are responsible for closing the returned *os.File.
func OpenFileIn(root *os.Root, relative string) (*os.File, error) {
	if root == nil {
		return nil, ErrNilRoot
	}
	f, err := root.Open(relative)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		// os.Root surfaces containment violations as *PathError wrapping
		// syscall.EXDEV / ENOTDIR / a dedicated sentinel depending on
		// platform. We fold all of them into ErrPathOutsideRoot so
		// callers can switch on a single sentinel.
		return nil, fmt.Errorf("velocity/router: path %q escapes root: %w", relative, errors.Join(ErrPathOutsideRoot, err))
	}
	return f, nil
}

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
