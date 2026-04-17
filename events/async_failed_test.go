package events

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// panicingListener panics the first time Handle is invoked.
type panicingListener struct{}

func (l *panicingListener) Handle(event interface{}) error {
	panic("boom")
}
func (l *panicingListener) ShouldQueue() bool { return false }

// erroringListener returns a non-nil error.
type erroringListener struct{ msg string }

func (l *erroringListener) Handle(event interface{}) error { return fmtErr(l.msg) }
func (l *erroringListener) ShouldQueue() bool              { return false }

type fmtErrType string

func (e fmtErrType) Error() string { return string(e) }

func fmtErr(s string) error { return fmtErrType(s) }

// TestAsyncDispatcher_PanicRecovered covers Task 6b: listener panics must be
// recovered and surfaced as an events.async_failed event through the
// configured failure sink.
func TestAsyncDispatcher_PanicRecovered(t *testing.T) {
	a := NewAsyncDispatcher()

	var (
		mu     sync.Mutex
		events []*AsyncFailed
	)
	a.SetFailureSink(func(e interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		if af, ok := e.(*AsyncFailed); ok {
			events = append(events, af)
		}
		return nil
	})

	if err := a.Push("evt", &panicingListener{}, 0); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// Allow the goroutine to run and report.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		ok := len(events) == 1
		mu.Unlock()
		if ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("expected 1 AsyncFailed, got %d", len(events))
	}
	if events[0].Name() != "events.async_failed" {
		t.Errorf("event Name = %q, want events.async_failed", events[0].Name())
	}
	if !strings.Contains(events[0].Error, "boom") {
		t.Errorf("error field = %q, want substring 'boom'", events[0].Error)
	}
}

// TestAsyncDispatcher_ErrorReported covers Task 6b for the non-panic case —
// listener returning an error also triggers the failure sink.
func TestAsyncDispatcher_ErrorReported(t *testing.T) {
	a := NewAsyncDispatcher()

	ch := make(chan *AsyncFailed, 1)
	a.SetFailureSink(func(e interface{}) error {
		if af, ok := e.(*AsyncFailed); ok {
			ch <- af
		}
		return nil
	})

	_ = a.Push("evt", &erroringListener{msg: "velocity/test: bad"}, 0)

	select {
	case ev := <-ch:
		if !strings.Contains(ev.Error, "velocity/test: bad") {
			t.Errorf("Error = %q, want substring 'velocity/test: bad'", ev.Error)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for AsyncFailed")
	}
}
