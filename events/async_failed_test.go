package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	testsync "github.com/velocitykode/velocity/testing"
	"github.com/velocitykode/velocity/trace"
)

// failureCollector is a listener of AsyncFailed that records every one it
// receives.
type failureCollector struct {
	mu       sync.Mutex
	failures []*AsyncFailed
}

func (c *failureCollector) Handle(_ context.Context, event interface{}) error {
	if failed, ok := event.(*AsyncFailed); ok {
		c.mu.Lock()
		c.failures = append(c.failures, failed)
		c.mu.Unlock()
	}
	return nil
}

func (c *failureCollector) Async() bool { return false }

func (c *failureCollector) snapshot() []*AsyncFailed {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*AsyncFailed(nil), c.failures...)
}

// TestAsyncFailed_DeliveredToItsListeners asserts the AsyncFailed a detached
// delivery dispatches for a failed listener reaches the listeners of
// AsyncFailed, carrying the failure itself with its type (the listener's
// error, or the recovered panic), the event and listener names and the
// caller's trace IDs.
func TestAsyncFailed_DeliveredToItsListeners(t *testing.T) {
	d := NewDispatcher()
	collector := &failureCollector{}
	d.Listen(&AsyncFailed{}, collector)
	d.Listen("evt", failingListener{})
	d.Listen("evt", panickingListener{})

	ctx := trace.WithTrace(context.Background(), "trace-async-failed", "span-async-failed")
	if err := d.DispatchAsync(ctx, "evt"); err != nil {
		t.Fatalf("DispatchAsync: %v", err)
	}
	testsync.Eventually(t, func() bool { return len(collector.snapshot()) == 2 }, 2*time.Second, "AsyncFailed delivered per failed listener")

	for _, failed := range collector.snapshot() {
		if failed.EventName != "evt" {
			t.Errorf("EventName = %q, want evt", failed.EventName)
		}
		if failed.TraceID != "trace-async-failed" || failed.SpanID != "span-async-failed" {
			t.Errorf("trace IDs = %q/%q, want the caller's", failed.TraceID, failed.SpanID)
		}
		if got := trace.GetTraceID(failed.Context); got != "trace-async-failed" {
			t.Errorf("Context trace id = %q, want the caller's", got)
		}
		if failed.Err == nil {
			t.Error("Err is nil, want the listener's failure")
		}
		switch failed.ListenerName {
		case fmt.Sprintf("%T", failingListener{}):
			if !errors.Is(failed.Err, errListenerBroke) {
				t.Errorf("Err = %v, want the listener's own error", failed.Err)
			}
		case fmt.Sprintf("%T", panickingListener{}):
			var rp contract.RecoveredPanic
			if !errors.As(failed.Err, &rp) || rp.Recovered() != "listener exploded" {
				t.Errorf("Err = %#v, want the recovered panic", failed.Err)
			}
		default:
			t.Errorf("ListenerName = %q, want one of the failing listeners", failed.ListenerName)
		}
	}
}

// alwaysFailingListener fails on every event it is handed and counts them.
type alwaysFailingListener struct{ calls atomic.Int32 }

func (l *alwaysFailingListener) Handle(context.Context, interface{}) error {
	l.calls.Add(1)
	return errors.New("velocity/test: fails on everything")
}
func (l *alwaysFailingListener) Async() bool { return false }

// TestAsyncFailed_FailingFailureListenerReportedOnce asserts a listener that
// fails on the AsyncFailed it is handed is itself reported, once, and the
// failure is not dispatched again, so a listener failing on every event
// cannot loop.
func TestAsyncFailed_FailingFailureListenerReportedOnce(t *testing.T) {
	d := NewDispatcher()
	reports := &listenerFailureReports{}
	d.SetFailureReporter(reports.fn())
	l := &alwaysFailingListener{}
	d.Listen("evt", l)
	d.Listen(&AsyncFailed{}, l)

	if err := d.DispatchAfter(context.Background(), "evt", time.Millisecond); err != nil {
		t.Fatalf("DispatchAfter: %v", err)
	}
	testsync.Eventually(t, func() bool { return reports.count() >= 2 }, 2*time.Second, "both failures reported")
	time.Sleep(100 * time.Millisecond)

	if n := reports.count(); n != 2 {
		t.Fatalf("failures reported %d times, want 2 (the listener on evt, then on its AsyncFailed)", n)
	}
	if n := l.calls.Load(); n != 2 {
		t.Errorf("listener ran %d times, want 2", n)
	}
	reports.mu.Lock()
	defer reports.mu.Unlock()
	if got := reports.failures[1].EventName; got != resolveEventName(&AsyncFailed{}) {
		t.Errorf("second report EventName = %q, want the AsyncFailed event's", got)
	}
}

// TestAsyncFailed_FailureError asserts FailureError returns the failure
// with its type, that the JSON form carries Err as its text and decodes back
// to an event whose FailureError has that text, and that FailureSource
// names a listener.
func TestAsyncFailed_FailureError(t *testing.T) {
	cause := contract.NewHTTPError(404, "gone")
	if got := (&AsyncFailed{Err: cause}).FailureError(); got != error(cause) {
		t.Errorf("FailureError() = %#v, want Err itself", got)
	}
	if got := (&AsyncFailed{}).FailureError(); got != nil {
		t.Errorf("FailureError() of an empty event = %v, want nil", got)
	}
	if got := (&AsyncFailed{}).FailureSource(); got != contract.ErrorSourceListener {
		t.Errorf("FailureSource() = %v, want ErrorSourceListener", got)
	}
	data, err := json.Marshal(&AsyncFailed{EventName: "evt", Err: errors.New("listener broke")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"Err":"listener broke"`) {
		t.Errorf("JSON form does not carry Err's text: %s", data)
	}
	var decoded AsyncFailed
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode the JSON form: %v", err)
	}
	if got := decoded.FailureError(); got == nil || got.Error() != "listener broke" {
		t.Errorf("FailureError() of the decoded event = %v, want the Err text", got)
	}
}
