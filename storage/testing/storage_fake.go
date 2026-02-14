package testing

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/velocitykode/velocity/storage"
)

// FakeStorage is a fake storage driver for testing
type FakeStorage struct {
	mu          sync.RWMutex
	files       map[string]*fileData
	assertions  []assertion
	shouldFail  bool
	failMessage string
}

// fileData stores file information
type fileData struct {
	content      []byte
	lastModified time.Time
	mimeType     string
	metadata     map[string]string
}

// assertion records storage operations for verification
type assertion struct {
	operation string
	path      string
	timestamp time.Time
	metadata  map[string]interface{}
}

// StorageFake creates a new fake storage instance for testing
func StorageFake() *FakeStorage {
	return &FakeStorage{
		files:      make(map[string]*fileData),
		assertions: make([]assertion, 0),
	}
}

// Put stores content at the given path
func (f *FakeStorage) Put(path string, contents []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.shouldFail {
		return errors.New(f.failMessage)
	}

	path = cleanPath(path)

	f.files[path] = &fileData{
		content:      contents,
		lastModified: time.Now(),
		mimeType:     detectMimeType(contents),
		metadata:     make(map[string]string),
	}

	f.recordAssertion("put", path, map[string]interface{}{
		"size": len(contents),
	})

	return nil
}

// PutStream stores a stream at the given path
func (f *FakeStorage) PutStream(path string, stream io.Reader) error {
	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, stream); err != nil {
		return fmt.Errorf("failed to read stream: %w", err)
	}

	return f.Put(path, buf.Bytes())
}

// Get retrieves content from the given path
func (f *FakeStorage) Get(path string) ([]byte, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if f.shouldFail {
		return nil, errors.New(f.failMessage)
	}

	path = cleanPath(path)
	file, exists := f.files[path]
	if !exists {
		return nil, storage.ErrFileNotFound
	}

	f.recordAssertion("get", path, nil)

	// Return a copy to prevent modification
	content := make([]byte, len(file.content))
	copy(content, file.content)
	return content, nil
}

// GetStream retrieves a stream from the given path
func (f *FakeStorage) GetStream(path string) (io.ReadCloser, error) {
	content, err := f.Get(path)
	if err != nil {
		return nil, err
	}

	return io.NopCloser(bytes.NewReader(content)), nil
}

// Exists checks if a file exists at the given path
func (f *FakeStorage) Exists(path string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()

	path = cleanPath(path)
	_, exists := f.files[path]

	f.recordAssertion("exists", path, map[string]interface{}{
		"result": exists,
	})

	return exists
}

// Delete removes files at the given paths
func (f *FakeStorage) Delete(paths ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.shouldFail {
		return errors.New(f.failMessage)
	}

	for _, path := range paths {
		path = cleanPath(path)
		delete(f.files, path)

		f.recordAssertion("delete", path, nil)
	}

	return nil
}

// Copy copies a file from one path to another
func (f *FakeStorage) Copy(from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.shouldFail {
		return errors.New(f.failMessage)
	}

	from = cleanPath(from)
	to = cleanPath(to)

	sourceFile, exists := f.files[from]
	if !exists {
		return storage.ErrFileNotFound
	}

	// Create copy
	newContent := make([]byte, len(sourceFile.content))
	copy(newContent, sourceFile.content)

	f.files[to] = &fileData{
		content:      newContent,
		lastModified: time.Now(),
		mimeType:     sourceFile.mimeType,
		metadata:     copyMetadata(sourceFile.metadata),
	}

	f.recordAssertion("copy", from, map[string]interface{}{
		"to": to,
	})

	return nil
}

// Move moves a file from one path to another
func (f *FakeStorage) Move(from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.shouldFail {
		return errors.New(f.failMessage)
	}

	from = cleanPath(from)
	to = cleanPath(to)

	file, exists := f.files[from]
	if !exists {
		return storage.ErrFileNotFound
	}

	f.files[to] = file
	delete(f.files, from)
	file.lastModified = time.Now()

	f.recordAssertion("move", from, map[string]interface{}{
		"to": to,
	})

	return nil
}

// Size returns the size of a file at the given path
func (f *FakeStorage) Size(path string) (int64, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	path = cleanPath(path)
	file, exists := f.files[path]
	if !exists {
		return 0, storage.ErrFileNotFound
	}

	return int64(len(file.content)), nil
}

// LastModified returns the last modified time of a file
func (f *FakeStorage) LastModified(path string) (time.Time, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	path = cleanPath(path)
	file, exists := f.files[path]
	if !exists {
		return time.Time{}, storage.ErrFileNotFound
	}

	return file.lastModified, nil
}

// MimeType returns the MIME type of a file
func (f *FakeStorage) MimeType(path string) (string, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	path = cleanPath(path)
	file, exists := f.files[path]
	if !exists {
		return "", storage.ErrFileNotFound
	}

	return file.mimeType, nil
}

// Files lists files in a directory
func (f *FakeStorage) Files(directory string) ([]string, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	directory = cleanPath(directory)
	if directory != "" && !strings.HasSuffix(directory, "/") {
		directory += "/"
	}

	var files []string
	for path := range f.files {
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
func (f *FakeStorage) AllFiles(directory string) ([]string, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	directory = cleanPath(directory)
	if directory != "" && !strings.HasSuffix(directory, "/") {
		directory += "/"
	}

	var files []string
	for path := range f.files {
		if directory == "" || strings.HasPrefix(path, directory) {
			files = append(files, path)
		}
	}

	return files, nil
}

// Directories lists directories
func (f *FakeStorage) Directories(directory string) ([]string, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	directory = cleanPath(directory)
	prefix := ""
	if directory != "" {
		prefix = directory + "/"
	}

	dirs := make(map[string]bool)
	for path := range f.files {
		if prefix != "" && !strings.HasPrefix(path, prefix) {
			continue
		}

		relPath := path
		if prefix != "" {
			relPath = strings.TrimPrefix(path, prefix)
		}

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
func (f *FakeStorage) AllDirectories(directory string) ([]string, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	directory = cleanPath(directory)
	if directory != "" && !strings.HasSuffix(directory, "/") {
		directory += "/"
	}

	dirs := make(map[string]bool)
	for path := range f.files {
		if directory == "" || strings.HasPrefix(path, directory) {
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

// MakeDirectory creates a directory (no-op for fake storage)
func (f *FakeStorage) MakeDirectory(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.recordAssertion("make_directory", path, nil)
	return nil
}

// DeleteDirectory deletes a directory and all its contents
func (f *FakeStorage) DeleteDirectory(directory string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.shouldFail {
		return errors.New(f.failMessage)
	}

	directory = cleanPath(directory)
	if directory != "" && !strings.HasSuffix(directory, "/") {
		directory += "/"
	}

	// Delete all files in the directory
	for path := range f.files {
		if directory == "" || strings.HasPrefix(path, directory) {
			delete(f.files, path)
		}
	}

	f.recordAssertion("delete_directory", directory, nil)

	return nil
}

// URL returns the public URL for a file
func (f *FakeStorage) URL(path string) string {
	f.mu.RLock()
	defer f.mu.RUnlock()

	path = cleanPath(path)
	if _, exists := f.files[path]; exists {
		return fmt.Sprintf("http://fake-storage.test/%s", path)
	}
	return ""
}

// TemporaryURL returns a temporary URL for a file
func (f *FakeStorage) TemporaryURL(path string, expiration time.Duration) (string, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	path = cleanPath(path)
	if _, exists := f.files[path]; !exists {
		return "", storage.ErrFileNotFound
	}

	expiresAt := time.Now().Add(expiration).Unix()
	return fmt.Sprintf("http://fake-storage.test/%s?expires=%d", path, expiresAt), nil
}

// recordAssertion records an operation for testing assertions
func (f *FakeStorage) recordAssertion(operation, path string, metadata map[string]interface{}) {
	f.assertions = append(f.assertions, assertion{
		operation: operation,
		path:      path,
		timestamp: time.Now(),
		metadata:  metadata,
	})
}

// GetAssertions returns all recorded assertions
func (f *FakeStorage) GetAssertions() []assertion {
	f.mu.RLock()
	defer f.mu.RUnlock()

	// Return a copy
	result := make([]assertion, len(f.assertions))
	copy(result, f.assertions)
	return result
}

// Clear clears all stored files and assertions
func (f *FakeStorage) Clear() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.files = make(map[string]*fileData)
	f.assertions = make([]assertion, 0)
	f.shouldFail = false
	f.failMessage = ""
}

// ShouldFail configures the fake to fail with a specific message
func (f *FakeStorage) ShouldFail(message string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.shouldFail = true
	f.failMessage = message
}

// ShouldSucceed configures the fake to succeed
func (f *FakeStorage) ShouldSucceed() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.shouldFail = false
	f.failMessage = ""
}

// GetStoredFiles returns a map of all stored files
func (f *FakeStorage) GetStoredFiles() map[string][]byte {
	f.mu.RLock()
	defer f.mu.RUnlock()

	result := make(map[string][]byte)
	for path, file := range f.files {
		content := make([]byte, len(file.content))
		copy(content, file.content)
		result[path] = content
	}
	return result
}

// cleanPath cleans and normalizes a path
func cleanPath(path string) string {
	return strings.Trim(filepath.ToSlash(filepath.Clean(path)), "/")
}

// detectMimeType detects the MIME type from content
func detectMimeType(content []byte) string {
	if len(content) == 0 {
		return "application/octet-stream"
	}

	// Simple detection based on magic bytes
	if len(content) > 2 {
		if content[0] == 0xFF && content[1] == 0xD8 && content[2] == 0xFF {
			return "image/jpeg"
		}
		if content[0] == 0x89 && content[1] == 0x50 && content[2] == 0x4E {
			return "image/png"
		}
		if string(content[:3]) == "GIF" {
			return "image/gif"
		}
		if len(content) > 4 && string(content[:4]) == "%PDF" {
			return "application/pdf"
		}
	}

	// Check if it's valid UTF-8 text
	if isValidUTF8(content) {
		return "text/plain"
	}

	return "application/octet-stream"
}

// isValidUTF8 checks if content is valid UTF-8
func isValidUTF8(data []byte) bool {
	return strings.IndexFunc(string(data), func(r rune) bool {
		return r == 0xFFFD || r == 0
	}) == -1
}

// copyMetadata creates a copy of metadata map
func copyMetadata(original map[string]string) map[string]string {
	copy := make(map[string]string)
	for k, v := range original {
		copy[k] = v
	}
	return copy
}
