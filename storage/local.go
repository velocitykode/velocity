package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// defaultMaxFileSize is the default maximum file size for local storage (100MB)
const defaultMaxFileSize = 100 * 1024 * 1024

func init() {
	Drivers().Register("local", func(_ context.Context, cfg DiskConfig) (Driver, error) {
		return NewLocalDriver(cfg), nil
	})
}

// LocalDriver implements the Driver interface for local filesystem storage.
//
// Containment is enforced by an *os.Root opened at driver construction.
// All per-operation path handling is delegated to the root handle — on
// Linux this means openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS), which
// kernel-rejects traversal and symlink escapes and eliminates the
// TOCTOU window that a Lstat-then-Open implementation would have.
type LocalDriver struct {
	root        string
	rootMu      sync.RWMutex
	rootHandle  *os.Root
	url         string
	visibility  Visibility
	maxFileSize int64
}

// Compile-time assertion: LocalDriver releases the *os.Root file descriptor
// via contract.ShutdownAware, so the storage manager can drain it during
// app shutdown and tests can avoid FD leaks.
var _ contract.ShutdownAware = (*LocalDriver)(nil)

// NewLocalDriver creates a new local storage driver.
//
// The driver opens an *os.Root for the configured directory at
// construction and retains it for the lifetime of the driver. Callers
// MUST call Shutdown(ctx) to release the root; the storage manager
// wires this into its own Shutdown chain.
//
// If the configured directory cannot be created or opened as a root,
// NewLocalDriver returns a driver with a nil root — every subsequent
// operation will fail with ErrInvalidPath. We do not panic because
// storage is commonly optional per-app.
func NewLocalDriver(config DiskConfig) *LocalDriver {
	root := config.Root
	if !filepath.IsAbs(root) {
		if cwd, err := os.Getwd(); err == nil {
			root = filepath.Join(cwd, root)
		}
	}

	// Ensure the directory exists with restricted permissions before
	// handing it to os.OpenRoot — OpenRoot refuses to open a missing
	// directory.
	_ = os.MkdirAll(root, 0o700)

	visibility := Private
	if config.Visibility == "public" {
		visibility = Public
	}

	maxFileSize := int64(defaultMaxFileSize)
	if config.MaxSize > 0 {
		maxFileSize = config.MaxSize
	}

	d := &LocalDriver{
		root:        root,
		url:         strings.TrimSuffix(config.URL, "/"),
		visibility:  visibility,
		maxFileSize: maxFileSize,
	}

	if handle, err := os.OpenRoot(root); err == nil {
		d.rootHandle = handle
	}
	return d
}

// Shutdown releases the *os.Root file descriptor. Idempotent.
func (d *LocalDriver) Shutdown(ctx context.Context) error {
	d.rootMu.Lock()
	defer d.rootMu.Unlock()
	if d.rootHandle == nil {
		return nil
	}
	err := d.rootHandle.Close()
	d.rootHandle = nil
	if err != nil {
		return fmt.Errorf("velocity/storage: close local root: %w", err)
	}
	return nil
}

// withRoot runs fn with the driver's *os.Root under a read lock.
// Returns ErrInvalidPath if the driver was constructed without a usable
// root (e.g. the configured directory could not be created).
func (d *LocalDriver) withRoot(fn func(root *os.Root) error) error {
	d.rootMu.RLock()
	defer d.rootMu.RUnlock()
	if d.rootHandle == nil {
		return fmt.Errorf("velocity/storage: local driver has no open root: %w", ErrInvalidPath)
	}
	return fn(d.rootHandle)
}

// normalizeRelative converts a user-supplied path into a form acceptable
// to *os.Root. Absolute paths are rejected outright (os.Root already
// refuses them, but a dedicated error beats the kernel's generic
// EINVAL). Slash direction is normalised so Windows-style backslashes
// continue to work on cross-platform callers.
func normalizeRelative(path string) (string, error) {
	normalised := filepath.FromSlash(path)
	if filepath.IsAbs(normalised) || strings.HasPrefix(normalised, string(filepath.Separator)) {
		return "", fmt.Errorf("velocity/storage: absolute path rejected: %w", ErrInvalidPath)
	}
	clean := filepath.Clean(normalised)
	if clean == "" || clean == "." {
		return ".", nil
	}
	return clean, nil
}

// mapOpenError converts *os.Root open errors to storage-layer errors.
// The kernel (on Linux via openat2) rejects escape attempts with a
// variety of errno values (EXDEV/ENOTDIR/ELOOP/EACCES), so rather than
// matching on each one we treat any non-NotExist error from the root as
// an invalid-path failure. Callers that genuinely need IO-error detail
// can still errors.As for *os.PathError.
func mapOpenError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return ErrFileNotFound
	}
	return err
}

// Put stores content at the given path
func (d *LocalDriver) Put(path string, contents []byte) error {
	if int64(len(contents)) > d.maxFileSize {
		return fmt.Errorf("velocity/storage: file size %d exceeds maximum of %d bytes: %w", len(contents), d.maxFileSize, ErrQuotaExceeded)
	}
	rel, err := normalizeRelative(path)
	if err != nil {
		return err
	}
	return d.withRoot(func(root *os.Root) error {
		if err := mkdirAllIn(root, filepath.Dir(rel)); err != nil {
			return fmt.Errorf("velocity/storage: create directory: %w", err)
		}
		// Atomic write: temp in same directory, rename.
		tmp := rel + ".tmp"
		if err := root.WriteFile(tmp, contents, 0o600); err != nil {
			return fmt.Errorf("velocity/storage: write file: %w", mapOpenError(err))
		}
		if err := root.Rename(tmp, rel); err != nil {
			_ = root.Remove(tmp)
			return fmt.Errorf("velocity/storage: move file: %w", mapOpenError(err))
		}
		return nil
	})
}

// PutStream stores a stream at the given path
func (d *LocalDriver) PutStream(path string, stream io.Reader) error {
	rel, err := normalizeRelative(path)
	if err != nil {
		return err
	}
	return d.withRoot(func(root *os.Root) error {
		if err := mkdirAllIn(root, filepath.Dir(rel)); err != nil {
			return fmt.Errorf("velocity/storage: create directory: %w", err)
		}
		tmp := rel + ".tmp"
		file, err := root.Create(tmp)
		if err != nil {
			return fmt.Errorf("velocity/storage: create file: %w", mapOpenError(err))
		}
		// root.Create resolves the file's mode through the process
		// umask (typically yielding 0o644 / 0o664). Tighten to 0o600
		// before any bytes are written so request bodies, uploads,
		// and any incidental PII landing on disk are owner-only by
		// default, matching the invariant Put already maintains.
		if chmodErr := file.Chmod(0o600); chmodErr != nil {
			_ = file.Close()
			_ = root.Remove(tmp)
			return fmt.Errorf("velocity/storage: chmod file: %w", chmodErr)
		}
		limited := io.LimitReader(stream, d.maxFileSize+1)
		written, copyErr := io.Copy(file, limited)
		closeErr := file.Close()
		if copyErr != nil {
			_ = root.Remove(tmp)
			return fmt.Errorf("velocity/storage: write stream: %w", copyErr)
		}
		if closeErr != nil {
			_ = root.Remove(tmp)
			return fmt.Errorf("velocity/storage: close file: %w", closeErr)
		}
		if written > d.maxFileSize {
			_ = root.Remove(tmp)
			return fmt.Errorf("velocity/storage: stream exceeds maximum size of %d bytes: %w", d.maxFileSize, ErrQuotaExceeded)
		}
		if err := root.Rename(tmp, rel); err != nil {
			_ = root.Remove(tmp)
			return fmt.Errorf("velocity/storage: move file: %w", mapOpenError(err))
		}
		return nil
	})
}

// Get retrieves content from the given path
func (d *LocalDriver) Get(path string) ([]byte, error) {
	rel, err := normalizeRelative(path)
	if err != nil {
		return nil, err
	}
	var contents []byte
	err = d.withRoot(func(root *os.Root) error {
		data, err := root.ReadFile(rel)
		if err != nil {
			return mapOpenError(err)
		}
		contents = data
		return nil
	})
	return contents, err
}

// GetStream retrieves a stream from the given path.
//
// The returned ReadCloser is an open *os.File obtained via OpenFileIn —
// callers MUST close it. No re-resolution happens between this call
// and the read.
func (d *LocalDriver) GetStream(path string) (io.ReadCloser, error) {
	rel, err := normalizeRelative(path)
	if err != nil {
		return nil, err
	}
	var file *os.File
	err = d.withRoot(func(root *os.Root) error {
		f, err := root.Open(rel)
		if err != nil {
			return mapOpenError(err)
		}
		file = f
		return nil
	})
	if err != nil {
		return nil, err
	}
	return file, nil
}

// Exists checks if a file exists at the given path
func (d *LocalDriver) Exists(path string) bool {
	rel, err := normalizeRelative(path)
	if err != nil {
		return false
	}
	var exists bool
	_ = d.withRoot(func(root *os.Root) error {
		if _, err := root.Stat(rel); err == nil {
			exists = true
		}
		return nil
	})
	return exists
}

// Delete removes files at the given paths
func (d *LocalDriver) Delete(paths ...string) error {
	return d.withRoot(func(root *os.Root) error {
		for _, path := range paths {
			rel, err := normalizeRelative(path)
			if err != nil {
				return err
			}
			if err := root.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("velocity/storage: delete %s: %w", path, mapOpenError(err))
			}
		}
		return nil
	})
}

// Copy copies a file from one path to another
func (d *LocalDriver) Copy(from, to string) error {
	fromRel, err := normalizeRelative(from)
	if err != nil {
		return err
	}
	toRel, err := normalizeRelative(to)
	if err != nil {
		return err
	}
	return d.withRoot(func(root *os.Root) error {
		source, err := root.Open(fromRel)
		if err != nil {
			return fmt.Errorf("velocity/storage: open source: %w", mapOpenError(err))
		}
		defer source.Close()

		if err := mkdirAllIn(root, filepath.Dir(toRel)); err != nil {
			return fmt.Errorf("velocity/storage: create directory: %w", err)
		}
		dest, err := root.Create(toRel)
		if err != nil {
			return fmt.Errorf("velocity/storage: create destination: %w", mapOpenError(err))
		}
		defer dest.Close()
		// Tighten umask-derived mode (~0o644) down to 0o600 so the
		// copy inherits the same owner-only invariant Put applies on
		// initial write.
		if chmodErr := dest.Chmod(0o600); chmodErr != nil {
			return fmt.Errorf("velocity/storage: chmod destination: %w", chmodErr)
		}
		if _, err := io.Copy(dest, source); err != nil {
			return fmt.Errorf("velocity/storage: copy: %w", err)
		}
		return nil
	})
}

// Move moves a file from one path to another
func (d *LocalDriver) Move(from, to string) error {
	fromRel, err := normalizeRelative(from)
	if err != nil {
		return err
	}
	toRel, err := normalizeRelative(to)
	if err != nil {
		return err
	}
	// Try rename first (same-filesystem, atomic). If it fails we fall
	// back to copy+delete. Both branches stay inside the root.
	renameErr := d.withRoot(func(root *os.Root) error {
		if err := mkdirAllIn(root, filepath.Dir(toRel)); err != nil {
			return fmt.Errorf("velocity/storage: create directory: %w", err)
		}
		return root.Rename(fromRel, toRel)
	})
	if renameErr == nil {
		return nil
	}
	if err := d.Copy(from, to); err != nil {
		return err
	}
	return d.Delete(from)
}

// Size returns the size of a file at the given path
func (d *LocalDriver) Size(path string) (int64, error) {
	rel, err := normalizeRelative(path)
	if err != nil {
		return 0, err
	}
	var size int64
	err = d.withRoot(func(root *os.Root) error {
		info, err := root.Stat(rel)
		if err != nil {
			return mapOpenError(err)
		}
		size = info.Size()
		return nil
	})
	return size, err
}

// LastModified returns the last modified time of a file
func (d *LocalDriver) LastModified(path string) (time.Time, error) {
	rel, err := normalizeRelative(path)
	if err != nil {
		return time.Time{}, err
	}
	var t time.Time
	err = d.withRoot(func(root *os.Root) error {
		info, err := root.Stat(rel)
		if err != nil {
			return mapOpenError(err)
		}
		t = info.ModTime()
		return nil
	})
	return t, err
}

// MimeType returns the MIME type of a file
func (d *LocalDriver) MimeType(path string) (string, error) {
	rel, err := normalizeRelative(path)
	if err != nil {
		return "", err
	}
	var detected string
	err = d.withRoot(func(root *os.Root) error {
		file, err := root.Open(rel)
		if err != nil {
			return mapOpenError(err)
		}
		defer file.Close()
		buf := make([]byte, 512)
		n, err := file.Read(buf)
		if err != nil && err != io.EOF {
			return fmt.Errorf("velocity/storage: read file: %w", err)
		}
		detected = http.DetectContentType(buf[:n])
		return nil
	})
	return detected, err
}

// Files lists files in a directory
func (d *LocalDriver) Files(directory string) ([]string, error) {
	rel, err := normalizeRelative(directory)
	if err != nil {
		return nil, err
	}
	var files []string
	err = d.withRoot(func(root *os.Root) error {
		entries, err := readDirIn(root, rel)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("velocity/storage: read directory: %w", mapOpenError(err))
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				files = append(files, filepath.ToSlash(filepath.Join(directory, entry.Name())))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// AllFiles lists all files recursively in a directory
func (d *LocalDriver) AllFiles(directory string) ([]string, error) {
	rel, err := normalizeRelative(directory)
	if err != nil {
		return nil, err
	}
	var files []string
	err = d.withRoot(func(root *os.Root) error {
		return walkRoot(root, rel, func(relPath string, entry fs.DirEntry) error {
			if !entry.IsDir() {
				files = append(files, filepath.ToSlash(relPath))
			}
			return nil
		})
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("velocity/storage: walk directory: %w", err)
	}
	return files, nil
}

// Directories lists directories
func (d *LocalDriver) Directories(directory string) ([]string, error) {
	rel, err := normalizeRelative(directory)
	if err != nil {
		return nil, err
	}
	var dirs []string
	err = d.withRoot(func(root *os.Root) error {
		entries, err := readDirIn(root, rel)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("velocity/storage: read directory: %w", mapOpenError(err))
		}
		for _, entry := range entries {
			if entry.IsDir() {
				dirs = append(dirs, filepath.ToSlash(filepath.Join(directory, entry.Name())))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dirs, nil
}

// AllDirectories lists all directories recursively
func (d *LocalDriver) AllDirectories(directory string) ([]string, error) {
	rel, err := normalizeRelative(directory)
	if err != nil {
		return nil, err
	}
	var dirs []string
	err = d.withRoot(func(root *os.Root) error {
		return walkRoot(root, rel, func(relPath string, entry fs.DirEntry) error {
			if entry.IsDir() && relPath != rel {
				dirs = append(dirs, filepath.ToSlash(relPath))
			}
			return nil
		})
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("velocity/storage: walk directory: %w", err)
	}
	return dirs, nil
}

// MakeDirectory creates a directory
func (d *LocalDriver) MakeDirectory(path string) error {
	rel, err := normalizeRelative(path)
	if err != nil {
		return err
	}
	return d.withRoot(func(root *os.Root) error {
		return mkdirAllIn(root, rel)
	})
}

// DeleteDirectory deletes a directory and all its contents
func (d *LocalDriver) DeleteDirectory(directory string) error {
	rel, err := normalizeRelative(directory)
	if err != nil {
		return err
	}
	return d.withRoot(func(root *os.Root) error {
		if err := root.RemoveAll(rel); err != nil {
			return fmt.Errorf("velocity/storage: remove directory: %w", mapOpenError(err))
		}
		return nil
	})
}

// URL returns the public URL for a file.
//
// Each path segment is URL-escaped independently so that reserved
// characters (`?`, `#`, space, `%`, ...) inside a storage key cannot
// inject query strings, fragments, or invalid bytes into the emitted
// URL. The literal `/` between segments is preserved.
func (d *LocalDriver) URL(path string) string {
	if d.url == "" {
		return ""
	}
	path = strings.ReplaceAll(path, string(filepath.Separator), "/")
	return d.url + "/" + escapeURLPathSegments(path)
}

// escapeURLPathSegments percent-encodes each `/`-delimited segment of
// path so reserved characters in keys cannot inject query / fragment
// state. `url.PathEscape` does NOT escape `/`, so a blanket call would
// destroy the separators; splitting first preserves the path shape
// while encoding every segment individually.
func escapeURLPathSegments(path string) string {
	if path == "" {
		return ""
	}
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		segs[i] = url.PathEscape(seg)
	}
	return strings.Join(segs, "/")
}

// TemporaryURL returns a temporary URL for a file (not supported for local)
func (d *LocalDriver) TemporaryURL(path string, expiration time.Duration) (string, error) {
	return d.URL(path), nil
}

// mkdirAllIn creates directory rel inside root, including intermediate
// components. os.Root.MkdirAll was added in Go 1.25 and uses openat-based
// creation so every component is resolved inside root.
func mkdirAllIn(root *os.Root, rel string) error {
	if rel == "." || rel == "" {
		return nil
	}
	if err := root.MkdirAll(rel, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return mapOpenError(err)
	}
	return nil
}

// readDirIn reads the entries of rel inside root by opening it as a
// directory and calling File.ReadDir — *os.Root has no ReadDir method
// in Go 1.26.
func readDirIn(root *os.Root, rel string) ([]fs.DirEntry, error) {
	if rel == "" {
		rel = "."
	}
	dir, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return dir.ReadDir(-1)
}

// walkRoot walks the subtree rooted at rel inside root, invoking fn for
// every entry found. The path passed to fn is root-relative (what
// AllFiles/AllDirectories want to return). Traversal stays entirely
// inside root because every descent goes through root.Open again.
func walkRoot(root *os.Root, rel string, fn func(string, fs.DirEntry) error) error {
	if rel == "" {
		rel = "."
	}
	stack := []string{rel}
	for len(stack) > 0 {
		n := len(stack) - 1
		current := stack[n]
		stack = stack[:n]

		info, err := root.Stat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			// Leaf — treat it as a single entry.
			if err := fn(current, fs.FileInfoToDirEntry(info)); err != nil {
				return err
			}
			continue
		}
		if current != rel {
			if err := fn(current, fs.FileInfoToDirEntry(info)); err != nil {
				return err
			}
		}
		entries, err := readDirIn(root, current)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			child := filepath.Join(current, entry.Name())
			if entry.IsDir() {
				stack = append(stack, child)
			} else {
				if err := fn(child, entry); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
