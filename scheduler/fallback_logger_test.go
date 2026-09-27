package scheduler

import (
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// A scheduler that was never given a logger writes its failures through
// the one framework fallback, and nothing through the standard library log
// package or slog.Default; SetLogger(nil) restores that default.
func TestScheduler_WithoutLoggerWritesThroughTheFallback(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func() *Scheduler
	}{
		{"never set", New},
		{"set back to nil", func() *Scheduler {
			s := New()
			s.SetLogger(&captureLogger{})
			s.SetLogger(nil)
			return s
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fallback := fallbacklogtest.Capture(t)
			stdlib := fallbacklogtest.CaptureStdlib(t)

			s := tc.build()
			s.Before(func() { panic("before hook") })
			s.runDueJobs()

			if got := fallback.Count("ERROR", "velocity/scheduler: scheduler-level callback panicked"); got != 1 {
				t.Errorf("fallback lines = %d, want 1: %q", got, fallback.String())
			}
			if out := stdlib.String(); out != "" {
				t.Errorf("stdlib log / slog.Default got %q, want nothing", out)
			}
		})
	}
}

// A Manager without a logger reports a recovered panic through the fallback.
func TestManager_WithoutLoggerWritesThroughTheFallback(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)

	m := NewManager()
	m.logError("velocity/scheduler: run panic recovered", "error", "boom")

	if got := fallback.Count("ERROR", "velocity/scheduler: run panic recovered"); got != 1 {
		t.Errorf("fallback lines = %d, want 1: %q", got, fallback.String())
	}
}
