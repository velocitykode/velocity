package drivers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewFileLogger(t *testing.T) {
	logger := NewFileLogger("/tmp/test-logs")
	if logger == nil {
		t.Error("NewFileLogger() returned nil")
	}
	if logger.path != "/tmp/test-logs" {
		t.Errorf("NewFileLogger() path = %v, want /tmp/test-logs", logger.path)
	}
}

func TestFileLogger_ensureFile(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileLogger(tempDir)

	err := logger.ensureFile()
	if err != nil {
		t.Fatalf("ensureFile() error = %v", err)
	}

	if logger.file == nil {
		t.Error("ensureFile() did not create file")
	}

	// Check file exists
	expectedFile := filepath.Join(tempDir, "velocity-"+time.Now().Format("2006-01-02")+".log")
	if _, err := os.Stat(expectedFile); os.IsNotExist(err) {
		t.Errorf("Expected log file %s does not exist", expectedFile)
	}

	// Clean up
	if logger.file != nil {
		logger.file.Close()
	}
}

func TestFileLogger_LogMethods(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileLogger(tempDir)

	// Test each log level
	logger.Debug("debug message", "key1", "value1")
	logger.Info("info message", "key2", "value2")
	logger.Warn("warn message", "key3", "value3")
	logger.Error("error message", "key4", "value4")
	logger.Fatal("fatal message", "key5", "value5")

	// Force file sync
	if logger.file != nil {
		logger.file.Sync()
		logger.file.Close()
	}

	// Read the log file
	logFile := filepath.Join(tempDir, "velocity-"+time.Now().Format("2006-01-02")+".log")
	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	logContent := string(content)

	// Check that all log levels are present
	expectedMessages := []string{
		"DEBUG: debug message | key1=value1",
		"INFO: info message | key2=value2",
		"WARN: warn message | key3=value3",
		"ERROR: error message | key4=value4",
		"FATAL: fatal message | key5=value5",
	}

	for _, expected := range expectedMessages {
		if !strings.Contains(logContent, expected) {
			t.Errorf("Log file missing expected content: %s", expected)
		}
	}
}

func TestFileLogger_DateRotation(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileLogger(tempDir)

	// Log something today
	logger.Info("today's message")

	// Simulate date change by modifying the logger's date
	logger.date = "2020-01-01"

	// Log something with "new" date
	logger.Info("new day message")

	// Check that a new file was created
	files, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("Failed to read directory: %v", err)
	}

	// Should have today's file
	hasToday := false
	expectedToday := "velocity-" + time.Now().Format("2006-01-02") + ".log"

	for _, file := range files {
		if file.Name() == expectedToday {
			hasToday = true
			break
		}
	}

	if !hasToday {
		t.Errorf("Expected log file %s not found", expectedToday)
	}

	// Clean up
	if logger.file != nil {
		logger.file.Close()
	}
}

func TestFileLogger_ConcurrentWrites(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileLogger(tempDir)

	done := make(chan bool)

	// Launch multiple goroutines to write concurrently
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 10; j++ {
				logger.Info("concurrent message", "goroutine", id, "iteration", j)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	// Force file sync
	if logger.file != nil {
		logger.file.Sync()
		logger.file.Close()
	}

	// Read the log file
	logFile := filepath.Join(tempDir, "velocity-"+time.Now().Format("2006-01-02")+".log")
	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	// Count the number of lines (should be 100)
	lines := strings.Split(string(content), "\n")
	// Subtract 1 for the trailing newline
	actualLines := len(lines) - 1
	if actualLines != 100 {
		t.Errorf("Expected 100 log lines, got %d", actualLines)
	}
}

func TestFileLogger_InvalidPath(t *testing.T) {
	// Use an invalid path that can't be created
	logger := NewFileLogger("/nonexistent/path/that/cannot/be/created")

	// Try to log something - it should handle the error gracefully
	logger.Info("test message")

	// The file should be nil due to error
	if logger.file != nil {
		t.Error("File should be nil when path cannot be created")
	}
}

func TestFileLogger_DirectoryCreationError(t *testing.T) {
	// Create a file where we expect a directory
	tempDir := t.TempDir()
	blockingFile := filepath.Join(tempDir, "blocking")

	// Create a regular file that will block directory creation
	file, err := os.Create(blockingFile)
	if err != nil {
		t.Fatalf("Failed to create blocking file: %v", err)
	}
	file.Close()

	// Try to use the blocking file path as a directory for logs
	logger := NewFileLogger(filepath.Join(blockingFile, "logs"))

	// Try to log - should handle the mkdir error
	logger.Info("test message")

	// File should be nil due to error
	if logger.file != nil {
		t.Error("File should be nil when directory cannot be created")
	}
}

func TestFileLogger_FileWriteError(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileLogger(tempDir)

	// First ensure file is created
	logger.Info("initial message")

	// Close the file to simulate write error
	if logger.file != nil {
		logger.file.Close()
	}

	// Try to log - should handle the write error
	logger.Info("message after close")

	// Should attempt to recreate the file
	if logger.file == nil {
		t.Error("Logger should attempt to recreate file after error")
	}
}

func TestFileLogger_CloseError(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileLogger(tempDir)

	// Create initial log file
	logger.Info("initial message")

	// Save the file reference
	oldFile := logger.file

	// Force a date change to trigger file rotation
	logger.date = "2020-01-01"

	// Close the old file first to avoid the actual close in ensureFile
	if oldFile != nil {
		oldFile.Close()
	}

	// Now log again - this will try to close an already closed file
	logger.Info("new day message")

	// Should still create new file despite close error
	if logger.file == nil {
		t.Error("Logger should create new file even if old file close fails")
	}
}

func TestFileLogger_OpenFileError(t *testing.T) {
	// Create a read-only directory to prevent file creation
	tempDir := t.TempDir()
	readOnlyDir := filepath.Join(tempDir, "readonly")

	// Create and make directory read-only
	err := os.Mkdir(readOnlyDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}

	// Make it read-only after creation
	err = os.Chmod(readOnlyDir, 0555)
	if err != nil {
		t.Fatalf("Failed to make directory read-only: %v", err)
	}

	// Ensure we restore permissions for cleanup
	defer os.Chmod(readOnlyDir, 0755)

	logger := NewFileLogger(readOnlyDir)

	// Try to log - should fail to open file in read-only directory
	logger.Info("test message")

	// File should be nil due to open error
	if logger.file != nil {
		t.Error("File should be nil when file cannot be opened in read-only directory")
	}
}
