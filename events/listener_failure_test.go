package events

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	testsync "github.com/velocitykode/velocity/testing"
	"github.com/velocitykode/velocity/trace"
)

// errListenerBroke is the error failingListener returns.
var errListenerBroke = errors.New("velocity/test: listener broke")

// failingListener returns errListenerBroke on every event.
type failingListener struct{}

func (failingListener) Handle(context.Context, interface{}) error { return errListenerBroke }
func (failingListener) Async() bool                               { return false }

// panickingListener panics on every event.
type panickingListener struct{}

func (panickingListener) Handle(context.Context, interface{}) error { panic("listener exploded") }
func (panickingListener) Async() bool                               { return false }

// listenerFailureReports records the listener failures the failure-report
// bridge receives, with the context each was reported under.
type listenerFailureReports struct {
	mu       sync.Mutex
	failures []*AsyncFailed
	errs     []error
	ctxs     []context.Context
}

func (r *listenerFailureReports) fn() func(ctx context.Context, event interface{}, err error) {
	return func(ctx context.Context, event interface{}, err error) {
		failed, ok := event.(*AsyncFailed)
		if !ok {
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.failures = append(r.failures, failed)
		r.errs = append(r.errs, err)
		r.ctxs = append(r.ctxs, ctx)
	}
}

func (r *listenerFailureReports) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.failures)
}

// byListener returns the report for the listener type named name.
func (r *listenerFailureReports) byListener(t *testing.T, name string) (*AsyncFailed, context.Context, error) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, f := range r.failures {
		if f.ListenerName == name {
			return f, r.ctxs[i], r.errs[i]
		}
	}
	t.Fatalf("no report names listener %s (reports %d)", name, len(r.failures))
	return nil, nil, nil
}

// TestDetachedDispatch_ListenerFailuresReported asserts that a listener run
// by the no-queue fallback of DispatchAsync or DispatchAfter that returns
// an error, and one that panics, each reach the failure-report bridge
// exactly once as an AsyncFailed naming the event and the listener type,
// under the caller's trace IDs, instead of being dropped.
func TestDetachedDispatch_ListenerFailuresReported(t *testing.T) {
	tests := []struct {
		name     string
		dispatch func(d *DefaultDispatcher, ctx context.Context, event interface{}) error
	}{
		{"DispatchAsync", func(d *DefaultDispatcher, ctx context.Context, event interface{}) error {
			return d.DispatchAsync(ctx, event)
		}},
		{"DispatchAfter", func(d *DefaultDispatcher, ctx context.Context, event interface{}) error {
			return d.DispatchAfter(ctx, event, time.Millisecond)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDispatcher()
			reports := &listenerFailureReports{}
			d.SetFailureReporter(reports.fn())
			d.Listen("order.shipped", failingListener{})
			d.Listen("order.shipped", panickingListener{})

			ctx := trace.WithTrace(context.Background(), "trace-listener-failure", "span-listener-failure")
			if err := tt.dispatch(d, ctx, "order.shipped"); err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			testsync.Eventually(t, func() bool { return reports.count() >= 2 }, 2*time.Second, "both listener failures reported")
			time.Sleep(50 * time.Millisecond)
			if n := reports.count(); n != 2 {
				t.Fatalf("listener failures reported %d times, want 2 (one per listener)", n)
			}

			failed, reportCtx, err := reports.byListener(t, fmt.Sprintf("%T", failingListener{}))
			if failed.EventName != "order.shipped" {
				t.Errorf("EventName = %q, want order.shipped", failed.EventName)
			}
			if !errors.Is(err, errListenerBroke) {
				t.Errorf("reported error = %v, want the listener's own error %v", err, errListenerBroke)
			}
			if got := trace.GetTraceID(reportCtx); got != "trace-listener-failure" {
				t.Errorf("report trace id = %q, want the caller's", got)
			}

			failed, _, err = reports.byListener(t, fmt.Sprintf("%T", panickingListener{}))
			if failed.EventName != "order.shipped" {
				t.Errorf("EventName = %q, want order.shipped", failed.EventName)
			}
			var rp contract.RecoveredPanic
			if !errors.As(err, &rp) || !strings.Contains(err.Error(), "listener exploded") {
				t.Errorf("reported error = %#v, want the recovered panic", err)
			}
		})
	}
}
