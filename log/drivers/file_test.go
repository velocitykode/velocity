package drivers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewFileLogger(t *testing.T) {
	logger := NewFileLogger("/tmp/test-logs", 0, 0)
	if logger == nil {
		t.Fatal("NewFileLogger() returned nil")
	}
	if logger.path != "/tmp/test-logs" {
		t.Errorf("NewFileLogger() path = %v, want /tmp/test-logs", logger.path)
	}
}

func TestFileLogger_ensureFile(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileLogger(tempDir, 0, 0)

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
	logger := NewFileLogger(tempDir, 0, 0)

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
	logger := NewFileLogger(tempDir, 0, 0)

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
	logger := NewFileLogger(tempDir, 0, 0)

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
	if os.Geteuid() == 0 {
		t.Skip("root bypasses filesystem permission checks; this test requires non-root")
	}
	// Use an invalid path that can't be created
	logger := NewFileLogger("/nonexistent/path/that/cannot/be/created", 0, 0)

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
	logger := NewFileLogger(filepath.Join(blockingFile, "logs"), 0, 0)

	// Try to log - should handle the mkdir error
	logger.Info("test message")

	// File should be nil due to error
	if logger.file != nil {
		t.Error("File should be nil when directory cannot be created")
	}
}

func TestFileLogger_FileWriteError(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileLogger(tempDir, 0, 0)

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
	logger := NewFileLogger(tempDir, 0, 0)

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
	if os.Geteuid() == 0 {
		t.Skip("root bypasses filesystem permission checks; this test requires non-root")
	}
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

	logger := NewFileLogger(readOnlyDir, 0, 0)

	// Try to log - should fail to open file in read-only directory
	logger.Info("test message")

	// File should be nil due to open error
	if logger.file != nil {
		t.Error("File should be nil when file cannot be opened in read-only directory")
	}
}

func TestFileLogger_LevelFiltering(t *testing.T) {
	tempDir := t.TempDir()
	// Level 2 = warn: should suppress debug and info
	logger := NewFileLogger(tempDir, 0, 2)

	logger.Debug("debug filtered")
	logger.Info("info filtered")
	logger.Warn("warn visible")
	logger.Error("error visible")

	if logger.file != nil {
		logger.file.Sync()
		logger.file.Close()
	}

	logFile := filepath.Join(tempDir, "velocity-"+time.Now().Format("2006-01-02")+".log")
	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	logContent := string(content)

	if strings.Contains(logContent, "debug filtered") {
		t.Error("Debug message should be filtered at warn level")
	}
	if strings.Contains(logContent, "info filtered") {
		t.Error("Info message should be filtered at warn level")
	}
	if !strings.Contains(logContent, "warn visible") {
		t.Error("Warn message should appear at warn level")
	}
	if !strings.Contains(logContent, "error visible") {
		t.Error("Error message should appear at warn level")
	}
}

func TestFileLogger_Cleanup(t *testing.T) {
	tempDir := t.TempDir()

	// Create fake old log files
	old := []string{
		"velocity-2020-01-01.log",
		"velocity-2020-06-15.log",
	}
	for _, name := range old {
		os.WriteFile(filepath.Join(tempDir, name), []byte("old"), 0644)
	}

	// Create a recent file (today)
	today := time.Now().Format("2006-01-02")
	os.WriteFile(filepath.Join(tempDir, "velocity-"+today+".log"), []byte("recent"), 0644)

	// Create a non-log file that should be ignored
	os.WriteFile(filepath.Join(tempDir, "other.txt"), []byte("keep"), 0644)

	logger := NewFileLogger(tempDir, 7, 0)
	logger.cleanup()

	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("ReadDir error: %v", err)
	}

	remaining := make(map[string]bool)
	for _, e := range entries {
		remaining[e.Name()] = true
	}

	// Old files should be removed
	for _, name := range old {
		if remaining[name] {
			t.Errorf("expected %s to be deleted", name)
		}
	}

	// Today's file should remain
	if !remaining["velocity-"+today+".log"] {
		t.Error("today's log file should not be deleted")
	}

	// Non-log file should remain
	if !remaining["other.txt"] {
		t.Error("non-log file should not be deleted")
	}
}

// TestFileLogger_SanitisesCRLFInValue replays the H-30 threat against
// the file driver: a user-controlled value containing CRLF must not
// produce additional newlines in the emitted bytes. The sanitiser
// substitutes \x0d / \x0a hex escapes so a single log call still
// produces exactly one line.
func TestFileLogger_SanitisesCRLFInValue(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileLogger(tempDir, 0, 0)

	// Simulate the audit scenario: url field carries a percent-decoded
	// CRLF + a forged "[fake-fatal]" record.
	logger.Info("not found", "url", "/vulnpath\r\n[2026-01-01] FATAL: Database deleted")

	if logger.file != nil {
		logger.file.Sync()
		logger.file.Close()
	}

	logFile := filepath.Join(tempDir, "velocity-"+time.Now().Format("2006-01-02")+".log")
	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}
	out := string(content)

	// Exactly one record (one trailing newline from fmt.Fprintln).
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d:\n%s", len(lines), out)
	}

	// CRLF must be escaped, not preserved literally.
	if strings.Contains(out, "/vulnpath\r") || strings.Contains(out, "/vulnpath\n") {
		t.Errorf("file logger preserved literal CRLF (log forgery possible):\n%s", out)
	}
	if !strings.Contains(out, `\x0d\x0a`) {
		t.Errorf("file logger output missing CRLF escape \\x0d\\x0a:\n%s", out)
	}

	// The forged FATAL marker must remain visible but inside one
	// record, so SIEM cannot mistake it for a real log line.
	if !strings.Contains(out, "[2026-01-01] FATAL: Database deleted") {
		t.Errorf("file logger dropped sanitised content entirely:\n%s", out)
	}
}

// TestFileLogger_SanitisesANSIEscape confirms ESC (0x1b) in a kv
// value is escaped, so an operator tailing the log file from a
// terminal cannot have their cursor / colour / window-title state
// driven by an attacker-controlled request field.
func TestFileLogger_SanitisesANSIEscape(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileLogger(tempDir, 0, 0)

	logger.Info("request", "url", "/path\x1b[2J\x1b]0;evil\x07")

	if logger.file != nil {
		logger.file.Sync()
		logger.file.Close()
	}

	logFile := filepath.Join(tempDir, "velocity-"+time.Now().Format("2006-01-02")+".log")
	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}
	out := string(content)

	if strings.ContainsRune(out, 0x1b) {
		t.Errorf("file logger preserved literal ESC (terminal hijack possible):\n%q", out)
	}
	if strings.ContainsRune(out, 0x07) {
		t.Errorf("file logger preserved literal BEL:\n%q", out)
	}
	if !strings.Contains(out, `\x1b`) {
		t.Errorf("file logger output missing ESC escape \\x1b:\n%s", out)
	}
}

// TestFileLogger_SanitisesKey guards the second half of the audit
// fix: a CRLF in a kv key forges a log line just as effectively as
// one in the value.
func TestFileLogger_SanitisesKey(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileLogger(tempDir, 0, 0)

	logger.Info("test", "tainted\nkey", "value")

	if logger.file != nil {
		logger.file.Sync()
		logger.file.Close()
	}

	logFile := filepath.Join(tempDir, "velocity-"+time.Now().Format("2006-01-02")+".log")
	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}
	out := string(content)

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("CRLF in kv key produced %d lines, want 1:\n%s", len(lines), out)
	}
	if !strings.Contains(out, `tainted\x0akey=value`) {
		t.Errorf("file logger did not escape LF in kv key:\n%s", out)
	}
}

// TestFileLogger_SanitisesMessage verifies the msg argument is also
// run through the sanitiser, not just kvs. err.Error() goes here.
func TestFileLogger_SanitisesMessage(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileLogger(tempDir, 0, 0)

	logger.Error("decode failed: /api/users\r\n[FORGED] FATAL: db down")

	if logger.file != nil {
		logger.file.Sync()
		logger.file.Close()
	}

	logFile := filepath.Join(tempDir, "velocity-"+time.Now().Format("2006-01-02")+".log")
	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}
	out := string(content)

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("CRLF in msg produced %d lines, want 1:\n%s", len(lines), out)
	}
	if strings.Contains(out, "decode failed: /api/users\r") {
		t.Errorf("file logger preserved literal CR in msg:\n%q", out)
	}
}

func TestFileLogger_Cleanup_ZeroDays(t *testing.T) {
	tempDir := t.TempDir()

	// Create an old log file
	os.WriteFile(filepath.Join(tempDir, "velocity-2020-01-01.log"), []byte("old"), 0644)

	logger := NewFileLogger(tempDir, 0, 0)
	logger.cleanup()

	// With days=0, nothing should be deleted
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("ReadDir error: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 file, got %d", len(entries))
	}
}
