package storage

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDriverInterface tests that all drivers implement the interface correctly
func TestDriverInterface(t *testing.T) {
	// Create test directory for local driver
	testDir := filepath.Join(os.TempDir(), "velocity-storage-test")
	os.RemoveAll(testDir)
	defer os.RemoveAll(testDir)

	drivers := map[string]Driver{
		"local": NewLocalDriver(DiskConfig{
			Driver: "local",
			Root:   testDir,
		}),
		"memory": NewMemoryDriver(DiskConfig{
			Driver:  "memory",
			MaxSize: 10 * 1024 * 1024,
		}),
	}

	for name, driver := range drivers {
		t.Run(name, func(t *testing.T) {
			testDriver(t, driver)
		})
	}
}

func testDriver(t *testing.T, driver Driver) {
	// Test Put and Get
	t.Run("PutAndGet", func(t *testing.T) {
		content := []byte("Hello, World!")
		err := driver.Put("test.txt", content)
		if err != nil {
			t.Fatalf("Put failed: %v", err)
		}

		retrieved, err := driver.Get("test.txt")
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}

		if !bytes.Equal(content, retrieved) {
			t.Errorf("Content mismatch: got %s, want %s", retrieved, content)
		}
	})

	// Test Stream operations
	t.Run("StreamOperations", func(t *testing.T) {
		content := "Stream content test"
		reader := strings.NewReader(content)

		err := driver.PutStream("stream.txt", reader)
		if err != nil {
			t.Fatalf("PutStream failed: %v", err)
		}

		stream, err := driver.GetStream("stream.txt")
		if err != nil {
			t.Fatalf("GetStream failed: %v", err)
		}
		defer stream.Close()

		buf := new(bytes.Buffer)
		io.Copy(buf, stream)

		if buf.String() != content {
			t.Errorf("Stream content mismatch: got %s, want %s", buf.String(), content)
		}
	})

	// Test Exists
	t.Run("Exists", func(t *testing.T) {
		driver.Put("exists.txt", []byte("exists"))

		if !driver.Exists("exists.txt") {
			t.Error("File should exist")
		}

		if driver.Exists("nonexistent.txt") {
			t.Error("File should not exist")
		}
	})

	// Test Copy
	t.Run("Copy", func(t *testing.T) {
		content := []byte("copy test")
		driver.Put("original.txt", content)

		err := driver.Copy("original.txt", "copy.txt")
		if err != nil {
			t.Fatalf("Copy failed: %v", err)
		}

		copied, err := driver.Get("copy.txt")
		if err != nil {
			t.Fatalf("Get copied file failed: %v", err)
		}

		if !bytes.Equal(content, copied) {
			t.Error("Copied content doesn't match original")
		}

		// Original should still exist
		if !driver.Exists("original.txt") {
			t.Error("Original file should still exist after copy")
		}
	})

	// Test Move
	t.Run("Move", func(t *testing.T) {
		content := []byte("move test")
		driver.Put("tomove.txt", content)

		err := driver.Move("tomove.txt", "moved.txt")
		if err != nil {
			t.Fatalf("Move failed: %v", err)
		}

		moved, err := driver.Get("moved.txt")
		if err != nil {
			t.Fatalf("Get moved file failed: %v", err)
		}

		if !bytes.Equal(content, moved) {
			t.Error("Moved content doesn't match original")
		}

		// Original should not exist
		if driver.Exists("tomove.txt") {
			t.Error("Original file should not exist after move")
		}
	})

	// Test Delete
	t.Run("Delete", func(t *testing.T) {
		driver.Put("delete1.txt", []byte("delete1"))
		driver.Put("delete2.txt", []byte("delete2"))

		err := driver.Delete("delete1.txt", "delete2.txt")
		if err != nil {
			t.Fatalf("Delete failed: %v", err)
		}

		if driver.Exists("delete1.txt") || driver.Exists("delete2.txt") {
			t.Error("Files should be deleted")
		}
	})

	// Test Size
	t.Run("Size", func(t *testing.T) {
		content := []byte("size test content")
		driver.Put("size.txt", content)

		size, err := driver.Size("size.txt")
		if err != nil {
			t.Fatalf("Size failed: %v", err)
		}

		if size != int64(len(content)) {
			t.Errorf("Size mismatch: got %d, want %d", size, len(content))
		}
	})

	// Test LastModified
	t.Run("LastModified", func(t *testing.T) {
		// Add tolerance for filesystem timestamp precision (some FS have 1s resolution)
		before := time.Now().Add(-1 * time.Second)
		driver.Put("modified.txt", []byte("modified"))
		after := time.Now().Add(1 * time.Second)

		modified, err := driver.LastModified("modified.txt")
		if err != nil {
			t.Fatalf("LastModified failed: %v", err)
		}

		if modified.Before(before) || modified.After(after) {
			t.Errorf("Modified time out of range: %v (should be between %v and %v)",
				modified, before, after)
		}
	})

	// Test MimeType
	t.Run("MimeType", func(t *testing.T) {
		// JPEG magic bytes
		jpegContent := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46}
		driver.Put("image.jpg", jpegContent)

		mimeType, err := driver.MimeType("image.jpg")
		if err != nil {
			t.Fatalf("MimeType failed: %v", err)
		}

		if !strings.Contains(mimeType, "jpeg") && !strings.Contains(mimeType, "jpg") {
			t.Errorf("Unexpected mime type for JPEG: %s", mimeType)
		}
	})

	// Test Directory operations
	t.Run("DirectoryOperations", func(t *testing.T) {
		// Create files in different directories
		driver.Put("dir1/file1.txt", []byte("file1"))
		driver.Put("dir1/file2.txt", []byte("file2"))
		driver.Put("dir1/subdir/file3.txt", []byte("file3"))
		driver.Put("dir2/file4.txt", []byte("file4"))

		// Test Files (non-recursive)
		files, err := driver.Files("dir1")
		if err != nil {
			t.Fatalf("Files failed: %v", err)
		}

		if len(files) != 2 {
			t.Errorf("Expected 2 files in dir1, got %d: %v", len(files), files)
		}

		// Test AllFiles (recursive)
		allFiles, err := driver.AllFiles("dir1")
		if err != nil {
			t.Fatalf("AllFiles failed: %v", err)
		}

		if len(allFiles) != 3 {
			t.Errorf("Expected 3 files in dir1 (recursive), got %d: %v", len(allFiles), allFiles)
		}

		// Test Directories
		dirs, err := driver.Directories("")
		if err != nil {
			t.Fatalf("Directories failed: %v", err)
		}

		// Should have at least dir1 and dir2
		if len(dirs) < 2 {
			t.Errorf("Expected at least 2 directories, got %d: %v", len(dirs), dirs)
		}

		// Test DeleteDirectory
		err = driver.DeleteDirectory("dir1")
		if err != nil {
			t.Fatalf("DeleteDirectory failed: %v", err)
		}

		// Files in dir1 should be gone
		if driver.Exists("dir1/file1.txt") {
			t.Error("Files in deleted directory should not exist")
		}
	})

	// Cleanup
	driver.Delete("test.txt", "stream.txt", "exists.txt", "copy.txt",
		"moved.txt", "size.txt", "modified.txt", "image.jpg")
	driver.DeleteDirectory("dir2")
}

// TestMemoryDriverQuota tests memory driver quota enforcement
func TestMemoryDriverQuota(t *testing.T) {
	driver := NewMemoryDriver(DiskConfig{
		Driver:  "memory",
		MaxSize: 1024, // 1KB limit
	})

	// Should succeed
	smallContent := make([]byte, 500)
	err := driver.Put("small.txt", smallContent)
	if err != nil {
		t.Fatalf("Put small file failed: %v", err)
	}

	// Should fail - would exceed quota
	largeContent := make([]byte, 600)
	err = driver.Put("large.txt", largeContent)
	if err != ErrQuotaExceeded {
		t.Errorf("Expected quota exceeded error, got: %v", err)
	}

	// Replace small file with larger one should fail if it exceeds quota
	largerContent := make([]byte, 1100) // This would exceed the 1024 limit
	err = driver.Put("small.txt", largerContent)
	if err != ErrQuotaExceeded {
		t.Errorf("Expected quota exceeded error when replacing, got: %v", err)
	}

	// But replacing with a file that fits should work
	fitContent := make([]byte, 900)
	err = driver.Put("small.txt", fitContent)
	if err != nil {
		t.Errorf("Expected no error when replacing within quota, got: %v", err)
	}
}

// TestConcurrentAccess tests thread safety
func TestConcurrentAccess(t *testing.T) {
	driver := NewMemoryDriver(DiskConfig{
		Driver:  "memory",
		MaxSize: 10 * 1024 * 1024,
	})

	var wg sync.WaitGroup
	errors := make(chan error, 100)

	// Concurrent writes
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				path := filepath.Join("concurrent", string(rune('0'+id)), "file.txt")
				content := []byte(strings.Repeat(string(rune('A'+id)), 100))
				if err := driver.Put(path, content); err != nil {
					errors <- err
				}
			}
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			time.Sleep(10 * time.Millisecond) // Let some writes happen first
			for j := 0; j < 10; j++ {
				path := filepath.Join("concurrent", string(rune('0'+id)), "file.txt")
				driver.Get(path) // Ignore errors for non-existent files
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Concurrent operation failed: %v", err)
	}
}

// TestManager tests the storage manager
func TestManager(t *testing.T) {
	// Create test directories
	testDir1 := filepath.Join(os.TempDir(), "velocity-storage-test1")
	testDir2 := filepath.Join(os.TempDir(), "velocity-storage-test2")
	os.RemoveAll(testDir1)
	os.RemoveAll(testDir2)
	defer os.RemoveAll(testDir1)
	defer os.RemoveAll(testDir2)

	config := Config{
		Default: "local",
		Disks: map[string]DiskConfig{
			"local": {
				Driver: "local",
				Root:   testDir1,
			},
			"backup": {
				Driver: "local",
				Root:   testDir2,
			},
			"memory": {
				Driver:  "memory",
				MaxSize: 1024 * 1024,
			},
		},
	}

	manager := NewManager(config)
	err := manager.Configure(config)
	if err != nil {
		t.Fatalf("Failed to configure manager: %v", err)
	}

	// Test default disk
	defaultDisk, err := manager.Default()
	if err != nil {
		t.Fatalf("Default disk error: %v", err)
	}

	// Write to default disk
	err = defaultDisk.Put("test.txt", []byte("default disk"))
	if err != nil {
		t.Fatalf("Failed to write to default disk: %v", err)
	}

	// Test specific disk
	backupDisk, err := manager.Disk("backup")
	if err != nil {
		t.Fatalf("Backup disk error: %v", err)
	}

	err = backupDisk.Put("backup.txt", []byte("backup disk"))
	if err != nil {
		t.Fatalf("Failed to write to backup disk: %v", err)
	}

	// Verify files are in different locations
	if _, err := os.Stat(filepath.Join(testDir1, "test.txt")); err != nil {
		t.Error("File not found in default disk location")
	}

	if _, err := os.Stat(filepath.Join(testDir2, "backup.txt")); err != nil {
		t.Error("File not found in backup disk location")
	}

	// Test switching default
	err = manager.SetDefault("memory")
	if err != nil {
		t.Fatalf("Failed to set default: %v", err)
	}

	newDefault, err := manager.Default()
	if err != nil {
		t.Fatalf("Default disk error after SetDefault: %v", err)
	}
	err = newDefault.Put("memory.txt", []byte("memory disk"))
	if err != nil {
		t.Fatalf("Failed to write to memory disk: %v", err)
	}

	// Memory file shouldn't exist on filesystem
	if _, err := os.Stat(filepath.Join(testDir1, "memory.txt")); err == nil {
		t.Error("Memory file shouldn't exist on filesystem")
	}
}

// TestManagerAPI tests the manager storage API using instances
func TestManagerAPI(t *testing.T) {
	// Configure test storage
	testDir := filepath.Join(os.TempDir(), "velocity-storage-global-test")
	os.RemoveAll(testDir)
	defer os.RemoveAll(testDir)

	config := Config{
		Default: "test",
		Disks: map[string]DiskConfig{
			"test": {
				Driver: "local",
				Root:   testDir,
			},
		},
	}

	manager := NewManager(config)
	err := manager.Configure(config)
	if err != nil {
		t.Fatalf("Failed to configure storage: %v", err)
	}

	d, err := manager.Default()
	if err != nil {
		t.Fatalf("Default disk error: %v", err)
	}

	// Test Put
	err = d.Put("global.txt", []byte("global content"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Test Get
	content, err := d.Get("global.txt")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if string(content) != "global content" {
		t.Errorf("Content mismatch: got %s, want global content", content)
	}

	// Test Exists
	if !d.Exists("global.txt") {
		t.Error("File should exist")
	}

	// Test Delete
	err = d.Delete("global.txt")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if d.Exists("global.txt") {
		t.Error("File should be deleted")
	}

	// Test accessing specific disk
	testDisk, err := manager.Disk("test")
	if err != nil {
		t.Fatalf("Test disk error: %v", err)
	}

	err = testDisk.Put("disk.txt", []byte("disk content"))
	if err != nil {
		t.Fatalf("Disk Put failed: %v", err)
	}
}

// BenchmarkPut benchmarks Put operations
func BenchmarkPut(b *testing.B) {
	driver := NewMemoryDriver(DiskConfig{
		Driver:  "memory",
		MaxSize: 100 * 1024 * 1024,
	})

	content := []byte("benchmark content")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := strings.ReplaceAll(filepath.Join("bench", "file.txt"), "\\", "/")
		driver.Put(path, content)
	}
}

// BenchmarkGet benchmarks Get operations
func BenchmarkGet(b *testing.B) {
	driver := NewMemoryDriver(DiskConfig{
		Driver:  "memory",
		MaxSize: 100 * 1024 * 1024,
	})

	content := []byte("benchmark content")
	path := "bench/file.txt"
	driver.Put(path, content)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		driver.Get(path)
	}
}
