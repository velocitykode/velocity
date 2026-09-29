package async

import (
	"context"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// panickingLogger panics on every line.
type panickingLogger struct{}

func (panickingLogger) Debug(string, ...any)          { panic("logger broke") }
func (panickingLogger) Info(string, ...any)           { panic("logger broke") }
func (panickingLogger) Warn(string, ...any)           { panic("logger broke") }
func (panickingLogger) Error(string, ...any)          { panic("logger broke") }
func (panickingLogger) Fatal(string, ...any)          { panic("logger broke") }
func (l panickingLogger) With(...any) contract.Logger { return l }

// A package logger that panics while writing a recovered panic or a GoCtx
// cancellation does not crash the process (the line is written inside the
// helper's own recovery, or on a goroutine with none): the line goes to
// the standalone fallback logger instead.
func TestPackageLogger_PanickingLoggerIsContained(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	t.Cleanup(func() { SetLogger(nil) })
	SetPanicHook(nil)
	SetLogger(panickingLogger{})

	Go(func() { panic("work broke") })
	if n := fallback.Wait("ERROR", "async: panic recovered", 1, 2*time.Second); n != 1 {
		t.Errorf("fallback panic lines = %d, want 1", n)
	}

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	GoCtx(ctx, func(context.Context) { <-release })
	if n := fallback.Wait("ERROR", "async: GoCtx context done", 1, 2*time.Second); n != 1 {
		t.Errorf("fallback GoCtx lines = %d, want 1", n)
	}
}

// GoWithLogger's own logger panicking while writing the recovered panic is
// contained the same way.
func TestGoWithLogger_PanickingLoggerIsContained(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	SetPanicHook(nil)
	GoWithLogger(panickingLogger{}, "worker", func() { panic("work broke") })
	if n := fallback.Wait("ERROR", "async: panic recovered", 1, 2*time.Second); n != 1 {
		t.Errorf("fallback panic lines = %d, want 1", n)
	}
}
