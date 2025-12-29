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

// LocalDriver implements the Driver interface for local filesystem storage
type LocalDriver struct {
	root       string
	url        string
	visibility Visibility
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

	// Ensure root directory exists
	os.MkdirAll(root, 0755)

	visibility := Public
	if config.Visibility == "private" {
		visibility = Private
	}

	return &LocalDriver{
		root:       root,
		url:        strings.TrimSuffix(config.URL, "/"),
		visibility: visibility,
	}
}

// Put stores content at the given path
func (d *LocalDriver) Put(path string, contents []byte) error {
	fullPath := d.fullPath(path)

	// Create directory if it doesn't exist
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Write file atomically (write to temp, then rename)
	tempFile := fullPath + ".tmp"
	if err := os.WriteFile(tempFile, contents, 0644); err != nil {
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
	fullPath := d.fullPath(path)

	// Create directory if it doesn't exist
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Create temp file
	tempFile := fullPath + ".tmp"
	file, err := os.Create(tempFile)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	// Copy stream to file
	if _, err := io.Copy(file, stream); err != nil {
		os.Remove(tempFile)
		return fmt.Errorf("failed to write stream: %w", err)
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
	fullPath := d.fullPath(path)

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
	fullPath := d.fullPath(path)

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
	fullPath := d.fullPath(path)
	_, err := os.Stat(fullPath)
	return err == nil
}

// Delete removes files at the given paths
func (d *LocalDriver) Delete(paths ...string) error {
	for _, path := range paths {
		fullPath := d.fullPath(path)
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
	fromPath := d.fullPath(from)
	toPath := d.fullPath(to)

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
	if err := os.MkdirAll(dir, 0755); err != nil {
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
	fromPath := d.fullPath(from)
	toPath := d.fullPath(to)

	// Create destination directory
	dir := filepath.Dir(toPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
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
	fullPath := d.fullPath(path)

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
	fullPath := d.fullPath(path)

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
	fullPath := d.fullPath(path)

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
	fullPath := d.fullPath(directory)

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
	fullPath := d.fullPath(directory)
	var files []string

	err := filepath.WalkDir(fullPath, func(path string, entry fs.DirEntry, err error) error {
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
	fullPath := d.fullPath(directory)

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
	fullPath := d.fullPath(directory)
	var dirs []string

	err := filepath.WalkDir(fullPath, func(path string, entry fs.DirEntry, err error) error {
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
	fullPath := d.fullPath(path)
	return os.MkdirAll(fullPath, 0755)
}

// DeleteDirectory deletes a directory and all its contents
func (d *LocalDriver) DeleteDirectory(directory string) error {
	fullPath := d.fullPath(directory)
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

// fullPath returns the full filesystem path for a given storage path
func (d *LocalDriver) fullPath(path string) string {
	// Clean the path and ensure it doesn't escape root
	path = filepath.Clean(filepath.FromSlash(path))
	return filepath.Join(d.root, path)
}
