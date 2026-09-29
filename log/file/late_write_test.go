package file

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// A warning or error written after Shutdown, through the logger or a
// logger With returned, is not lost: it goes to the standalone fallback
// logger with its bound fields, and the file is not reopened. Debug and
// Info lines follow the fallback's policy (dropped).
func TestFileLogger_LateWarningsAndErrorsReachTheFallback(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	dir := filepath.Join(t.TempDir(), "logs")
	l := NewFileLogger(dir, 0, contract.LogLevelUnset)
	bound := l.With("request_id", "r1")
	if err := l.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	l.Debug("late debug")
	l.Info("late info")
	l.Error("late error", "job", "mail")
	bound.Warn("late bound warn", "attempt", 2)
	l.Fatal("late fatal")

	if n := fallback.Count("ERROR", "late error"); n != 1 {
		t.Errorf("fallback late error lines = %d, want 1", n)
	}
	if n := fallback.Count("WARN", "late bound warn"); n != 1 {
		t.Errorf("fallback late bound warn lines = %d, want 1", n)
	}
	if n := fallback.Count("ERROR", "late fatal"); n != 1 {
		t.Errorf("fallback late fatal lines = %d, want 1 (as ERROR)", n)
	}
	out := fallback.String()
	for _, want := range []string{"job=mail", "request_id=r1 attempt=2"} {
		if !strings.Contains(out, want) {
			t.Errorf("fallback output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "late debug") || strings.Contains(out, "late info") {
		t.Errorf("fallback output carries a Debug or Info line:\n%s", out)
	}
	if got := logFiles(t, dir); len(got) != 0 {
		t.Errorf("log files after late writes = %v, want none (the file was reopened)", got)
	}
}

// A late write below the logger's level stays dropped: closing does not
// widen what the logger writes.
func TestFileLogger_LateWriteBelowTheLevelStaysDropped(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	l := NewFileLogger(filepath.Join(t.TempDir(), "logs"), 0, contract.LogLevelError)
	_ = l.Shutdown(context.Background())
	l.Warn("late warn under an error-level logger")
	if n := fallback.Count("WARN", "late warn under an error-level logger"); n != 0 {
		t.Errorf("fallback lines = %d, want 0", n)
	}
}
