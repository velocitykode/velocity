package testing

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// StorageAssertions provides assertion methods for storage testing
type StorageAssertions struct {
	storage *FakeStorage
	t       testing.TB
}

// NewStorageAssertions creates a new storage assertions instance
func NewStorageAssertions(storage *FakeStorage, t testing.TB) *StorageAssertions {
	return &StorageAssertions{
		storage: storage,
		t:       t,
	}
}

// Assert returns assertions for the fake storage
func (f *FakeStorage) Assert(t testing.TB) *StorageAssertions {
	return NewStorageAssertions(f, t)
}

// AssertExists verifies that a file exists at the given path
func (a *StorageAssertions) AssertExists(path string) {
	a.t.Helper()

	if !a.storage.Exists(path) {
		a.t.Errorf("Storage assertion failed: file '%s' does not exist", path)
	}
}

// AssertMissing verifies that a file does not exist at the given path
func (a *StorageAssertions) AssertMissing(path string) {
	a.t.Helper()

	if a.storage.Exists(path) {
		a.t.Errorf("Storage assertion failed: file '%s' should not exist", path)
	}
}

// AssertStored verifies that a file was stored with specific content
func (a *StorageAssertions) AssertStored(path string, expectedContent []byte) {
	a.t.Helper()

	content, err := a.storage.Get(path)
	if err != nil {
		a.t.Errorf("Storage assertion failed: could not get file '%s': %v", path, err)
		return
	}

	if !bytes.Equal(content, expectedContent) {
		a.t.Errorf("Storage assertion failed: file '%s' content mismatch\nExpected: %q\nGot: %q",
			path, expectedContent, content)
	}
}

// AssertStoredString verifies that a file was stored with specific string content
func (a *StorageAssertions) AssertStoredString(path string, expectedContent string) {
	a.AssertStored(path, []byte(expectedContent))
}

// AssertCount verifies the number of stored files
func (a *StorageAssertions) AssertCount(expectedCount int) {
	a.t.Helper()

	files := a.storage.GetStoredFiles()
	actualCount := len(files)

	if actualCount != expectedCount {
		a.t.Errorf("Storage assertion failed: expected %d files, got %d", expectedCount, actualCount)
		if actualCount > 0 {
			var paths []string
			for path := range files {
				paths = append(paths, path)
			}
			a.t.Logf("Stored files: %v", paths)
		}
	}
}

// AssertNothingStored verifies that no files were stored
func (a *StorageAssertions) AssertNothingStored() {
	a.AssertCount(0)
}

// AssertSize verifies the size of a stored file
func (a *StorageAssertions) AssertSize(path string, expectedSize int64) {
	a.t.Helper()

	size, err := a.storage.Size(path)
	if err != nil {
		a.t.Errorf("Storage assertion failed: could not get size of '%s': %v", path, err)
		return
	}

	if size != expectedSize {
		a.t.Errorf("Storage assertion failed: file '%s' size mismatch\nExpected: %d bytes\nGot: %d bytes",
			path, expectedSize, size)
	}
}

// AssertMimeType verifies the MIME type of a stored file
func (a *StorageAssertions) AssertMimeType(path string, expectedMimeType string) {
	a.t.Helper()

	mimeType, err := a.storage.MimeType(path)
	if err != nil {
		a.t.Errorf("Storage assertion failed: could not get MIME type of '%s': %v", path, err)
		return
	}

	if mimeType != expectedMimeType {
		a.t.Errorf("Storage assertion failed: file '%s' MIME type mismatch\nExpected: %s\nGot: %s",
			path, expectedMimeType, mimeType)
	}
}

// AssertDirectory verifies that a directory contains specific files
func (a *StorageAssertions) AssertDirectory(directory string, expectedFiles []string) {
	a.t.Helper()

	files, err := a.storage.Files(directory)
	if err != nil {
		a.t.Errorf("Storage assertion failed: could not list files in '%s': %v", directory, err)
		return
	}

	// Create maps for easier comparison
	expectedMap := make(map[string]bool)
	for _, file := range expectedFiles {
		expectedMap[file] = true
	}

	actualMap := make(map[string]bool)
	for _, file := range files {
		actualMap[file] = true
	}

	// Check for missing files
	for file := range expectedMap {
		if !actualMap[file] {
			a.t.Errorf("Storage assertion failed: expected file '%s' not found in directory '%s'",
				file, directory)
		}
	}

	// Check for unexpected files
	for file := range actualMap {
		if !expectedMap[file] {
			a.t.Errorf("Storage assertion failed: unexpected file '%s' found in directory '%s'",
				file, directory)
		}
	}
}

// AssertCopied verifies that a file was copied from one path to another
func (a *StorageAssertions) AssertCopied(from, to string) {
	a.t.Helper()

	// Check source still exists
	if !a.storage.Exists(from) {
		a.t.Errorf("Storage assertion failed: source file '%s' does not exist after copy", from)
	}

	// Check destination exists
	if !a.storage.Exists(to) {
		a.t.Errorf("Storage assertion failed: destination file '%s' does not exist after copy", to)
		return
	}

	// Check content matches
	sourceContent, _ := a.storage.Get(from)
	destContent, _ := a.storage.Get(to)

	if !bytes.Equal(sourceContent, destContent) {
		a.t.Errorf("Storage assertion failed: copied file content mismatch between '%s' and '%s'",
			from, to)
	}
}

// AssertMoved verifies that a file was moved from one path to another
func (a *StorageAssertions) AssertMoved(from, to string) {
	a.t.Helper()

	// Check source no longer exists
	if a.storage.Exists(from) {
		a.t.Errorf("Storage assertion failed: source file '%s' still exists after move", from)
	}

	// Check destination exists
	if !a.storage.Exists(to) {
		a.t.Errorf("Storage assertion failed: destination file '%s' does not exist after move", to)
	}
}

// AssertDeleted verifies that files were deleted
func (a *StorageAssertions) AssertDeleted(paths ...string) {
	a.t.Helper()

	for _, path := range paths {
		if a.storage.Exists(path) {
			a.t.Errorf("Storage assertion failed: file '%s' should have been deleted", path)
		}
	}
}

// AssertOperationCalled verifies that a specific operation was called
func (a *StorageAssertions) AssertOperationCalled(operation string, path string) {
	a.t.Helper()

	assertions := a.storage.GetAssertions()
	found := false

	for _, assertion := range assertions {
		if assertion.operation == operation && assertion.path == path {
			found = true
			break
		}
	}

	if !found {
		a.t.Errorf("Storage assertion failed: operation '%s' was not called for path '%s'",
			operation, path)
	}
}

// AssertOperationCount verifies the number of times an operation was called
func (a *StorageAssertions) AssertOperationCount(operation string, expectedCount int) {
	a.t.Helper()

	assertions := a.storage.GetAssertions()
	count := 0

	for _, assertion := range assertions {
		if assertion.operation == operation {
			count++
		}
	}

	if count != expectedCount {
		a.t.Errorf("Storage assertion failed: operation '%s' was called %d times, expected %d",
			operation, count, expectedCount)
	}
}

// AssertLastOperation verifies the last operation performed
func (a *StorageAssertions) AssertLastOperation(expectedOperation string) {
	a.t.Helper()

	assertions := a.storage.GetAssertions()
	if len(assertions) == 0 {
		a.t.Errorf("Storage assertion failed: no operations were performed")
		return
	}

	lastOp := assertions[len(assertions)-1]
	if lastOp.operation != expectedOperation {
		a.t.Errorf("Storage assertion failed: last operation was '%s', expected '%s'",
			lastOp.operation, expectedOperation)
	}
}

// AssertContains verifies that a file contains specific text
func (a *StorageAssertions) AssertContains(path string, substring string) {
	a.t.Helper()

	content, err := a.storage.Get(path)
	if err != nil {
		a.t.Errorf("Storage assertion failed: could not get file '%s': %v", path, err)
		return
	}

	if !strings.Contains(string(content), substring) {
		a.t.Errorf("Storage assertion failed: file '%s' does not contain '%s'", path, substring)
	}
}

// AssertNotContains verifies that a file does not contain specific text
func (a *StorageAssertions) AssertNotContains(path string, substring string) {
	a.t.Helper()

	content, err := a.storage.Get(path)
	if err != nil {
		a.t.Errorf("Storage assertion failed: could not get file '%s': %v", path, err)
		return
	}

	if strings.Contains(string(content), substring) {
		a.t.Errorf("Storage assertion failed: file '%s' should not contain '%s'", path, substring)
	}
}

// AssertURL verifies that a file has a specific URL
func (a *StorageAssertions) AssertURL(path string, expectedURL string) {
	a.t.Helper()

	url := a.storage.URL(path)
	if url != expectedURL {
		a.t.Errorf("Storage assertion failed: file '%s' URL mismatch\nExpected: %s\nGot: %s",
			path, expectedURL, url)
	}
}

// AssertTemporaryURL verifies that a temporary URL can be generated
func (a *StorageAssertions) AssertTemporaryURL(path string) {
	a.t.Helper()

	url, err := a.storage.TemporaryURL(path, 3600)
	if err != nil {
		a.t.Errorf("Storage assertion failed: could not generate temporary URL for '%s': %v",
			path, err)
		return
	}

	if url == "" {
		a.t.Errorf("Storage assertion failed: empty temporary URL for '%s'", path)
		return
	}

	if !strings.Contains(url, path) {
		a.t.Errorf("Storage assertion failed: temporary URL does not contain path '%s': %s",
			path, url)
	}
}

// AssertDirectoryEmpty verifies that a directory is empty
func (a *StorageAssertions) AssertDirectoryEmpty(directory string) {
	a.t.Helper()

	files, err := a.storage.Files(directory)
	if err != nil {
		a.t.Errorf("Storage assertion failed: could not list files in '%s': %v", directory, err)
		return
	}

	if len(files) > 0 {
		a.t.Errorf("Storage assertion failed: directory '%s' is not empty, contains %d files: %v",
			directory, len(files), files)
	}
}

// AssertDirectoryNotEmpty verifies that a directory is not empty
func (a *StorageAssertions) AssertDirectoryNotEmpty(directory string) {
	a.t.Helper()

	files, err := a.storage.Files(directory)
	if err != nil {
		a.t.Errorf("Storage assertion failed: could not list files in '%s': %v", directory, err)
		return
	}

	if len(files) == 0 {
		a.t.Errorf("Storage assertion failed: directory '%s' is empty", directory)
	}
}

// DumpStoredFiles logs all stored files for debugging
func (a *StorageAssertions) DumpStoredFiles() {
	a.t.Helper()

	files := a.storage.GetStoredFiles()
	if len(files) == 0 {
		a.t.Log("No files stored")
		return
	}

	a.t.Logf("Stored files (%d):", len(files))
	for path, content := range files {
		preview := string(content)
		if len(preview) > 100 {
			preview = preview[:100] + "..."
		}
		a.t.Logf("  %s (%d bytes): %q", path, len(content), preview)
	}
}

// DumpOperations logs all operations for debugging
func (a *StorageAssertions) DumpOperations() {
	a.t.Helper()

	assertions := a.storage.GetAssertions()
	if len(assertions) == 0 {
		a.t.Log("No operations performed")
		return
	}

	a.t.Logf("Operations (%d):", len(assertions))
	for i, op := range assertions {
		metaStr := ""
		if len(op.metadata) > 0 {
			metaStr = fmt.Sprintf(" %v", op.metadata)
		}
		a.t.Logf("  %d. %s on '%s'%s at %s",
			i+1, op.operation, op.path, metaStr, op.timestamp.Format("15:04:05.000"))
	}
}
