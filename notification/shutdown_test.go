package notification

import (
	"context"
	"errors"
	"testing"
)

// shutdownableChannel satisfies the Channel interface (via the embedded
// contract.NotificationChannel) and records Shutdown calls.
type shutdownableChannel struct {
	Channel
	calls int
	err   error
}

func (c *shutdownableChannel) Shutdown(ctx context.Context) error {
	c.calls++
	return c.err
}

// plainChannel satisfies Channel but not contract.ShutdownAware, so Shutdown
// must skip it.
type plainChannel struct{ Channel }

func TestManagerShutdown_JoinsErrorsAndClears(t *testing.T) {
	m := NewManager()

	errA := errors.New("channel a boom")
	errB := errors.New("channel b boom")
	a := &shutdownableChannel{err: errA}
	b := &shutdownableChannel{err: errB}
	ok := &shutdownableChannel{}
	skip := &plainChannel{}
	m.SetChannel("a", a)
	m.SetChannel("b", b)
	m.SetChannel("ok", ok)
	m.SetChannel("skip", skip)

	err := m.Shutdown(context.Background())
	if err == nil {
		t.Fatal("expected joined error, got nil")
	}

	if a.calls != 1 || b.calls != 1 || ok.calls != 1 {
		t.Fatalf("expected each shutdownable channel called once, got a=%d b=%d ok=%d", a.calls, b.calls, ok.calls)
	}
	if !errors.Is(err, errA) {
		t.Errorf("joined error missing errA: %v", err)
	}
	if !errors.Is(err, errB) {
		t.Errorf("joined error missing errB: %v", err)
	}

	// Registry cleared after shutdown.
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
