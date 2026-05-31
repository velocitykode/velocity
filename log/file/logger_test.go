package file

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileLogger_BasicLogging(t *testing.T) {
	tempDir := t.TempDir()

	logger := NewFileLogger(tempDir, 0, 0)
	defer logger.Shutdown(context.Background())

	logger.Info("test message", "key", "value")
	logger.Error("error message")

	currentDate := time.Now().Format("2006-01-02")
	logFile := filepath.Join(tempDir, "velocity-"+currentDate+".log")

	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	logContent := string(content)
	if !strings.Contains(logContent, "test message") {
		t.Errorf("Log should contain 'test message', got: %s", logContent)
	}
	if !strings.Contains(logContent, "key=value") {
		t.Errorf("Log should contain 'key=value', got: %s", logContent)
	}
	if !strings.Contains(logContent, "error message") {
		t.Errorf("Log should contain 'error message', got: %s", logContent)
	}
}

func TestFileLogger_LevelFiltering(t *testing.T) {
	tempDir := t.TempDir()

	logger := NewFileLogger(tempDir, 0, 2) // warn and above
	defer logger.Shutdown(context.Background())

	logger.Debug("debug message")
	logger.Info("info message")
	logger.Warn("warn message")
	logger.Error("error message")

	currentDate := time.Now().Format("2006-01-02")
	logFile := filepath.Join(tempDir, "velocity-"+currentDate+".log")

	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	logContent := string(content)
	if strings.Contains(logContent, "debug message") {
		t.Errorf("Log should NOT contain 'debug message' at warn level")
	}
	if strings.Contains(logContent, "info message") {
		t.Errorf("Log should NOT contain 'info message' at warn level")
	}
	if !strings.Contains(logContent, "warn message") {
		t.Errorf("Log should contain 'warn message'")
	}
	if !strings.Contains(logContent, "error message") {
		t.Errorf("Log should contain 'error message'")
	}
}

func TestFileLogger_Rotation(t *testing.T) {
	tempDir := t.TempDir()

	logger := NewFileLogger(tempDir, 0, 0)
	defer logger.Shutdown(context.Background())

	logger.Info("message before rotation")

	// Simulate date change by manipulating internal state
	logger.mu.Lock()
	logger.date = "2020-01-01"
	logger.mu.Unlock()

	logger.Info("message after rotation")

	currentDate := time.Now().Format("2006-01-02")
	logFile := filepath.Join(tempDir, "velocity-"+currentDate+".log")

	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	if !strings.Contains(string(content), "message after rotation") {
		t.Errorf("Current log file should contain message after rotation")
	}
}

func TestFileLogger_RetentionCleanup(t *testing.T) {
	tempDir := t.TempDir()

	// Create some old log files
	oldDates := []string{"2020-01-01", "2020-01-02", "2020-01-03"}
	for _, date := range oldDates {
		oldFile := filepath.Join(tempDir, "velocity-"+date+".log")
		if err := os.WriteFile(oldFile, []byte("old log"), 0o600); err != nil {
			t.Fatalf("Failed to create old log file: %v", err)
		}
	}

	logger := NewFileLogger(tempDir, 7, 0) // 7 day retention
	defer logger.Shutdown(context.Background())

	logger.cleanup()

	// Old files should be removed
	for _, date := range oldDates {
		oldFile := filepath.Join(tempDir, "velocity-"+date+".log")
		if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
			t.Errorf("Old log file %s should have been removed", oldFile)
		}
	}
}

func TestFileLogger_ConcurrentWrites(t *testing.T) {
	tempDir := t.TempDir()

	logger := NewFileLogger(tempDir, 0, 0)
	defer logger.Shutdown(context.Background())

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(n int) {
			for j := 0; j < 10; j++ {
				logger.Info("concurrent message")
			}
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	currentDate := time.Now().Format("2006-01-02")
	logFile := filepath.Join(tempDir, "velocity-"+currentDate+".log")

	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	lines := strings.Count(string(content), "concurrent message")
	if lines != 100 {
		t.Errorf("Expected 100 log lines, got %d", lines)
	}
}

func TestFileLogger_ShutdownThenLog(t *testing.T) {
	tempDir := t.TempDir()

	logger := NewFileLogger(tempDir, 0, 0)
	logger.Info("before shutdown")

	if err := logger.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	// Logging after shutdown should reopen the file, not panic
	logger.Info("after shutdown")

	currentDate := time.Now().Format("2006-01-02")
	logFile := filepath.Join(tempDir, "velocity-"+currentDate+".log")

	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	if !strings.Contains(string(content), "after shutdown") {
		t.Errorf("Log should contain 'after shutdown'")
	}
}

func TestFileLogger_FileMode(t *testing.T) {
	tempDir := t.TempDir()

	logger := NewFileLogger(tempDir, 0, 0)
	defer logger.Shutdown(context.Background())

	logger.Info("mode test")

	currentDate := time.Now().Format("2006-01-02")
	logFile := filepath.Join(tempDir, "velocity-"+currentDate+".log")

	info, err := os.Stat(logFile)
	if err != nil {
		t.Fatalf("Failed to stat log file: %v", err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Errorf("Expected log file mode 0o600, got %o", info.Mode().Perm())
	}
}

func TestFileLogger_CustomFileMode(t *testing.T) {
	tempDir := t.TempDir()

	logger := NewFileLogger(tempDir, 0, 0, WithFileMode(0o644))
	defer logger.Shutdown(context.Background())

	logger.Info("custom mode test")

	currentDate := time.Now().Format("2006-01-02")
	logFile := filepath.Join(tempDir, "velocity-"+currentDate+".log")

	info, err := os.Stat(logFile)
	if err != nil {
		t.Fatalf("Failed to stat log file: %v", err)
	}

	if info.Mode().Perm() != 0o644 {
		t.Errorf("Expected log file mode 0o644, got %o", info.Mode().Perm())
	}
}

func TestFileLogger_DirMode(t *testing.T) {
	tempDir := t.TempDir()
	logDir := filepath.Join(tempDir, "logs")

	logger := NewFileLogger(logDir, 0, 0)
	defer logger.Shutdown(context.Background())

	logger.Info("dir mode test")

	info, err := os.Stat(logDir)
	if err != nil {
		t.Fatalf("Failed to stat log dir: %v", err)
	}

	if info.Mode().Perm() != 0o700 {
		t.Errorf("Expected log dir mode 0o700, got %o", info.Mode().Perm())
	}
}

func TestFileLogger_ChmodExistingFile(t *testing.T) {
	tempDir := t.TempDir()

	currentDate := time.Now().Format("2006-01-02")
	logFile := filepath.Join(tempDir, "velocity-"+currentDate+".log")

	// Pre-create a world-readable file
	if err := os.WriteFile(logFile, []byte("preexisting\n"), 0o644); err != nil {
		t.Fatalf("Failed to pre-create log file: %v", err)
	}

	logger := NewFileLogger(tempDir, 0, 0)
	defer logger.Shutdown(context.Background())

	logger.Info("tighten perms")

	info, err := os.Stat(logFile)
	if err != nil {
		t.Fatalf("Failed to stat log file: %v", err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Errorf("Expected log file mode tightened to 0o600, got %o", info.Mode().Perm())
	}
}

func TestFileLogger_ChmodExistingDir(t *testing.T) {
	tempDir := t.TempDir()
	logDir := filepath.Join(tempDir, "logs")

	// Pre-create a world-listable dir
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("Failed to pre-create log dir: %v", err)
	}

	logger := NewFileLogger(logDir, 0, 0)
	defer logger.Shutdown(context.Background())

	logger.Info("tighten dir perms")

	info, err := os.Stat(logDir)
	if err != nil {
		t.Fatalf("Failed to stat log dir: %v", err)
	}

	if info.Mode().Perm() != 0o700 {
		t.Errorf("Expected log dir mode tightened to 0o700, got %o", info.Mode().Perm())
	}
}

func TestDirModeFromFileMode(t *testing.T) {
	cases := []struct {
		fileMode os.FileMode
		wantDir  os.FileMode
	}{
		{0o600, 0o700},
		{0o644, 0o755},
		{0o660, 0o770},
		{0o640, 0o750},
	}

	for _, tc := range cases {
		got := dirModeFromFileMode(tc.fileMode)
		if got != tc.wantDir {
			t.Errorf("dirModeFromFileMode(%o) = %o, want %o", tc.fileMode, got, tc.wantDir)
		}
	}
}
