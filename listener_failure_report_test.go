package velocity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	testsync "github.com/velocitykode/velocity/testing"
	"github.com/velocitykode/velocity/trace"
)

// failingOrderListener returns an error for every event.
type failingOrderListener struct{}

func (failingOrderListener) Handle(context.Context, interface{}) error {
	return errors.New("order listener failed")
}
func (failingOrderListener) Async() bool { return false }

// panickingOrderListener panics on every event.
type panickingOrderListener struct{}

func (panickingOrderListener) Handle(context.Context, interface{}) error {
	panic("order listener exploded")
}
func (panickingOrderListener) Async() bool { return false }

// TestDispatchAsync_ListenerFailuresReachReporterOnce asserts that in an
// app with no queue behind its dispatcher, a listener run through
// DispatchAsync that returns an error, and one that panics, each reach the
// Reporter chain exactly once, naming the event and the listener, under
// the caller's trace ID.
func TestDispatchAsync_ListenerFailuresReachReporterOnce(t *testing.T) {
	app, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	defer app.Shutdown(context.Background())
	reports := &failureReports{}
	reports.add(app)
	app.Services.Events.Listen("order.shipped", failingOrderListener{})
	app.Services.Events.Listen("order.shipped", panickingOrderListener{})

	ctx := trace.WithTrace(context.Background(), "trace-order-shipped", "span-order-shipped")
	if err := app.Services.Events.DispatchAsync(ctx, "order.shipped"); err != nil {
		t.Fatalf("DispatchAsync: %v", err)
	}
	testsync.Eventually(t, func() bool { return reports.count() >= 2 }, 2*time.Second, "listener failures reported")
	time.Sleep(50 * time.Millisecond)
	if n := reports.count(); n != 2 {
		t.Fatalf("listener failures reported %d times, want 2 (%v)", n, reports.errs)
	}

	reports.mu.Lock()
	defer reports.mu.Unlock()
	byListener := map[string]int{}
	for i, exCtx := range reports.ctxs {
		listener, _ := exCtx.Extra["listener_type"].(string)
		byListener[listener] = i
		if got := exCtx.Extra["event_name"]; got != "order.shipped" {
			t.Errorf("report %d event_name = %v, want order.shipped", i, got)
		}
		if exCtx.TraceID != "trace-order-shipped" {
			t.Errorf("report %d trace id = %q, want the caller's", i, exCtx.TraceID)
		}
		if exCtx.Source != contract.ErrorSourceListener {
			t.Errorf("report %d source = %v, want ErrorSourceListener", i, exCtx.Source)
		}
	}
	failing, ok := byListener[fmt.Sprintf("%T", failingOrderListener{})]
	if !ok {
		t.Fatalf("no report names the failing listener: %v", byListener)
	}
	if got := reports.errs[failing].Error(); got != "order listener failed" {
		t.Errorf("failing listener reported %q", got)
	}
	panicking, ok := byListener[fmt.Sprintf("%T", panickingOrderListener{})]
	if !ok {
		t.Fatalf("no report names the panicking listener: %v", byListener)
	}
	if got := reports.errs[panicking].Error(); !strings.Contains(got, "order listener exploded") {
		t.Errorf("panicking listener reported %q", got)
	}
}
