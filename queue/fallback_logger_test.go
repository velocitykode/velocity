package queue

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// fallbackProbeJob is a job type no other test pushes, so the memory
// driver's once-per-type advisory fires for it.
type fallbackProbeJob struct{}

func (fallbackProbeJob) Handle() error { return nil }
func (fallbackProbeJob) Failed(error)  {}

// A worker built without a logger (or with a nil one) writes nothing when
// constructed and writes its errors through the one framework fallback,
// never through the standard library log package or slog.Default.
func TestNewWorker_WithoutLoggerWritesThroughTheFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []Option
	}{
		{"no option", nil},
		{"nil logger", []Option{WithWorkerLogger(nil)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fallback := fallbacklogtest.Capture(t)
			stdlib := fallbacklogtest.CaptureStdlib(t)

			w := NewWorker(NewMemoryDriver(), "fallback-test", func(Job) error { return nil }, tc.opts...)
			if out := fallback.String(); out != "" {
				t.Errorf("construction wrote %q, want nothing", out)
			}
			w.logger.Error("Worker error", "id", 1, "error", "deserialize failed")

			if got := fallback.Count("ERROR", "Worker error id=1 error=\"deserialize failed\""); got != 1 {
				t.Errorf("fallback lines = %d, want 1: %q", got, fallback.String())
			}
			if out := stdlib.String(); out != "" {
				t.Errorf("stdlib log / slog.Default got %q, want nothing", out)
			}
		})
	}
}

// The memory driver without a logger writes its advisory through the
// fallback.
func TestMemoryDriver_WithoutLoggerWarnsThroughTheFallback(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)

	d := NewMemoryDriver()
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
	if err := d.PushCtx(context.Background(), fallbackProbeJob{}); err != nil {
		t.Fatalf("PushCtx: %v", err)
	}

	if got := fallback.Count("WARN", "velocity/queue: job type does not implement Identifiable"); got != 1 {
		t.Errorf("fallback lines = %d, want 1: %q", got, fallback.String())
	}
}

// Queue signing without a signing logger writes its key diagnostics
// through the fallback.
func TestConfigureSigning_WithoutLoggerWarnsThroughTheFallback(t *testing.T) {
	saveAndRestoreSigningState(t)
	signingMu.RLock()
	prev := signingLogger
	signingMu.RUnlock()
	t.Cleanup(func() { SetSigningLogger(prev) })
	fallback := fallbacklogtest.Capture(t)

	SetSigningLogger(nil)
	if err := ConfigureSigningWith("", "", SigningOptions{AllowUnsignedInDev: true}); err != nil {
		t.Fatalf("ConfigureSigningWith: %v", err)
	}

	if got := fallback.Count("WARN", "velocity/queue: no signing key found"); got != 1 {
		t.Errorf("fallback lines = %d, want 1: %q", got, fallback.String())
	}
}
