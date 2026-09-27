package problem

import (
	"errors"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// A handler built without a logger writes its own warnings, and its default
// LogReporter its reports, through the one framework fallback, and nothing
// through the standard library log package or slog.Default.
func TestNewHandler_WithoutLoggerWritesThroughTheFallback(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	stdlib := fallbacklogtest.CaptureStdlib(t)

	h := NewHandler(WithDebug(true))
	h.Report(errors.New("database unreachable"), nil)

	if got := fallback.Count("WARN", debugForcedOffWarning); got != 1 {
		t.Errorf("fallback WARN lines = %d, want 1: %q", got, fallback.String())
	}
	if got := fallback.Count("ERROR", "database unreachable"); got != 1 {
		t.Errorf("fallback ERROR lines = %d, want 1: %q", got, fallback.String())
	}
	if out := stdlib.String(); out != "" {
		t.Errorf("stdlib log / slog.Default got %q, want nothing", out)
	}
}

// A LogReporter built without a logger reports through the fallback.
func TestLogReporter_WithoutLoggerReportsThroughTheFallback(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)

	NewLogReporter().Report(errors.New("queue push failed"), nil)

	if got := fallback.Count("ERROR", "queue push failed"); got != 1 {
		t.Errorf("fallback lines = %d, want 1: %q", got, fallback.String())
	}
}
