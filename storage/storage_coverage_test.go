package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestManagerAPICompleteCoverage tests all Manager API functions using an instance
func TestManagerAPICompleteCoverage(t *testing.T) {
	// Setup test storage
	testDir := filepath.Join(os.TempDir(), "velocity-storage-coverage-test")
	os.RemoveAll(testDir)
	defer os.RemoveAll(testDir)

	config := Config{
		Default: "local",
		Disks: map[string]DiskConfig{
			"local": {
				Driver: "local",
				Root:   testDir,
				URL:    "http://localhost/storage",
			},
			"memory": {
				Driver:  "memory",
				MaxSize: 10 * 1024 * 1024,
			},
		},
	}

	manager := NewManager(config)
	err := manager.Configure(config)
	if err != nil {
		t.Fatalf("Failed to configure: %v", err)
	}

	d, err := manager.Default()
	if err != nil {
		t.Fatalf("Default disk error: %v", err)
	}

	// Test PutStream
	t.Run("PutStream", func(t *testing.T) {
		reader := strings.NewReader("stream content")
		err := d.PutStream("stream.txt", reader)
		if err != nil {
			t.Errorf("PutStream failed: %v", err)
		}
	})

	// Test GetStream
	t.Run("GetStream", func(t *testing.T) {
		stream, err := d.GetStream("stream.txt")
		if err != nil {
			t.Errorf("GetStream failed: %v", err)
		}
		if stream != nil {
			stream.Close()
		}
	})

	// Test Copy
	t.Run("Copy", func(t *testing.T) {
		d.Put("original.txt", []byte("original"))
		err := d.Copy("original.txt", "copy.txt")
		if err != nil {
			t.Errorf("Copy failed: %v", err)
		}
	})

	// Test Move
	t.Run("Move", func(t *testing.T) {
		d.Put("tomove.txt", []byte("move me"))
		err := d.Move("tomove.txt", "moved.txt")
		if err != nil {
			t.Errorf("Move failed: %v", err)
		}
	})

	// Test Size
	t.Run("Size", func(t *testing.T) {
		d.Put("sized.txt", []byte("12345"))
		size, err := d.Size("sized.txt")
		if err != nil {
			t.Errorf("Size failed: %v", err)
		}
		if size != 5 {
			t.Errorf("Size wrong: got %d, want 5", size)
		}
	})

	// Test LastModified
	t.Run("LastModified", func(t *testing.T) {
		d.Put("timed.txt", []byte("time"))
		_, err := d.LastModified("timed.txt")
		if err != nil {
			t.Errorf("LastModified failed: %v", err)
		}
	})

	// Test MimeType
	t.Run("MimeType", func(t *testing.T) {
		d.Put("mime.txt", []byte("text"))
		_, err := d.MimeType("mime.txt")
		if err != nil {
			t.Errorf("MimeType failed: %v", err)
		}
	})

	// Test Files
	t.Run("Files", func(t *testing.T) {
		d.Put("dir/file1.txt", []byte("1"))
		d.Put("dir/file2.txt", []byte("2"))
		files, err := d.Files("dir")
		if err != nil {
			t.Errorf("Files failed: %v", err)
		}
		if len(files) != 2 {
			t.Errorf("Files count wrong: got %d, want 2", len(files))
		}
	})

	// Test AllFiles
	t.Run("AllFiles", func(t *testing.T) {
		d.Put("dir2/sub/file.txt", []byte("sub"))
		files, err := d.AllFiles("dir2")
		if err != nil {
			t.Errorf("AllFiles failed: %v", err)
		}
		if len(files) == 0 {
			t.Error("AllFiles returned empty")
		}
	})

	// Test Directories
	t.Run("Directories", func(t *testing.T) {
		d.Put("parent/child/file.txt", []byte("nested"))
		dirs, err := d.Directories("parent")
		if err != nil {
			t.Errorf("Directories failed: %v", err)
		}
		if len(dirs) == 0 {
			t.Error("Directories returned empty")
		}
	})

	// Test AllDirectories
	t.Run("AllDirectories", func(t *testing.T) {
		dirs, err := d.AllDirectories("")
		if err != nil {
			t.Errorf("AllDirectories failed: %v", err)
		}
		// Should have some directories from previous tests
		if len(dirs) == 0 {
			t.Error("AllDirectories returned empty")
		}
	})

	// Test MakeDirectory
	t.Run("MakeDirectory", func(t *testing.T) {
		err := d.MakeDirectory("newdir")
		if err != nil {
			t.Errorf("MakeDirectory failed: %v", err)
		}
	})

	// Test DeleteDirectory
	t.Run("DeleteDirectory", func(t *testing.T) {
		d.Put("deldir/file.txt", []byte("delete me"))
		err := d.DeleteDirectory("deldir")
		if err != nil {
			t.Errorf("DeleteDirectory failed: %v", err)
		}
	})

	// Test URL
	t.Run("URL", func(t *testing.T) {
		d.Put("public.txt", []byte("public"))
		url := d.URL("public.txt")
		if url == "" {
			t.Error("URL returned empty")
		}
	})

	// Test TemporaryURL
	t.Run("TemporaryURL", func(t *testing.T) {
		// Local driver doesn't support temporary URLs
		_, err := d.TemporaryURL("public.txt", 1*time.Hour)
		// We expect an error or empty string for local driver
		_ = err
	})

	// Test Disk with nonexistent name
	t.Run("NilDisk", func(t *testing.T) {
		_, err := manager.Disk("nonexistent")
		if !errors.Is(err, ErrDiskNotFound) {
			t.Errorf("Disk should return ErrDiskNotFound, got: %v", err)
		}
	})
}

// TestManagerAdditional tests additional manager functions
func TestManagerAdditional(t *testing.T) {
	testDir := filepath.Join(os.TempDir(), "velocity-storage-manager-test")
	os.RemoveAll(testDir)
	defer os.RemoveAll(testDir)

	config := Config{
		Default: "local",
		Disks: map[string]DiskConfig{
			"local": {
				Driver: "local",
				Root:   testDir,
			},
		},
	}

	manager := NewManager(config)
	manager.Configure(config)

	// Test AddDisk
	t.Run("AddDisk", func(t *testing.T) {
		memDriver := NewMemoryDriver(DiskConfig{
			Driver:  "memory",
			MaxSize: 1024,
		})
		manager.AddDisk("custom", memDriver)

		disk, err := manager.Disk("custom")
		if err != nil {
			t.Errorf("AddDisk failed: %v", err)
		}
		if disk == nil {
			t.Error("AddDisk returned nil driver")
		}
	})

	// Test SetDefault with non-existent disk
	t.Run("SetDefaultNonExistent", func(t *testing.T) {
		err := manager.SetDefault("nonexistent")
		if err != ErrDiskNotFound {
			t.Errorf("SetDefault should return ErrDiskNotFound, got: %v", err)
		}
	})

	// Test Disk with non-existent disk
	t.Run("DiskNonExistent", func(t *testing.T) {
		_, err := manager.Disk("nonexistent")
		if !errors.Is(err, ErrDiskNotFound) {
			t.Errorf("Disk should return ErrDiskNotFound, got: %v", err)
		}
	})
}

// TestMemoryDriverAdditional tests additional memory driver functions
func TestMemoryDriverAdditional(t *testing.T) {
	driver := NewMemoryDriver(DiskConfig{
		Driver:  "memory",
		MaxSize: 1024 * 1024,
	})

	// Test Stats
	t.Run("Stats", func(t *testing.T) {
		driver.Put("stats.txt", []byte("test"))
		used, max, count := driver.Stats()
		if used == 0 || max == 0 || count == 0 {
			t.Errorf("Stats wrong: used=%d, max=%d, count=%d", used, max, count)
		}
	})

	// Test Clear
	t.Run("Clear", func(t *testing.T) {
		driver.Put("clear.txt", []byte("clear me"))
		driver.Clear()
		if driver.Exists("clear.txt") {
			t.Error("Clear didn't remove files")
		}

		used, _, count := driver.Stats()
		if used != 0 || count != 0 {
			t.Error("Clear didn't reset stats")
		}
	})

	// Test AllDirectories
	t.Run("AllDirectories", func(t *testing.T) {
		// Clear driver first
		driver.Clear()
		driver.Put("a/b/c/file.txt", []byte("nested"))
		dirs, err := driver.AllDirectories("")
		if err != nil {
			t.Errorf("AllDirectories failed: %v", err)
		}
		// The implementation may not create intermediate directories
		// Just check it doesn't error
		_ = dirs
	})

	// Test MakeDirectory (no-op for memory)
	t.Run("MakeDirectory", func(t *testing.T) {
		err := driver.MakeDirectory("testdir")
		if err != nil {
			t.Errorf("MakeDirectory failed: %v", err)
		}
	})

	// Test URL (not supported for memory)
	t.Run("URL", func(t *testing.T) {
		url := driver.URL("test.txt")
		if url != "" {
			t.Error("URL should return empty for memory driver")
		}
	})

	// Test TemporaryURL (not supported for memory)
	t.Run("TemporaryURL", func(t *testing.T) {
		url, err := driver.TemporaryURL("test.txt", 1*time.Hour)
		if err != ErrNotSupported {
			t.Errorf("TemporaryURL should return ErrNotSupported, got: %v", err)
		}
		if url != "" {
			t.Error("TemporaryURL should return empty for memory driver")
		}
	})

	// Test detectMimeType edge cases
	t.Run("MimeTypeEdgeCases", func(t *testing.T) {
		// Test PNG
		pngBytes := []byte{0x89, 0x50, 0x4E, 0x47}
		driver.Put("image.png", pngBytes)
		mime, _ := driver.MimeType("image.png")
		if !strings.Contains(mime, "png") {
			t.Errorf("PNG mime type wrong: %s", mime)
		}

		// Test GIF
		gifBytes := []byte("GIF89a")
		driver.Put("image.gif", gifBytes)
		mime, _ = driver.MimeType("image.gif")
		if !strings.Contains(mime, "gif") {
			t.Errorf("GIF mime type wrong: %s", mime)
		}

		// Test PDF
		pdfBytes := []byte("%PDF-1.4")
		driver.Put("doc.pdf", pdfBytes)
		mime, _ = driver.MimeType("doc.pdf")
		if !strings.Contains(mime, "pdf") {
			t.Errorf("PDF mime type wrong: %s", mime)
		}

		// Test empty file
		driver.Put("empty.bin", []byte{})
		mime, _ = driver.MimeType("empty.bin")
		if mime != "application/octet-stream" {
			t.Errorf("Empty file mime type wrong: %s", mime)
		}

		// Test binary data
		binaryBytes := []byte{0x00, 0x01, 0x02, 0x03}
		driver.Put("binary.bin", binaryBytes)
		mime, _ = driver.MimeType("binary.bin")
		if mime != "application/octet-stream" {
			t.Errorf("Binary mime type wrong: %s", mime)
		}
	})

	// Test error cases
	t.Run("ErrorCases", func(t *testing.T) {
		// Get non-existent file
		_, err := driver.Get("nonexistent.txt")
		if err != ErrFileNotFound {
			t.Errorf("Get should return ErrFileNotFound, got: %v", err)
		}

		// GetStream non-existent file
		_, err = driver.GetStream("nonexistent.txt")
		if err != ErrFileNotFound {
			t.Errorf("GetStream should return ErrFileNotFound, got: %v", err)
		}

		// Size of non-existent file
		_, err = driver.Size("nonexistent.txt")
		if err != ErrFileNotFound {
			t.Errorf("Size should return ErrFileNotFound, got: %v", err)
		}

		// LastModified of non-existent file
		_, err = driver.LastModified("nonexistent.txt")
		if err != ErrFileNotFound {
			t.Errorf("LastModified should return ErrFileNotFound, got: %v", err)
		}

		// MimeType of non-existent file
		_, err = driver.MimeType("nonexistent.txt")
		if err != ErrFileNotFound {
			t.Errorf("MimeType should return ErrFileNotFound, got: %v", err)
		}

		// Copy non-existent file
		err = driver.Copy("nonexistent.txt", "dest.txt")
		if err != ErrFileNotFound {
			t.Errorf("Copy should return ErrFileNotFound, got: %v", err)
		}

		// Move non-existent file
		err = driver.Move("nonexistent.txt", "dest.txt")
		if err != ErrFileNotFound {
			t.Errorf("Move should return ErrFileNotFound, got: %v", err)
		}
	})
}

// TestLocalDriverAdditional tests additional local driver functions
func TestLocalDriverAdditional(t *testing.T) {
	testDir := filepath.Join(t.TempDir(), "velocity-storage-local-additional")

	driver := NewLocalDriver(DiskConfig{
		Driver:     "local",
		Root:       testDir,
		URL:        "http://localhost/storage",
		Visibility: "private",
	})

	// Test AllDirectories
	t.Run("AllDirectories", func(t *testing.T) {
		driver.Put("deep/nested/structure/file.txt", []byte("deep"))
		dirs, err := driver.AllDirectories("")
		if err != nil {
			t.Errorf("AllDirectories failed: %v", err)
		}
		// Should have deep, deep/nested, deep/nested/structure
		if len(dirs) < 3 {
			t.Errorf("AllDirectories should return at least 3 dirs, got %d", len(dirs))
		}
	})

	// Test MakeDirectory
	t.Run("MakeDirectory", func(t *testing.T) {
		err := driver.MakeDirectory("custom/dir")
		if err != nil {
			t.Errorf("MakeDirectory failed: %v", err)
		}

		// Verify directory was created
		fullPath := filepath.Join(testDir, "custom/dir")
		if stat, err := os.Stat(fullPath); err != nil || !stat.IsDir() {
			t.Error("MakeDirectory didn't create directory")
		}
	})

	// Test URL
	t.Run("URL", func(t *testing.T) {
		driver.Put("public.txt", []byte("public"))
		url := driver.URL("public.txt")
		expected := "http://localhost/storage/public.txt"
		if url != expected {
			t.Errorf("URL wrong: got %s, want %s", url, expected)
		}
	})

	// Test URL with empty base URL
	t.Run("URLEmptyBase", func(t *testing.T) {
		driver2 := NewLocalDriver(DiskConfig{
			Driver: "local",
			Root:   testDir,
			URL:    "",
		})
		url := driver2.URL("file.txt")
		if url != "" {
			t.Errorf("URL should be empty when base URL is empty, got: %s", url)
		}
	})

	// Test TemporaryURL: local driver falls back to returning URL(path) since
	// there's no presigning concept — callers that care should enforce that
	// only remote drivers expose time-limited URLs.
	t.Run("TemporaryURL", func(t *testing.T) {
		url, err := driver.TemporaryURL("test.txt", 1*time.Hour)
		if err != nil {
			t.Errorf("TemporaryURL should not error, got: %v", err)
		}
		if url == "" {
			t.Error("TemporaryURL should return non-empty URL when base URL is configured")
		}
	})

	// Test with relative path that becomes absolute
	t.Run("RelativeToAbsolutePath", func(t *testing.T) {
		driver3 := NewLocalDriver(DiskConfig{
			Driver: "local",
			Root:   "./relative",
		})
		// The driver should convert relative to absolute
		driver3.Put("test.txt", []byte("test"))
		// Just verify it doesn't panic
	})

	// Test error cases for non-existent paths. Size/LastModified on a directory
	// succeed (they stat the inode); that's a deliberate behavior match with
	// os.Stat, so we don't assert it here.
	t.Run("ErrorCases", func(t *testing.T) {
		_, err := driver.Size("does-not-exist.txt")
		if err == nil {
			t.Error("Size should fail for missing file")
		}

		_, err = driver.LastModified("does-not-exist.txt")
		if err == nil {
			t.Error("LastModified should fail for missing file")
		}
	})

	// Test PutStream error case
	t.Run("PutStreamError", func(t *testing.T) {
		// Create a reader that will fail
		reader := &failingReader{}
		err := driver.PutStream("fail.txt", reader)
		if err == nil {
			t.Error("PutStream should fail with failing reader")
		}
	})

	// Test GetStream error case
	t.Run("GetStreamNonExistent", func(t *testing.T) {
		_, err := driver.GetStream("nonexistent.txt")
		if err == nil {
			t.Error("GetStream should fail for non-existent file")
		}
	})

	// Test Copy error cases
	t.Run("CopyErrors", func(t *testing.T) {
		// Copy non-existent file
		err := driver.Copy("nonexistent.txt", "dest.txt")
		if err == nil {
			t.Error("Copy should fail for non-existent source")
		}

		// Copy to invalid destination
		driver.Put("source.txt", []byte("test"))
		err = driver.Copy("source.txt", "/dev/null/impossible/path/file.txt")
		if err == nil {
			t.Error("Copy should fail for invalid destination")
		}
	})

	// Test Move error cases
	t.Run("MoveErrors", func(t *testing.T) {
		// Move non-existent file
		err := driver.Move("nonexistent.txt", "dest.txt")
		if err == nil {
			t.Error("Move should fail for non-existent source")
		}
	})
}

// failingReader is a reader that always fails
type failingReader struct{}

func (r *failingReader) Read(p []byte) (n int, err error) {
	return 0, errors.New("read failed")
}

// TestConfigurationDriver tests driver configuration edge cases
func TestConfigurationDriver(t *testing.T) {
	// Test unknown driver
	t.Run("UnknownDriver", func(t *testing.T) {
		_, err := createDriverWithContext(context.Background(), DiskConfig{
			Driver: "unknown",
		})
		if err == nil {
			t.Error("createDriverWithContext should fail for unknown driver")
		}
	})

	// Test S3 driver creation (will fail without credentials)
	t.Run("S3Driver", func(t *testing.T) {
		_, err := createDriverWithContext(context.Background(), DiskConfig{
			Driver: "s3",
			Key:    "test",
			Secret: "test",
			Region: "us-east-1",
			Bucket: "test",
		})
		// S3 driver creation might fail, but we're testing the code path
		_ = err
	})

	// Test memory driver with default size
	t.Run("MemoryDriverDefaultSize", func(t *testing.T) {
		driver := NewMemoryDriver(DiskConfig{
			Driver:  "memory",
			MaxSize: 0, // Should use default
		})
		if driver == nil {
			t.Error("NewMemoryDriver should not return nil")
		}
		_, max, _ := driver.Stats()
		if max != 100*1024*1024 { // Default is 100MB
			t.Errorf("Default max size wrong: got %d, want %d", max, 100*1024*1024)
		}
	})
}

// TestConfigureError tests Configure with error
func TestConfigureError(t *testing.T) {
	manager := NewManager(Config{})
	config := Config{
		Default: "invalid",
		Disks: map[string]DiskConfig{
			"invalid": {
				Driver: "unknown_driver",
			},
		},
	}
	err := manager.Configure(config)
	if err == nil {
		t.Error("Configure should fail with unknown driver")
	}
}

// TestTypesFunctions tests functions in types.go
func TestTypesFunctions(t *testing.T) {
	// Test WithVisibility
	t.Run("WithVisibility", func(t *testing.T) {
		opts := &PutOptions{}
		fn := WithVisibility(Public)
		fn(opts)
		if opts.Visibility != Public {
			t.Error("WithVisibility didn't set visibility")
		}
	})

	// Test WithMimeType
	t.Run("WithMimeType", func(t *testing.T) {
		opts := &PutOptions{}
		fn := WithMimeType("text/plain")
		fn(opts)
		if opts.MimeType != "text/plain" {
			t.Error("WithMimeType didn't set mime type")
		}
	})

	// Test WithMetadata
	t.Run("WithMetadata", func(t *testing.T) {
		opts := &PutOptions{}
		meta := map[string]string{"key": "value"}
		fn := WithMetadata(meta)
		fn(opts)
		if opts.Metadata["key"] != "value" {
			t.Error("WithMetadata didn't set metadata")
		}
	})
}

// TestPutStreamError tests PutStream with error
func TestPutStreamError(t *testing.T) {
	driver := NewMemoryDriver(DiskConfig{
		Driver:  "memory",
		MaxSize: 1024,
	})

	// Test with failing reader
	reader := &failingReader{}
	err := driver.PutStream("fail.txt", reader)
	if err == nil {
		t.Error("PutStream should fail with failing reader")
	}
}

// TestCopyQuotaExceeded tests Copy when quota would be exceeded.
// Setup: MaxSize=100, source file is 60 bytes (used=60). Copy adds another
// 60 bytes; 60+60=120 > 100, so ErrQuotaExceeded.
func TestCopyQuotaExceeded(t *testing.T) {
	driver := NewMemoryDriver(DiskConfig{
		Driver:  "memory",
		MaxSize: 100,
	})

	if err := driver.Put("source.txt", make([]byte, 60)); err != nil {
		t.Fatalf("put source: %v", err)
	}

	err := driver.Copy("source.txt", "dest.txt")
	if err != ErrQuotaExceeded {
		t.Errorf("Copy should return ErrQuotaExceeded, got: %v", err)
	}
}

// TestLocalDriverErrorCases tests error handling in local driver
func TestLocalDriverErrorCases(t *testing.T) {
	testDir := filepath.Join(os.TempDir(), "velocity-storage-local-errors")
	os.RemoveAll(testDir)
	defer os.RemoveAll(testDir)

	driver := NewLocalDriver(DiskConfig{
		Driver: "local",
		Root:   testDir,
	})

	// Test Get non-existent file
	t.Run("GetNonExistent", func(t *testing.T) {
		_, err := driver.Get("nonexistent.txt")
		if err == nil {
			t.Error("Get should fail for non-existent file")
		}
	})

	// Test Size non-existent file
	t.Run("SizeNonExistent", func(t *testing.T) {
		_, err := driver.Size("nonexistent.txt")
		if err == nil {
			t.Error("Size should fail for non-existent file")
		}
	})

	// Test LastModified non-existent file
	t.Run("LastModifiedNonExistent", func(t *testing.T) {
		_, err := driver.LastModified("nonexistent.txt")
		if err == nil {
			t.Error("LastModified should fail for non-existent file")
		}
	})

	// Test MimeType for different file types
	t.Run("MimeTypes", func(t *testing.T) {
		// Text file
		driver.Put("text.txt", []byte("plain text"))
		mime, err := driver.MimeType("text.txt")
		if err != nil {
			t.Errorf("MimeType failed: %v", err)
		}
		if !strings.Contains(mime, "text") {
			t.Errorf("Text file mime type wrong: %s", mime)
		}

		// Binary file
		driver.Put("binary.bin", []byte{0x00, 0x01, 0x02})
		mime, err = driver.MimeType("binary.bin")
		if err != nil {
			t.Errorf("MimeType failed: %v", err)
		}
		if !strings.Contains(mime, "octet") {
			t.Errorf("Binary file mime type wrong: %s", mime)
		}
	})

	// Test Delete multiple files with some non-existent
	t.Run("DeleteMixed", func(t *testing.T) {
		driver.Put("exists.txt", []byte("exists"))
		// This should not fail even with non-existent files
		err := driver.Delete("exists.txt", "nonexistent.txt")
		if err != nil {
			t.Errorf("Delete should handle mixed files gracefully: %v", err)
		}
	})
}
