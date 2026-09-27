package trace

import (
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	logdrivers "github.com/velocitykode/velocity/log/drivers"
)

const randWarning = "velocity/trace: crypto/rand unavailable"

// An entropy outage warns once through the package logger, and through the
// fallback logger when none is set; nothing goes through the standard
// library log.
func TestRandUnavailable_WarnsOnceThroughThePackageLogger(t *testing.T) {
	t.Run("package logger", func(t *testing.T) {
		stdlib := fallbacklogtest.CaptureStdlib(t)
		fallback := fallbacklogtest.Capture(t)
		withRandReader(t, failingReader{})
		out := &fallbacklogtest.Output{}
		SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
		t.Cleanup(func() { SetLogger(nil) })

		for i := 0; i < 3; i++ {
			_ = MustGenerateTraceID()
			_ = MustGenerateSpanID()
		}

		if got := strings.Count(out.String(), "WARN: "+randWarning); got != 1 {
			t.Errorf("package logger warn lines = %d, want 1 (%q)", got, out.String())
		}
		if s := stdlib.String() + fallback.String(); s != "" {
			t.Errorf("stdlib / fallback got %q, want nothing", s)
		}
	})

	t.Run("no logger", func(t *testing.T) {
		stdlib := fallbacklogtest.CaptureStdlib(t)
		fallback := fallbacklogtest.Capture(t)
		withRandReader(t, failingReader{})
		SetLogger(nil)

		_ = MustGenerateTraceID()

		if got := fallback.Count("WARN", randWarning); got != 1 {
			t.Errorf("fallback warn lines = %d, want 1 (%q)", got, fallback.String())
		}
		if s := stdlib.String(); s != "" {
			t.Errorf("stdlib got %q, want nothing", s)
		}
	})
}

// SetLogger(nil) restores the fallback logger; GetLogger returns what
// SetLogger installed.
func TestSetLogger_NilRestoresTheFallback(t *testing.T) {
	t.Cleanup(func() { SetLogger(nil) })
	l := logdrivers.NewConsoleLoggerTo(&fallbacklogtest.Output{}, 0)
	SetLogger(l)
	if got := GetLogger(); got != l {
		t.Errorf("GetLogger() = %T, want the installed logger", got)
	}
	SetLogger(nil)
	if _, ok := GetLogger().(fallbacklog.Logger); !ok {
		t.Errorf("GetLogger() after SetLogger(nil) = %T, want fallbacklog.Logger", GetLogger())
	}
}

// SetLogger may run while request goroutines read the logger: it is held
// under a read-write mutex.
func TestSetLogger_WhileReadingIsSafe(t *testing.T) {
	t.Cleanup(func() { SetLogger(nil) })
	out := &fallbacklogtest.Output{}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
				SetLogger(nil)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = GetLogger()
			}
		}()
	}
	wg.Wait()
}
