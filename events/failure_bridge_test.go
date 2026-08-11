package events

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// bridgeRecorder captures failure-reporter invocations.
type bridgeRecorder struct {
	mu     sync.Mutex
	events []interface{}
	errs   []error
}

func (r *bridgeRecorder) fn() func(ctx context.Context, event interface{}, err error) {
	return func(_ context.Context, event interface{}, err error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events = append(r.events, event)
		r.errs = append(r.errs, err)
	}
}

func (r *bridgeRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func TestDispatch_FailureEventBridge(t *testing.T) {
	d := NewDispatcher()
	rec := &bridgeRecorder{}
	d.SetFailureReporter(rec.fn())

	ev := &AsyncFailed{Context: context.Background(), EventName: "x", Error: "boom"}
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if rec.count() != 1 {
		t.Fatalf("reporter called %d times, want 1", rec.count())
	}
	if rec.errs[0] == nil || rec.errs[0].Error() != "boom" {
		t.Fatalf("reported error = %v, want boom", rec.errs[0])
	}
	if rec.events[0] != ev {
		t.Fatal("reported event is not the dispatched event")
	}
}

func TestDispatch_NonFailureEventNotBridged(t *testing.T) {
	d := NewDispatcher()
	rec := &bridgeRecorder{}
	d.SetFailureReporter(rec.fn())

	if err := d.Dispatch(context.Background(), "user.created"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if rec.count() != 0 {
		t.Fatalf("reporter called %d times for non-failure event, want 0", rec.count())
	}
}

func TestDispatch_NilFailureErrorNotBridged(t *testing.T) {
	d := NewDispatcher()
	rec := &bridgeRecorder{}
	d.SetFailureReporter(rec.fn())

	// Error empty -> FailureError() nil -> nothing to report.
	if err := d.Dispatch(context.Background(), &AsyncFailed{Error: ""}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if rec.count() != 0 {
		t.Fatalf("reporter called %d times for nil failure error, want 0", rec.count())
	}
}

func TestDispatch_NoReporterIsNoop(t *testing.T) {
	d := NewDispatcher()
	if err := d.Dispatch(context.Background(), &AsyncFailed{Error: "boom"}); err != nil {
		t.Fatalf("Dispatch without reporter: %v", err)
	}
}

func TestDispatch_ReporterReentrancyGuard(t *testing.T) {
	d := NewDispatcher()

	var calls int
	d.SetFailureReporter(func(ctx context.Context, event interface{}, err error) {
		calls++
		if calls > 1 {
			t.Fatal("reporter re-entered through the bridge")
		}
		// A reporter that dispatches another failure event with the
		// bridged context must reach listeners but never re-report.
		if derr := d.Dispatch(ctx, &AsyncFailed{Error: "nested"}); derr != nil {
			t.Fatalf("nested Dispatch: %v", derr)
		}
	})

	if err := d.Dispatch(context.Background(), &AsyncFailed{Error: "boom"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if calls != 1 {
		t.Fatalf("reporter called %d times, want 1", calls)
	}
}

func TestDispatch_FailureBridgeRunsBeforeListeners(t *testing.T) {
	d := NewDispatcher()
	var order []string
	var mu sync.Mutex

	d.SetFailureReporter(func(_ context.Context, _ interface{}, _ error) {
		mu.Lock()
		order = append(order, "report")
		mu.Unlock()
	})
	d.Listen("events.async_failed", listenerFn(func(_ context.Context, _ interface{}) error {
		mu.Lock()
		order = append(order, "listener")
		mu.Unlock()
		return nil
	}))

	if err := d.Dispatch(context.Background(), &AsyncFailed{Error: "boom"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "report" || order[1] != "listener" {
		t.Fatalf("order = %v, want [report listener]", order)
	}
}

// otherFailed is a second FailureEvent type with its own event name, so a
// listener registered on events.async_failed can dispatch it without
// re-triggering itself.
type otherFailed struct{ err string }

func (e *otherFailed) FailureError() error { return errors.New(e.err) }

var _ contract.FailureEvent = (*otherFailed)(nil)

// listenerFn adapts a func to the Listener interface for tests.
type listenerFn func(ctx context.Context, event interface{}) error

func (f listenerFn) Handle(ctx context.Context, event interface{}) error { return f(ctx, event) }
func (f listenerFn) Async() bool                                         { return false }

// Static check the test relies on: AsyncFailed implements the contract.
var _ contract.FailureEvent = (*AsyncFailed)(nil)

// --- every public dispatch path reports at the point of dispatch ---

func TestDispatchNow_FailureEventBridge(t *testing.T) {
	d := NewDispatcher()
	rec := &bridgeRecorder{}
	d.SetFailureReporter(rec.fn())

	if err := d.DispatchNow(context.Background(), &AsyncFailed{Error: "boom"}); err != nil {
		t.Fatalf("DispatchNow: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("reporter called %d times, want 1", rec.count())
	}
}

func TestDispatchAsync_NoQueue_ReportsOnceSynchronously(t *testing.T) {
	d := NewDispatcher()
	rec := &bridgeRecorder{}
	d.SetFailureReporter(rec.fn())

	// No queue configured: falls back to a goroutine running DispatchNow.
	// The report must happen synchronously at the point of dispatch...
	if err := d.DispatchAsync(context.Background(), &AsyncFailed{Error: "boom"}); err != nil {
		t.Fatalf("DispatchAsync: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("reporter called %d times before fallback ran, want 1 (synchronous)", rec.count())
	}

	// ...and the goroutine's DispatchNow must NOT re-report. Give the
	// fallback time to run, then confirm the count is unchanged.
	time.Sleep(300 * time.Millisecond)
	if rec.count() != 1 {
		t.Fatalf("reporter called %d times after fallback, want 1 (no double-report)", rec.count())
	}
}

func TestDispatchAfter_NoQueue_ReportsNowNotAfterDelay(t *testing.T) {
	d := NewDispatcher()
	rec := &bridgeRecorder{}
	d.SetFailureReporter(rec.fn())

	// Long delay: a report observed immediately proves it happened at the
	// point of dispatch, not from the timer callback.
	if err := d.DispatchAfter(context.Background(), &AsyncFailed{Error: "boom"}, time.Hour); err != nil {
		t.Fatalf("DispatchAfter: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("reporter called %d times immediately, want 1 (synchronous, not delayed)", rec.count())
	}
}

func TestDispatchAfter_NoQueue_TimerFallbackDoesNotDoubleReport(t *testing.T) {
	d := NewDispatcher()
	rec := &bridgeRecorder{}
	d.SetFailureReporter(rec.fn())

	if err := d.DispatchAfter(context.Background(), &AsyncFailed{Error: "boom"}, time.Millisecond); err != nil {
		t.Fatalf("DispatchAfter: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if rec.count() != 1 {
		t.Fatalf("reporter called %d times after timer fired, want 1 (no double-report)", rec.count())
	}
}

func TestUntil_FailureEventBridge(t *testing.T) {
	d := NewDispatcher()
	rec := &bridgeRecorder{}
	d.SetFailureReporter(rec.fn())

	if _, err := d.Until(context.Background(), &AsyncFailed{Error: "boom"}); err != nil {
		t.Fatalf("Until: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("reporter called %d times, want 1", rec.count())
	}
}

func TestQueueIntegratedDispatch_FailureEventBridge(t *testing.T) {
	d := NewQueueIntegratedDispatcher()
	rec := &bridgeRecorder{}
	d.SetFailureReporter(rec.fn())

	if err := d.Dispatch(context.Background(), &AsyncFailed{Error: "boom"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("reporter called %d times, want 1", rec.count())
	}
}

func TestDispatchAsync_WithQueue_ReportsOnce(t *testing.T) {
	d := NewDispatcher()
	rec := &bridgeRecorder{}
	d.SetFailureReporter(rec.fn())
	d.SetQueueDispatcher(queueDispatcherFn(func(ctx context.Context, event interface{}, l Listener, delay time.Duration) error {
		return nil
	}))

	// Need a queued listener so the queue path is exercised; the report
	// must still happen exactly once at dispatch, independent of listeners.
	d.Listen("events.async_failed", listenerFn(func(_ context.Context, _ interface{}) error { return nil }))
	if err := d.DispatchAsync(context.Background(), &AsyncFailed{Error: "boom"}); err != nil {
		t.Fatalf("DispatchAsync: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("reporter called %d times, want 1", rec.count())
	}
}

// queueDispatcherFn adapts a func to the QueueDispatcher interface for tests.
type queueDispatcherFn func(ctx context.Context, event interface{}, listener Listener, delay time.Duration) error

func (f queueDispatcherFn) Push(ctx context.Context, event interface{}, listener Listener, delay time.Duration) error {
	return f(ctx, event, listener, delay)
}

// --- finding: reporter recursion with a FRESH context must not loop ---

func TestReporter_FreshContextRedispatch_NoRecursion(t *testing.T) {
	d := NewDispatcher()

	var calls int
	d.SetFailureReporter(func(_ context.Context, _ interface{}, _ error) {
		calls++
		if calls > 3 {
			t.Fatal("reporter recursed through the bridge despite fresh context")
		}
		// Hostile reporter: re-dispatches a brand-new failure event with a
		// brand-new context, deliberately discarding the marked ctx. The
		// per-goroutine guard must suppress the report; listeners still run.
		if err := d.Dispatch(context.Background(), &AsyncFailed{Error: "nested"}); err != nil {
			t.Fatalf("nested Dispatch: %v", err)
		}
	})

	if err := d.Dispatch(context.Background(), &AsyncFailed{Error: "boom"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if calls != 1 {
		t.Fatalf("reporter called %d times, want 1", calls)
	}
}

func TestReporter_FreshContextDispatchNow_NoRecursion(t *testing.T) {
	d := NewDispatcher()

	var calls int
	d.SetFailureReporter(func(_ context.Context, _ interface{}, _ error) {
		calls++
		if calls > 3 {
			t.Fatal("reporter recursed via DispatchNow with fresh context")
		}
		if err := d.DispatchNow(context.Background(), &AsyncFailed{Error: "nested"}); err != nil {
			t.Fatalf("nested DispatchNow: %v", err)
		}
	})

	if err := d.DispatchNow(context.Background(), &AsyncFailed{Error: "boom"}); err != nil {
		t.Fatalf("DispatchNow: %v", err)
	}
	if calls != 1 {
		t.Fatalf("reporter called %d times, want 1", calls)
	}
}

// --- finding: a listener dispatching a DIFFERENT failure event with the ctx
// it received must still be reported (the marker is event-scoped, not a
// blanket suppression) ---

func TestListener_DistinctFailureEventWithReceivedCtx_IsReported(t *testing.T) {
	d := NewDispatcher()
	rec := &bridgeRecorder{}
	d.SetFailureReporter(rec.fn())

	nested := &otherFailed{err: "terminal"}
	d.Listen("events.async_failed", listenerFn(func(ctx context.Context, _ interface{}) error {
		// Listener observes a terminal failure of its own and dispatches a
		// DIFFERENT failure event using the ctx it was handed (which carries
		// the marker for the OUTER event). It must not be swallowed.
		return d.Dispatch(ctx, nested)
	}))

	outer := &AsyncFailed{Error: "boom"}
	if err := d.Dispatch(context.Background(), outer); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if rec.count() != 2 {
		t.Fatalf("reporter called %d times, want 2 (outer + listener-originated)", rec.count())
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.events[0] != outer || rec.events[1] != nested {
		t.Fatalf("reported events = %T,%T; want outer then nested", rec.events[0], rec.events[1])
	}
}

// --- same event re-dispatched by a listener with its received ctx stays
// suppressed (loop prevention) ---

func TestListener_SameFailureEventWithReceivedCtx_NotReReported(t *testing.T) {
	d := NewDispatcher()
	rec := &bridgeRecorder{}
	d.SetFailureReporter(rec.fn())

	outer := &AsyncFailed{Error: "boom"}
	var redispatched bool
	d.Listen("events.async_failed", listenerFn(func(ctx context.Context, ev interface{}) error {
		if redispatched {
			return nil // break the listener loop; the bridge already proved its point
		}
		redispatched = true
		return d.Dispatch(ctx, ev)
	}))

	if err := d.Dispatch(context.Background(), outer); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("reporter called %d times, want 1 (same instance not re-reported)", rec.count())
	}
}

// --- uncomparable failure events: fallback paths must still report exactly
// once (deterministic report-flag skip, not identity-based dedupe) ---

// uncomparableFailed is a VALUE-type failure event whose dynamic type is
// uncomparable (slice field); identity comparison cannot dedupe it.
type uncomparableFailed struct{ errs []string }

func (e uncomparableFailed) FailureError() error { return errors.New(e.errs[0]) }

var _ contract.FailureEvent = uncomparableFailed{}

func TestDispatchAsync_NoQueue_UncomparableEvent_ReportsOnce(t *testing.T) {
	d := NewDispatcher()
	rec := &bridgeRecorder{}
	d.SetFailureReporter(rec.fn())

	ev := uncomparableFailed{errs: []string{"boom"}}
	if err := d.DispatchAsync(context.Background(), ev); err != nil {
		t.Fatalf("DispatchAsync: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if rec.count() != 1 {
		t.Fatalf("reporter called %d times, want exactly 1 for uncomparable event", rec.count())
	}
}

func TestDispatchAfter_NoQueue_UncomparableEvent_ReportsOnce(t *testing.T) {
	d := NewDispatcher()
	rec := &bridgeRecorder{}
	d.SetFailureReporter(rec.fn())

	ev := uncomparableFailed{errs: []string{"boom"}}
	if err := d.DispatchAfter(context.Background(), ev, time.Millisecond); err != nil {
		t.Fatalf("DispatchAfter: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if rec.count() != 1 {
		t.Fatalf("reporter called %d times, want exactly 1 for uncomparable event", rec.count())
	}
}
