package log

import (
	"context"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/internal/panicerr"
)

// panickingShutdownLogger's Shutdown panics.
type panickingShutdownLogger struct {
	NullLogger
	calls int
}

func (l *panickingShutdownLogger) Shutdown(context.Context) error {
	l.calls++
	panic("logger shutdown panicked")
}

// shutdownOf runs shutdown, failing the test when it panics.
func shutdownOf(t *testing.T, shutdown func(context.Context) error) error {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("Shutdown panicked: %v", p)
		}
	}()
	return shutdown(context.Background())
}

// A channel whose Shutdown panics is contained: every other channel is
// still shut down and the panic is the returned error.
func TestManagerShutdown_ContainsAPanickingChannel(t *testing.T) {
	m := NewManager(LoggingConfig{})
	bad := &panickingShutdownLogger{}
	good := []*shutdownableLogger{{}, {}, {}}
	m.mu.Lock()
	m.channels["bad"] = bad
	for i, l := range good {
		m.channels[string(rune('a'+i))] = l
	}
	m.mu.Unlock()

	err := shutdownOf(t, m.Shutdown)
	for i, l := range good {
		if l.calls != 1 {
			t.Errorf("channel %d Shutdown calls = %d, want 1", i, l.calls)
		}
	}
	var pe *panicerr.Error
	if !errors.As(err, &pe) || pe.Recovered() != "logger shutdown panicked" {
		t.Fatalf("Shutdown = %v, want the panic as its error", err)
	}
}

// An owning stack shuts down every child even when one panics or fails,
// and returns every child's error joined, not only the first.
func TestStackLoggerShutdown_ContainsAndJoins(t *testing.T) {
	errA, errB := errors.New("a failed"), errors.New("b failed")
	bad := &panickingShutdownLogger{}
	a, b, ok := &shutdownableLogger{err: errA}, &shutdownableLogger{err: errB}, &shutdownableLogger{}
	stack := NewStackLogger(a, bad, b, ok)

	err := shutdownOf(t, stack.Shutdown)
	if bad.calls != 1 || a.calls != 1 || b.calls != 1 || ok.calls != 1 {
		t.Fatalf("Shutdown calls a=%d bad=%d b=%d ok=%d, want 1 each", a.calls, bad.calls, b.calls, ok.calls)
	}
	if !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Errorf("Shutdown = %v, want both child errors", err)
	}
	if panicerr.AsTyped(err) == nil {
		t.Errorf("Shutdown = %v, want the panic among its errors", err)
	}
}
