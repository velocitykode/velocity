package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

func init() {
	Drivers().Register("memory", func(_ context.Context, cfg DiskConfig) (Driver, error) {
		return NewMemoryDriver(cfg), nil
	})
}

// MemoryFile represents a file in memory
type MemoryFile struct {
	Content      []byte
	LastModified time.Time
	MimeType     string
}

// MemoryDriver implements the Driver interface for in-memory storage
type MemoryDriver struct {
	mu      sync.RWMutex
	files   map[string]*MemoryFile
	maxSize int64
	used    int64
}

// NewMemoryDriver creates a new memory storage driver
func NewMemoryDriver(config DiskConfig) *MemoryDriver {
	maxSize := config.MaxSize
	if maxSize == 0 {
		maxSize = 100 * 1024 * 1024 // Default 100MB
	}

	return &MemoryDriver{
		files:   make(map[string]*MemoryFile),
		maxSize: maxSize,
	}
}

// Put stores content at the given path
func (d *MemoryDriver) Put(path string, contents []byte) error {
	path, err := d.cleanPath(path)
	if err != nil {
		return err
	}
	return d.putCleaned(path, contents)
}

// putCleaned stores content at an already-cleaned path.
func (d *MemoryDriver) putCleaned(path string, contents []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	size := int64(len(contents))

	// Check if we have enough space
	existingFile, exists := d.files[path]
	if exists {
		// Replacing existing file - check if the new size would exceed quota
		// Calculate what the new total usage would be
		newUsed := d.used - int64(len(existingFile.Content)) + size
		if d.maxSize > 0 && newUsed > d.maxSize {
			return ErrQuotaExceeded
		}
		// Update the used space
		d.used = newUsed
	} else {
		// New file
		if d.maxSize > 0 && d.used+size > d.maxSize {
			return ErrQuotaExceeded
		}
		d.used += size
	}

	d.files[path] = &MemoryFile{
		Content:      contents,
		LastModified: time.Now(),
		MimeType:     DetectMimeType(contents),
	}

	return nil
}

// PutStream stores a stream at the given path
func (d *MemoryDriver) PutStream(path string, stream io.Reader) error {
	// Validate the path before consuming the stream so traversal rejection is
	// consistent regardless of stream/quota errors.
	path, err := d.cleanPath(path)
	if err != nil {
		return err
	}

	// Limit stream size to the configured max to prevent unbounded memory usage
	limit := d.maxSize
	if limit <= 0 {
		limit = 100 * 1024 * 1024 // 100MB fallback
	}
	limited := io.LimitReader(stream, limit+1)
	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, limited); err != nil {
		return fmt.Errorf("failed to read stream: %w", err)
	}
	if int64(buf.Len()) > limit {
		return fmt.Errorf("stream exceeds maximum size of %d bytes", limit)
	}

	return d.putCleaned(path, buf.Bytes())
}

// Get retrieves content from the given path
func (d *MemoryDriver) Get(path string) ([]byte, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	path, err := d.cleanPath(path)
	if err != nil {
		return nil, err
	}
	file, exists := d.files[path]
	if !exists {
		return nil, ErrFileNotFound
	}

	// Return a copy to prevent modification
	content := make([]byte, len(file.Content))
	copy(content, file.Content)
	return content, nil
}

// GetStream retrieves a stream from the given path
func (d *MemoryDriver) GetStream(path string) (io.ReadCloser, error) {
	content, err := d.Get(path)
	if err != nil {
		return nil, err
	}

	return io.NopCloser(bytes.NewReader(content)), nil
}

// Exists checks if a file exists at the given path
func (d *MemoryDriver) Exists(path string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()

	path, err := d.cleanPath(path)
	if err != nil {
		return false
	}
	_, exists := d.files[path]
	return exists
}

// Delete removes files at the given paths
func (d *MemoryDriver) Delete(paths ...string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	cleanedPaths := make([]string, len(paths))
	for i, path := range paths {
		cleaned, err := d.cleanPath(path)
		if err != nil {
			return err
		}
		cleanedPaths[i] = cleaned
	}
	for _, cleaned := range cleanedPaths {
		if file, exists := d.files[cleaned]; exists {
			d.used -= int64(len(file.Content))
			delete(d.files, cleaned)
		}
	}
	return nil
}

// Copy copies a file from one path to another
func (d *MemoryDriver) Copy(from, to string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	from, err := d.cleanPath(from)
	if err != nil {
		return err
	}
	to, err = d.cleanPath(to)
	if err != nil {
		return err
	}

	sourceFile, exists := d.files[from]
	if !exists {
		return ErrFileNotFound
	}

	// Check if destination already exists
	var additionalSpace int64
	if existingFile, exists := d.files[to]; exists {
		// We're replacing an existing file
		additionalSpace = int64(len(sourceFile.Content)) - int64(len(existingFile.Content))
	} else {
		// New file
		additionalSpace = int64(len(sourceFile.Content))
	}

	// Check space for copy
	if d.maxSize > 0 && d.used+additionalSpace > d.maxSize {
		return ErrQuotaExceeded
	}

	// Create copy
	newContent := make([]byte, len(sourceFile.Content))
	copy(newContent, sourceFile.Content)

	if existingFile, exists := d.files[to]; exists {
		// Update used space when replacing
		d.used -= int64(len(existingFile.Content))
	}

	d.files[to] = &MemoryFile{
		Content:      newContent,
		LastModified: time.Now(),
		MimeType:     sourceFile.MimeType,
	}
	d.used += int64(len(newContent))

	return nil
}

// Move moves a file from one path to another
func (d *MemoryDriver) Move(from, to string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	from, err := d.cleanPath(from)
	if err != nil {
		return err
	}
	to, err = d.cleanPath(to)
	if err != nil {
		return err
	}

	file, exists := d.files[from]
	if !exists {
		return ErrFileNotFound
	}

	if from == to {
		return nil
	}

	if existingFile, exists := d.files[to]; exists {
		d.used -= int64(len(existingFile.Content))
	}

	// Move is just reassigning the pointer
	d.files[to] = file
	delete(d.files, from)
	file.LastModified = time.Now()

	return nil
}

// Size returns the size of a file at the given path
func (d *MemoryDriver) Size(path string) (int64, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	path, err := d.cleanPath(path)
	if err != nil {
		return 0, err
	}
	file, exists := d.files[path]
	if !exists {
		return 0, ErrFileNotFound
	}

	return int64(len(file.Content)), nil
}

// LastModified returns the last modified time of a file
func (d *MemoryDriver) LastModified(path string) (time.Time, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	path, err := d.cleanPath(path)
	if err != nil {
		return time.Time{}, err
	}
	file, exists := d.files[path]
	if !exists {
		return time.Time{}, ErrFileNotFound
	}

	return file.LastModified, nil
}

// MimeType returns the MIME type of a file
func (d *MemoryDriver) MimeType(path string) (string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	path, err := d.cleanPath(path)
	if err != nil {
		return "", err
	}
	file, exists := d.files[path]
	if !exists {
		return "", ErrFileNotFound
	}

	return file.MimeType, nil
}

// Files lists files in a directory
func (d *MemoryDriver) Files(directory string) ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	directory, err := d.cleanPath(directory)
	if err != nil {
		return nil, err
	}
	if directory != "" && !strings.HasSuffix(directory, "/") {
		directory += "/"
	}

	var files []string
	for path := range d.files {
		// Check if file is in the directory (not in subdirectories)
		if strings.HasPrefix(path, directory) {
			relPath := strings.TrimPrefix(path, directory)
			// Only include files directly in this directory
			if !strings.Contains(relPath, "/") {
				files = append(files, path)
			}
		}
	}

	return files, nil
}

// AllFiles lists all files recursively in a directory
func (d *MemoryDriver) AllFiles(directory string) ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	directory, err := d.cleanPath(directory)
	if err != nil {
		return nil, err
	}
	if directory != "" && !strings.HasSuffix(directory, "/") {
		directory += "/"
	}

	var files []string
	for path := range d.files {
		if directory == "" || strings.HasPrefix(path, directory) {
			files = append(files, path)
		}
	}

	return files, nil
}

// Directories lists directories
func (d *MemoryDriver) Directories(directory string) ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if directory != "" {
		cleaned, err := d.cleanPath(directory)
		if err != nil {
			return nil, err
		}
		directory = cleaned
	}
	prefix := ""
	if directory != "" {
		prefix = directory + "/"
	}

	// Extract directories from file paths
	dirs := make(map[string]bool)
	for path := range d.files {
		// Skip files not in the target directory
		if prefix != "" && !strings.HasPrefix(path, prefix) {
			continue
		}

		// Get the relative path
		relPath := path
		if prefix != "" {
			relPath = strings.TrimPrefix(path, prefix)
		}

		// Find the first directory separator
		if idx := strings.Index(relPath, "/"); idx > 0 {
			dirName := relPath[:idx]
			if prefix == "" {
				dirs[dirName] = true
			} else {
				dirs[directory+"/"+dirName] = true
			}
		}
	}

	result := make([]string, 0, len(dirs))
	for dir := range dirs {
		result = append(result, dir)
	}

	return result, nil
}

// AllDirectories lists all directories recursively
func (d *MemoryDriver) AllDirectories(directory string) ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	directory, err := d.cleanPath(directory)
	if err != nil {
		return nil, err
	}
	if directory != "" && !strings.HasSuffix(directory, "/") {
		directory += "/"
	}

	// Extract all directories from file paths
	dirs := make(map[string]bool)
	for path := range d.files {
		if directory == "" || strings.HasPrefix(path, directory) {
			// Extract all directory levels from path
			parts := strings.Split(path, "/")
			for i := 1; i < len(parts); i++ {
				dirPath := strings.Join(parts[:i], "/")
				if dirPath != "" && (directory == "" || strings.HasPrefix(dirPath+"/", directory)) {
					dirs[dirPath] = true
				}
			}
		}
	}

	result := make([]string, 0, len(dirs))
	for dir := range dirs {
		result = append(result, dir)
	}

	return result, nil
}

// MakeDirectory creates a directory (no-op for memory driver)
func (d *MemoryDriver) MakeDirectory(path string) error {
	if _, err := d.cleanPath(path); err != nil {
		return err
	}
	// Directories are implicit in memory driver
	return nil
}

// DeleteDirectory deletes a directory and all its contents
func (d *MemoryDriver) DeleteDirectory(directory string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	directory, err := d.cleanPath(directory)
	if err != nil {
		return err
	}
	if directory != "" && !strings.HasSuffix(directory, "/") {
		directory += "/"
	}

	// Delete all files in the directory
	for path, file := range d.files {
		if directory == "" || strings.HasPrefix(path, directory) {
			d.used -= int64(len(file.Content))
			delete(d.files, path)
		}
	}

	return nil
}

// URL returns the public URL for a file (not supported for memory)
func (d *MemoryDriver) URL(path string) string {
	// Memory driver doesn't support URLs
	return ""
}

// TemporaryURL returns a temporary URL for a file (not supported for memory)
func (d *MemoryDriver) TemporaryURL(path string, expiration time.Duration) (string, error) {
	// Reject path traversal before reporting lack of support.
	if _, err := d.cleanPath(path); err != nil {
		return "", err
	}
	// Memory driver doesn't support URLs
	return "", ErrNotSupported
}

// cleanPath cleans and normalizes a path, rejecting paths containing
// ".." components to prevent path traversal. This mirrors the s3 driver's
// path policy so that keys accepted by the in-memory driver behave
// identically on a real filesystem-backed disk.
func (d *MemoryDriver) cleanPath(path string) (string, error) {
	// Remove leading/trailing slashes and normalize separators.
	path = strings.Trim(path, "/")
	path = strings.ReplaceAll(path, "\\", "/")

	// Reject path traversal.
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return "", fmt.Errorf("velocity/storage: path traversal detected")
		}
	}

	return path, nil
}

// Stats returns memory usage statistics
func (d *MemoryDriver) Stats() (used int64, max int64, fileCount int) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.used, d.maxSize, len(d.files)
}

// Clear removes all files from memory
func (d *MemoryDriver) Clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.files = make(map[string]*MemoryFile)
	d.used = 0
}
