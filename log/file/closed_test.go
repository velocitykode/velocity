package file

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// logFiles returns the names of the files in dir; a missing dir has none.
func logFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A write after Shutdown, through the logger or a logger With returned,
// does not reopen the log file: Shutdown is terminal.
func TestFileLogger_WriteAfterShutdownDoesNotReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	l := NewFileLogger(dir, 0, contract.LogLevelUnset)
	bound := l.With("request_id", "r1")
	l.Error("before shutdown")
	if got := logFiles(t, dir); len(got) != 1 {
		t.Fatalf("log files before Shutdown = %v, want one", got)
	}
	if err := l.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	l.Error("after shutdown")
	bound.Warn("after shutdown, bound")
	l.Fatal("after shutdown, fatal")

	if got := logFiles(t, dir); len(got) != 0 {
		t.Fatalf("log files after writes past Shutdown = %v, want none (the file was reopened)", got)
	}
	if err := l.Shutdown(context.Background()); err != nil {
		t.Errorf("second Shutdown = %v, want nil", err)
	}
}

// Writers racing Shutdown never reopen the file once Shutdown returned.
func TestFileLogger_ConcurrentWritesAndShutdown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	l := NewFileLogger(dir, 0, contract.LogLevelUnset)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := l.With("worker", i)
			for j := 0; j < 200; j++ {
				b.Info("line")
			}
		}()
	}
	if err := l.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	wg.Wait()
	l.mu.Lock()
	reopened := l.file != nil
	l.mu.Unlock()
	if reopened {
		t.Fatal("log file open after Shutdown returned and writers finished")
	}
}
