package async

import (
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// With no logger set, the package logger is the one framework fallback.
func TestPackageLogger_DefaultsToTheFallback(t *testing.T) {
	if _, ok := GetLogger().(fallbacklog.Logger); !ok {
		t.Fatalf("GetLogger() = %T, want fallbacklog.Logger", GetLogger())
	}
}

// SetLogger(nil) restores the fallback: a panic recovered afterwards writes
// one ERROR line through it, and nothing through the standard library log
// package or slog.Default.
func TestSetLoggerNil_RecoveredPanicWritesThroughTheFallback(t *testing.T) {
	prev := GetLogger()
	t.Cleanup(func() { SetLogger(prev) })
	fallback := fallbacklogtest.Capture(t)
	stdlib := fallbacklogtest.CaptureStdlib(t)

	SetLogger(nil)
	Go(func() { panic("boom") })

	if got := fallback.Wait("ERROR", "async: panic recovered", 1, 2*time.Second); got != 1 {
		t.Fatalf("fallback lines = %d, want 1: %q", got, fallback.String())
	}
	if s := stdlib.String(); s != "" {
		t.Errorf("stdlib log / slog.Default got %q, want nothing", s)
	}
}
