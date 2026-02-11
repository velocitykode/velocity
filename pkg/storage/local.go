package storage

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// defaultMaxFileSize is the default maximum file size for local storage (100MB)
const defaultMaxFileSize = 100 * 1024 * 1024

// LocalDriver implements the Driver interface for local filesystem storage
type LocalDriver struct {
	root        string
	url         string
	visibility  Visibility
	maxFileSize int64
}

// NewLocalDriver creates a new local storage driver
func NewLocalDriver(config DiskConfig) *LocalDriver {
	// Ensure root path is absolute
	root := config.Root
	if !filepath.IsAbs(root) {
		if cwd, err := os.Getwd(); err == nil {
			root = filepath.Join(cwd, root)
		}
	}

	// Ensure root directory exists with restricted permissions
	os.MkdirAll(root, 0700)

	visibility := Private
	if config.Visibility == "public" {
		visibility = Public
	}

	maxFileSize := int64(defaultMaxFileSize)
	if config.MaxSize > 0 {
		maxFileSize = config.MaxSize
	}

	return &LocalDriver{
		root:        root,
		url:         strings.TrimSuffix(config.URL, "/"),
		visibility:  visibility,
		maxFileSize: maxFileSize,
	}
}

// Put stores content at the given path
func (d *LocalDriver) Put(path string, contents []byte) error {
	if int64(len(contents)) > d.maxFileSize {
		return fmt.Errorf("file size %d exceeds maximum of %d bytes: %w", len(contents), d.maxFileSize, ErrQuotaExceeded)
	}

	fullPath, err := d.safePath(path)
	if err != nil {
		return err
	}

	// Create directory if it doesn't exist
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Write file atomically (write to temp, then rename)
	tempFile := fullPath + ".tmp"
	if err := os.WriteFile(tempFile, contents, 0600); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	if err := os.Rename(tempFile, fullPath); err != nil {
		os.Remove(tempFile)
		return fmt.Errorf("failed to move file: %w", err)
	}

	return nil
}

// PutStream stores a stream at the given path
func (d *LocalDriver) PutStream(path string, stream io.Reader) error {
	fullPath, err := d.safePath(path)
	if err != nil {
		return err
	}

	// Create directory if it doesn't exist
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Create temp file
	tempFile := fullPath + ".tmp"
	file, err := os.Create(tempFile)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	// Copy stream to file with size limit
	limited := io.LimitReader(stream, d.maxFileSize+1)
	written, err := io.Copy(file, limited)
	if err != nil {
		os.Remove(tempFile)
		return fmt.Errorf("failed to write stream: %w", err)
	}
	if written > d.maxFileSize {
		file.Close()
		os.Remove(tempFile)
		return fmt.Errorf("stream exceeds maximum size of %d bytes: %w", d.maxFileSize, ErrQuotaExceeded)
	}

	// Close and rename
	file.Close()
	if err := os.Rename(tempFile, fullPath); err != nil {
		os.Remove(tempFile)
		return fmt.Errorf("failed to move file: %w", err)
	}

	return nil
}

// Get retrieves content from the given path
func (d *LocalDriver) Get(path string) ([]byte, error) {
	fullPath, err := d.safePath(path)
	if err != nil {
		return nil, err
	}

	contents, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrFileNotFound
		}
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return contents, nil
}

// GetStream retrieves a stream from the given path
func (d *LocalDriver) GetStream(path string) (io.ReadCloser, error) {
	fullPath, err := d.safePath(path)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrFileNotFound
		}
		return nil, fmt.Errorf("failed to open file: %w", err)
	}

	return file, nil
}

// Exists checks if a file exists at the given path
func (d *LocalDriver) Exists(path string) bool {
	fullPath, err := d.safePath(path)
	if err != nil {
		return false
	}
	_, err = os.Stat(fullPath)
	return err == nil
}

// Delete removes files at the given paths
func (d *LocalDriver) Delete(paths ...string) error {
	for _, path := range paths {
		fullPath, err := d.safePath(path)
		if err != nil {
			return err
		}
		if err := os.Remove(fullPath); err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("failed to delete %s: %w", path, err)
			}
		}
	}
	return nil
}

// Copy copies a file from one path to another
func (d *LocalDriver) Copy(from, to string) error {
	fromPath, err := d.safePath(from)
	if err != nil {
		return err
	}
	toPath, err := d.safePath(to)
	if err != nil {
		return err
	}

	// Open source file
	source, err := os.Open(fromPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrFileNotFound
		}
		return fmt.Errorf("failed to open source: %w", err)
	}
	defer source.Close()

	// Create destination directory
	dir := filepath.Dir(toPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Create destination file
	dest, err := os.Create(toPath)
	if err != nil {
		return fmt.Errorf("failed to create destination: %w", err)
	}
	defer dest.Close()

	// Copy content
	if _, err := io.Copy(dest, source); err != nil {
		return fmt.Errorf("failed to copy: %w", err)
	}

	return nil
}

// Move moves a file from one path to another
func (d *LocalDriver) Move(from, to string) error {
	fromPath, err := d.safePath(from)
	if err != nil {
		return err
	}
	toPath, err := d.safePath(to)
	if err != nil {
		return err
	}

	// Create destination directory
	dir := filepath.Dir(toPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Try rename first (most efficient)
	if err := os.Rename(fromPath, toPath); err != nil {
		// If rename fails (e.g., across filesystems), copy and delete
		if err := d.Copy(from, to); err != nil {
			return err
		}
		return d.Delete(from)
	}

	return nil
}

// Size returns the size of a file at the given path
func (d *LocalDriver) Size(path string) (int64, error) {
	fullPath, err := d.safePath(path)
	if err != nil {
		return 0, err
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ErrFileNotFound
		}
		return 0, fmt.Errorf("failed to stat file: %w", err)
	}

	return info.Size(), nil
}

// LastModified returns the last modified time of a file
func (d *LocalDriver) LastModified(path string) (time.Time, error) {
	fullPath, err := d.safePath(path)
	if err != nil {
		return time.Time{}, err
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, ErrFileNotFound
		}
		return time.Time{}, fmt.Errorf("failed to stat file: %w", err)
	}

	return info.ModTime(), nil
}

// MimeType returns the MIME type of a file
func (d *LocalDriver) MimeType(path string) (string, error) {
	fullPath, err := d.safePath(path)
	if err != nil {
		return "", err
	}

	// Read first 512 bytes for content detection
	file, err := os.Open(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrFileNotFound
		}
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	// Detect content type
	contentType := http.DetectContentType(buffer[:n])
	return contentType, nil
}

// Files lists files in a directory
func (d *LocalDriver) Files(directory string) ([]string, error) {
	fullPath, err := d.safePath(directory)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, filepath.Join(directory, entry.Name()))
		}
	}

	return files, nil
}

// AllFiles lists all files recursively in a directory
func (d *LocalDriver) AllFiles(directory string) ([]string, error) {
	fullPath, err := d.safePath(directory)
	if err != nil {
		return nil, err
	}
	var files []string

	err = filepath.WalkDir(fullPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !entry.IsDir() {
			relPath, err := filepath.Rel(d.root, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(relPath))
		}

		return nil
	})

	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to walk directory: %w", err)
	}

	return files, nil
}

// Directories lists directories
func (d *LocalDriver) Directories(directory string) ([]string, error) {
	fullPath, err := d.safePath(directory)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join(directory, entry.Name()))
		}
	}

	return dirs, nil
}

// AllDirectories lists all directories recursively
func (d *LocalDriver) AllDirectories(directory string) ([]string, error) {
	fullPath, err := d.safePath(directory)
	if err != nil {
		return nil, err
	}
	var dirs []string

	err = filepath.WalkDir(fullPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() && path != fullPath {
			relPath, err := filepath.Rel(d.root, path)
			if err != nil {
				return err
			}
			dirs = append(dirs, filepath.ToSlash(relPath))
		}

		return nil
	})

	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to walk directory: %w", err)
	}

	return dirs, nil
}

// MakeDirectory creates a directory
func (d *LocalDriver) MakeDirectory(path string) error {
	fullPath, err := d.safePath(path)
	if err != nil {
		return err
	}
	return os.MkdirAll(fullPath, 0700)
}

// DeleteDirectory deletes a directory and all its contents
func (d *LocalDriver) DeleteDirectory(directory string) error {
	fullPath, err := d.safePath(directory)
	if err != nil {
		return err
	}
	return os.RemoveAll(fullPath)
}

// URL returns the public URL for a file
func (d *LocalDriver) URL(path string) string {
	if d.url == "" {
		return ""
	}
	// Ensure path uses forward slashes for URLs
	path = strings.ReplaceAll(path, string(filepath.Separator), "/")
	return fmt.Sprintf("%s/%s", d.url, path)
}

// TemporaryURL returns a temporary URL for a file (not supported for local)
func (d *LocalDriver) TemporaryURL(path string, expiration time.Duration) (string, error) {
	// Local driver doesn't support temporary URLs
	// In production, you might implement this with signed tokens
	return d.URL(path), nil
}

// safePath returns the full filesystem path for a given storage path.
// It validates that the resolved path stays within the root directory to prevent path traversal.
func (d *LocalDriver) safePath(path string) (string, error) {
	path = filepath.Clean(filepath.FromSlash(path))
	full := filepath.Join(d.root, path)
	cleanRoot := filepath.Clean(d.root) + string(filepath.Separator)
	cleanFull := filepath.Clean(full)
	if cleanFull != filepath.Clean(d.root) && !strings.HasPrefix(cleanFull, cleanRoot) {
		return "", fmt.Errorf("path traversal detected: %w", ErrInvalidPath)
	}
	return cleanFull, nil
}
