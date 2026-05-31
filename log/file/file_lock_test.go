package file

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestFileLock_ConcurrentProcesses spins up two child processes that both
// hammer the same log file with WithFileLock enabled; without the flock the
// interleaved writes would corrupt lines. Each child writes N records and the
// parent asserts every line is intact (starts with '[' and is well-formed).
func TestFileLock_MultiProcess(t *testing.T) {
	if os.Getenv("VELOCITY_LOCK_CHILD") == "1" {
		// Child mode: write many locked records to the shared file.
		dir := os.Getenv("VELOCITY_LOCK_DIR")
		logger := NewFileLogger(dir, 0, 0, WithFileLock())
		defer logger.Shutdown(context.TODO())
		for i := 0; i < 200; i++ {
			logger.Info("child record", "pid", os.Getpid(), "i", i)
		}
		return
	}

	tempDir := t.TempDir()

	// Launch two children sharing the same dir.
	cmd1 := exec.Command(os.Args[0], "-test.run", "TestFileLock_MultiProcess")
	cmd1.Env = append(os.Environ(), "VELOCITY_LOCK_CHILD=1", "VELOCITY_LOCK_DIR="+tempDir)
	cmd2 := exec.Command(os.Args[0], "-test.run", "TestFileLock_MultiProcess")
	cmd2.Env = append(os.Environ(), "VELOCITY_LOCK_CHILD=1", "VELOCITY_LOCK_DIR="+tempDir)

	var wg sync.WaitGroup
	wg.Add(2)
	var err1, err2 error
	go func() { defer wg.Done(); err1 = cmd1.Run() }()
	go func() { defer wg.Done(); err2 = cmd2.Run() }()
	wg.Wait()

	if err1 != nil {
		t.Fatalf("child 1 failed: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("child 2 failed: %v", err2)
	}

	currentDate := time.Now().Format("2006-01-02")
	logFile := filepath.Join(tempDir, "velocity-"+currentDate+".log")
	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	lines := splitLines(string(content))
	if len(lines) != 400 {
		t.Errorf("Expected 400 intact lines, got %d", len(lines))
	}
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}
		if line[0] != '[' {
			t.Errorf("line %d is corrupted (does not start with '['): %q", i, line)
		}
	}
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
