package file

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// callbackValue runs fn when a logger formats it.
type callbackValue struct{ fn func() }

func (v callbackValue) String() string { v.fn(); return "value" }

// within fails the test when fn does not return before the deadline.
func within(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal("call did not return: a value's String ran under the file lock")
	}
}

// A value whose String shuts the logger down, or writes through it, is
// formatted before the file lock is taken, so neither deadlocks.
func TestFileLogger_ValueReentersLogger(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(l *FileLogger)
	}{
		{"shutdown", func(l *FileLogger) { _ = l.Shutdown(context.Background()) }},
		{"write", func(l *FileLogger) { l.Info("nested line") }},
		{"bound write", func(l *FileLogger) { l.With("k", "v").Warn("nested bound line") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			l := NewFileLogger(dir, 0, contract.LogLevelDebug)
			within(t, 2*time.Second, func() {
				l.Info("outer line", "v", callbackValue{fn: func() { tc.call(l) }})
			})
			_ = l.Shutdown(context.Background())
		})
	}
}

// A value whose String blocks holds up only its own line: another writer
// and Shutdown proceed meanwhile.
func TestFileLogger_BlockingValueHoldsNoLock(t *testing.T) {
	dir := t.TempDir()
	l := NewFileLogger(dir, 0, contract.LogLevelDebug)
	release := make(chan struct{})
	entered := make(chan struct{})
	go l.Info("blocked line", "v", callbackValue{fn: func() { close(entered); <-release }})
	<-entered
	within(t, 2*time.Second, func() { l.Info("free line") })
	within(t, 2*time.Second, func() { _ = l.Shutdown(context.Background()) })
	close(release)

	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("log dir entries = %v, %v; want one file", entries, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "INFO: free line") {
		t.Errorf("file = %q, want the free line", b)
	}
}

// Writers racing Shutdown: nothing races, no writer hangs, and Shutdown
// returns once.
func TestFileLogger_WritersRaceShutdown(t *testing.T) {
	fallbacklogtest.Capture(t) // late warnings go to the fallback
	l := NewFileLogger(t.TempDir(), 0, contract.LogLevelDebug)
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			for j := 0; j < 200; j++ {
				l.With("w", j).Warn("line", "v", callbackValue{fn: func() {}})
			}
			done <- struct{}{}
		}()
	}
	within(t, 5*time.Second, func() { _ = l.Shutdown(context.Background()) })
	for i := 0; i < 8; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a writer hung")
		}
	}
}
