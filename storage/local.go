package storage

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/drain"
	"github.com/velocitykode/velocity/internal/errchain"
)

// defaultMaxFileSize is the default maximum file size for local storage (100MB)
const defaultMaxFileSize = 100 * 1024 * 1024

// storageFileMode / storageDirMode are the secret-tier permissions applied
// to every file and directory the LocalDriver writes. Uploaded content may
// be user data, signed assets, or partial secrets, so other local users
// must not be able to read it. Public-visibility files are reachable via
// the URL surface, not via the filesystem layout.
const (
	storageFileMode os.FileMode = 0o600
	storageDirMode  os.FileMode = 0o700
)

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

	// own and run admit the driver's work in flight at Shutdown, each
	// PutStream from its start to its rename (internal/drain). The run is
	// made at construction and ends at the first Shutdown: the driver does
	// not start again. closeErr is the root's close error, kept under
	// rootMu for every Shutdown after it.
	own      drain.Owner
	run      *drain.Run
	closeErr error
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
	_ = os.MkdirAll(root, storageDirMode)

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
	d.own.NestedInside((*LocalDriver).PutStream)
	d.run = d.own.NewRun()
	return d
}

// Shutdown stops admitting PutStream calls, waits within ctx for the ones
// in flight to finish, then releases the *os.Root file descriptor.
//
// At ctx it returns ctx's error and closes the root at once, so a write
// still copying its stream cannot commit: it fails with ErrInvalidPath at
// its rename and removes its temp file once its stream returns. A stream
// blocked in its Read holds its write until the Read returns; the
// shutdown completes then, and every later Shutdown returns the result of
// the first. Called from a stream's Read it is refused with an error
// wrapping contract.ErrStopFromOwnWork and changes nothing.
func (d *LocalDriver) Shutdown(ctx context.Context) error {
	return d.own.Stop(ctx, d.run, func() error {
		<-d.run.Idle()
		return d.closeRoot()
	}, func() { _ = d.closeRoot() })
}

// OwnsCaller reports whether the calling goroutine runs work the driver's
// Shutdown waits for: a stream's Read inside PutStream. A stop called from
// it that waits for the driver's Shutdown would wait on itself. Read
// without a lock.
func (d *LocalDriver) OwnsCaller() bool {
	return d.own.Nested()
}

// closeRoot closes the root once and returns the close's error, to this
// call and every later one.
func (d *LocalDriver) closeRoot() error {
	d.rootMu.Lock()
	defer d.rootMu.Unlock()
	if d.rootHandle != nil {
		if err := d.rootHandle.Close(); err != nil {
			d.closeErr = errchain.Errorf("velocity/storage: close local root: %w", err)
		}
		d.rootHandle = nil
	}
	return d.closeErr
}

// withRoot runs fn with the driver's *os.Root under a read lock.
// Returns ErrInvalidPath if the driver was constructed without a usable
// root (e.g. the configured directory could not be created).
func (d *LocalDriver) withRoot(fn func(root *os.Root) error) error {
	d.rootMu.RLock()
	defer d.rootMu.RUnlock()
	if d.rootHandle == nil {
		return errchain.Errorf("velocity/storage: local driver has no open root: %w", ErrInvalidPath)
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
		return "", errchain.Errorf("velocity/storage: absolute path rejected: %w", ErrInvalidPath)
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
	if errors.Is(err, os.ErrNotExist) { //error-inspection-ok: os.Root error, stdlib value, no user method
		return ErrFileNotFound
	}
	return err
}

// Put stores content at the given path
func (d *LocalDriver) Put(path string, contents []byte) error {
	if int64(len(contents)) > d.maxFileSize {
		return errchain.Errorf("velocity/storage: file size %d exceeds maximum of %d bytes: %w", len(contents), d.maxFileSize, ErrQuotaExceeded)
	}
	rel, err := normalizeRelative(path)
	if err != nil {
		return err
	}
	return d.withRoot(func(root *os.Root) error {
		if err := mkdirAllIn(root, filepath.Dir(rel)); err != nil {
			return errchain.Errorf("velocity/storage: create directory: %w", err)
		}
		// Atomic write: a temp file of this write's own, then rename.
		file, tmp, err := createTemp(root, rel)
		if err != nil {
			return errchain.Errorf("velocity/storage: create file: %w", mapOpenError(err))
		}
		committed := false
		defer func() {
			if !committed {
				_ = file.Close()
				_ = root.Remove(tmp)
			}
		}()
		if _, err := file.Write(contents); err != nil {
			return errchain.Errorf("velocity/storage: write file: %w", err)
		}
		if err := file.Close(); err != nil {
			return errchain.Errorf("velocity/storage: close file: %w", err)
		}
		if err := root.Rename(tmp, rel); err != nil {
			return errchain.Errorf("velocity/storage: move file: %w", mapOpenError(err))
		}
		committed = true
		return nil
	})
}

// PutStream stores a stream at the given path.
//
// The stream is user code: its Read can block, panic, or call back into
// the driver. So the driver's lock is held only to create the write's
// temp file and, after the copy, to rename it into place; the copy runs
// without it. The write is admitted as the driver's work in flight:
// Shutdown waits for it, and refuses to be called from its Read. A write
// arriving once Shutdown began, or whose root Shutdown closed at its
// deadline before the rename, fails with ErrInvalidPath and leaves
// nothing behind.
func (d *LocalDriver) PutStream(path string, stream io.Reader) error {
	if !d.run.Admit() {
		return errchain.Errorf("velocity/storage: local driver is shut down: %w", ErrInvalidPath)
	}
	defer d.run.Release()
	rel, err := normalizeRelative(path)
	if err != nil {
		return err
	}
	dir, base := filepath.Dir(rel), filepath.Base(rel)

	// The temp file is created and later renamed through a root of its
	// own directory, not the driver's: it stays usable to remove the temp
	// file after a Shutdown closed the driver's root.
	var dirRoot *os.Root
	var file *os.File
	var tmp string
	err = d.withRoot(func(root *os.Root) error {
		if err := mkdirAllIn(root, dir); err != nil {
			return errchain.Errorf("velocity/storage: create directory: %w", err)
		}
		r, err := root.OpenRoot(dir)
		if err != nil {
			return errchain.Errorf("velocity/storage: open directory: %w", mapOpenError(err))
		}
		f, name, err := createTemp(r, base)
		if err != nil {
			_ = r.Close()
			return errchain.Errorf("velocity/storage: create file: %w", mapOpenError(err))
		}
		dirRoot, file, tmp = r, f, name
		return nil
	})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = file.Close()
			_ = dirRoot.Remove(tmp)
		}
		_ = dirRoot.Close()
	}()

	limited := io.LimitReader(stream, d.maxFileSize+1)
	written, err := io.Copy(file, limited)
	if err != nil {
		return errchain.Errorf("velocity/storage: write stream: %w", err)
	}
	if err := file.Close(); err != nil {
		return errchain.Errorf("velocity/storage: close file: %w", err)
	}
	if written > d.maxFileSize {
		return errchain.Errorf("velocity/storage: stream exceeds maximum size of %d bytes: %w", d.maxFileSize, ErrQuotaExceeded)
	}
	// Under the lock, so the object lands before a Shutdown closes the
	// root, or not at all.
	return d.withRoot(func(*os.Root) error {
		if err := dirRoot.Rename(tmp, base); err != nil {
			return errchain.Errorf("velocity/storage: move file: %w", mapOpenError(err))
		}
		committed = true
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
			if err := root.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) { //error-inspection-ok: os.Root error, stdlib value, no user method
				return errchain.Errorf("velocity/storage: delete %s: %w", path, mapOpenError(err))
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
			return errchain.Errorf("velocity/storage: open source: %w", mapOpenError(err))
		}
		defer source.Close()

		if err := mkdirAllIn(root, filepath.Dir(toRel)); err != nil {
			return errchain.Errorf("velocity/storage: create directory: %w", err)
		}
		dest, err := root.Create(toRel)
		if err != nil {
			return errchain.Errorf("velocity/storage: create destination: %w", mapOpenError(err))
		}
		defer dest.Close()
		// Tighten umask-derived mode (~0o644) down to 0o600 so the
		// copy inherits the same owner-only invariant Put applies on
		// initial write.
		if chmodErr := dest.Chmod(storageFileMode); chmodErr != nil {
			return errchain.Errorf("velocity/storage: chmod destination: %w", chmodErr)
		}
		if _, err := io.Copy(dest, source); err != nil {
			return errchain.Errorf("velocity/storage: copy: %w", err)
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
			return errchain.Errorf("velocity/storage: create directory: %w", err)
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
		detected, err = sniffMimeType(file) //lock-held-ok: reads a framework-owned file opened from the root, no user code
		return err
	})
	return detected, err
}

// sniffMimeType reads up to 512 bytes from r and detects the content
// type. io.ReadFull instead of a single Read: a Read may legally return
// fewer bytes than available, truncating the sniff window and
// misdetecting the type.
func sniffMimeType(r io.Reader) (string, error) {
	buf := make([]byte, 512)
	n, err := io.ReadFull(r, buf)
	if err != nil && !errchain.Is(err, io.EOF) && !errchain.Is(err, io.ErrUnexpectedEOF) {
		return "", errchain.Errorf("velocity/storage: read file: %w", err)
	}
	return http.DetectContentType(buf[:n]), nil
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
			if errors.Is(err, os.ErrNotExist) { //error-inspection-ok: os.Root error, stdlib value, no user method
				return nil
			}
			return errchain.Errorf("velocity/storage: read directory: %w", mapOpenError(err))
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
		if errchain.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, errchain.Errorf("velocity/storage: walk directory: %w", err)
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
			if errors.Is(err, os.ErrNotExist) { //error-inspection-ok: os.Root error, stdlib value, no user method
				return nil
			}
			return errchain.Errorf("velocity/storage: read directory: %w", mapOpenError(err))
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
		if errchain.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, errchain.Errorf("velocity/storage: walk directory: %w", err)
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
			return errchain.Errorf("velocity/storage: remove directory: %w", mapOpenError(err))
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
	return d.url + "/" + EscapeURLPathSegments(path)
}

// TemporaryURL is not supported by the local driver because local URLs are not
// signed or expiring. Callers must handle ErrNotSupported explicitly instead of
// receiving a permanent public URL.
func (d *LocalDriver) TemporaryURL(path string, expiration time.Duration) (string, error) {
	return "", ErrNotSupported
}

// tempMarker separates an object's name from the random suffix of a
// write's temp file.
const tempMarker = ".tmp-"

// createTemp creates the temp file of one write beside name, inside root:
// name + tempMarker + a random suffix, created exclusively, so no stored
// object and no other write's temp file is ever opened or replaced by it.
// The file is left at storageFileMode: the create mode passes through the
// process umask, which can clear owner bits, so it is set again. The
// caller removes the file on every path that does not rename it into
// place.
func createTemp(root *os.Root, name string) (*os.File, string, error) {
	const attempts = 3
	for i := 1; ; i++ {
		tmp := name + tempMarker + rand.Text()
		file, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, storageFileMode)
		if errors.Is(err, fs.ErrExist) && i < attempts { //error-inspection-ok: os.Root error, stdlib value, no user method
			continue
		}
		if err != nil {
			return nil, "", err
		}
		if err := file.Chmod(storageFileMode); err != nil {
			_ = file.Close()
			_ = root.Remove(tmp)
			return nil, "", errchain.Errorf("chmod temp file: %w", err)
		}
		return file, tmp, nil
	}
}

// mkdirAllIn creates directory rel inside root, including intermediate
// components. os.Root.MkdirAll was added in Go 1.25 and uses openat-based
// creation so every component is resolved inside root.
func mkdirAllIn(root *os.Root, rel string) error {
	if rel == "." || rel == "" {
		return nil
	}
	if err := root.MkdirAll(rel, storageDirMode); err != nil && !errors.Is(err, os.ErrExist) { //error-inspection-ok: os.Root error, stdlib value, no user method
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
