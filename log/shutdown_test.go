package log

import (
	"context"
	"errors"
	"testing"
)

// shutdownableLogger is a Logger that also implements Shutdowner, recording
// Shutdown calls.
type shutdownableLogger struct {
	NullLogger
	calls int
	err   error
}

func (l *shutdownableLogger) Shutdown(ctx context.Context) error {
	l.calls++
	return l.err
}

func TestManagerShutdown_JoinsErrorsAndClears(t *testing.T) {
	m := NewManager(LoggingConfig{})

	errA := errors.New("logger a boom")
	errB := errors.New("logger b boom")
	a := &shutdownableLogger{err: errA}
	b := &shutdownableLogger{err: errB}
	ok := &shutdownableLogger{}
	skip := &NullLogger{} // does not implement Shutdowner
	m.mu.Lock()
	m.channels["a"] = a
	m.channels["b"] = b
	m.channels["ok"] = ok
	m.channels["skip"] = skip
	m.mu.Unlock()

	err := m.Shutdown(context.Background())
	if err == nil {
		t.Fatal("expected joined error, got nil")
	}

	if a.calls != 1 || b.calls != 1 || ok.calls != 1 {
		t.Fatalf("expected each shutdownable logger called once, got a=%d b=%d ok=%d", a.calls, b.calls, ok.calls)
	}
	if !errors.Is(err, errA) {
		t.Errorf("joined error missing errA: %v", err)
	}
	if !errors.Is(err, errB) {
		t.Errorf("joined error missing errB: %v", err)
	}

	m.mu.RLock()
	n := len(m.channels)
	m.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected channel map empty after shutdown, got %d", n)
	}

	// Second call is a no-op returning nil without re-calling children.
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown should be nil, got %v", err)
	}
	if a.calls != 1 || b.calls != 1 || ok.calls != 1 {
		t.Fatalf("second Shutdown re-called children: a=%d b=%d ok=%d", a.calls, b.calls, ok.calls)
	}
}
