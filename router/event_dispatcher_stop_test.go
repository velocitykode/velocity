package router_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/router"
)

// stopEventCollector records every event an async pool delivered.
type stopEventCollector struct {
	mu     sync.Mutex
	events []interface{}
}

func (c *stopEventCollector) dispatch(_ context.Context, event interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	return nil
}

func (c *stopEventCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

// serveRecovering serves req on r and returns the value of any panic that
// escaped ServeHTTP, so a send on a stopped pool shows up as a test
// failure instead of ending the test binary.
func serveRecovering(r http.Handler, w http.ResponseWriter, req *http.Request) (escaped any) {
	defer func() { escaped = recover() }()
	r.ServeHTTP(w, req)
	return nil
}

// TestShutdownEventDispatcher_LateRequestEventIsCountedDrop asserts that a
// handler still running when ShutdownEventDispatcher returns (a straggler
// past the server's shutdown deadline) finishes without a panic: its
// RequestHandled, dispatched after the pool stopped, is dropped and counted
// in DroppedEventCount and reported through OnEventDispatchError, while
// every event queued before the stop reaches the pool's target.
func TestShutdownEventDispatcher_LateRequestEventIsCountedDrop(t *testing.T) {
	col := &stopEventCollector{}
	r := router.New()
	r.SetAsyncEventDispatcher(col.dispatch, 2, 64)

	var (
		dropMu     sync.Mutex
		dropErrs   []error
		dropEvents []router.Event
	)
	r.OnEventDispatchError = func(err error, ev router.Event) {
		dropMu.Lock()
		defer dropMu.Unlock()
		dropErrs = append(dropErrs, err)
		dropEvents = append(dropEvents, ev)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	r.Get("/fast", func(c *router.Context) error { return c.String(http.StatusOK, "ok") })
	r.Get("/slow", func(c *router.Context) error {
		close(entered)
		<-release
		return c.String(http.StatusOK, "late")
	})

	// Three finished requests queue RequestStarted, RequestRouted and
	// RequestHandled each.
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		if p := serveRecovering(r, w, httptest.NewRequest(http.MethodGet, "/fast", nil)); p != nil {
			t.Fatalf("fast request panicked: %v", p)
		}
	}

	escaped := make(chan any, 1)
	go func() {
		escaped <- serveRecovering(r, httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/slow", nil))
	}()
	<-entered // the straggler's RequestStarted and RequestRouted are queued too

	const queuedBeforeStop = 3*3 + 2
	if err := r.ShutdownEventDispatcher(context.Background()); err != nil {
		t.Fatalf("ShutdownEventDispatcher: %v", err)
	}
	if got := col.count(); got != queuedBeforeStop {
		t.Fatalf("delivered %d events before the stop returned, want %d", got, queuedBeforeStop)
	}
	before := r.DroppedEventCount()

	close(release)
	select {
	case p := <-escaped:
		if p != nil {
			t.Fatalf("straggler panicked dispatching after the stop: %v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("straggler never finished")
	}

	if got := r.DroppedEventCount() - before; got != 1 {
		t.Errorf("DroppedEventCount grew by %d, want 1 for the late RequestHandled", got)
	}
	dropMu.Lock()
	if len(dropEvents) != 1 {
		t.Errorf("OnEventDispatchError calls = %d, want 1", len(dropEvents))
	} else {
		if _, ok := dropEvents[0].(*router.RequestHandled); !ok {
			t.Errorf("dropped event = %T, want *router.RequestHandled", dropEvents[0])
		}
		if dropErrs[0] == nil || errors.Is(dropErrs[0], router.ErrEventBufferFull) {
			t.Errorf("drop error = %v, want the stopped-pool error, not a full buffer", dropErrs[0])
		}
	}
	dropMu.Unlock()
	if got := col.count(); got != queuedBeforeStop {
		t.Errorf("delivered %d events after the stop, want %d (nothing after the stop)", got, queuedBeforeStop)
	}
}

// TestShutdownEventDispatcher_TimeoutPanicAfterStopIsReported asserts that
// a Timeout handler goroutine panicking after the middleware answered 503
// and after the async pool stopped reports the panic once through the
// error sink: its RequestFailed is a counted drop instead of a send on a
// stopped pool that would abort the report, and the goroutine runs to its
// end (done is closed from its last deferred step).
func TestShutdownEventDispatcher_TimeoutPanicAfterStopIsReported(t *testing.T) {
	col := &stopEventCollector{}
	r := router.New()
	r.SetAsyncEventDispatcher(col.dispatch, 2, 64)

	var (
		sinkMu sync.Mutex
		lines  []string
	)
	reported := make(chan struct{}, 4)
	r.SetErrorLogger(func(msg string, kvs ...any) {
		sinkMu.Lock()
		lines = append(lines, msg+" "+fmt.Sprint(kvs...))
		sinkMu.Unlock()
		reported <- struct{}{}
	})
	r.SetWarnLogger(func(string, ...any) {})
	r.Use(router.Timeout(50 * time.Millisecond))

	release := make(chan struct{})
	done := make(chan struct{})
	r.Get("/slow", func(c *router.Context) error {
		defer close(done)
		<-c.Request.Context().Done()
		<-release
		panic(latePanicValue)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/slow", nil)
	req.Header.Set("Accept", "application/json")
	if p := serveRecovering(r, w, req); p != nil {
		t.Fatalf("ServeHTTP panicked: %v", p)
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}

	if err := r.ShutdownEventDispatcher(context.Background()); err != nil {
		t.Fatalf("ShutdownEventDispatcher: %v", err)
	}
	before := r.DroppedEventCount()

	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("late handler never finished")
	}
	select {
	case <-reported:
	case <-time.After(2 * time.Second):
		t.Fatal("the late panic never reached the error sink")
	}
	time.Sleep(50 * time.Millisecond) // a second report would show here

	sinkMu.Lock()
	defer sinkMu.Unlock()
	n := 0
	for _, l := range lines {
		if strings.Contains(l, latePanicValue) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("error sink reports of the late panic = %d, want 1 (%q)", n, lines)
	}
	if got := r.DroppedEventCount() - before; got != 1 {
		t.Errorf("DroppedEventCount grew by %d, want 1 for the late RequestFailed", got)
	}
}

// TestShutdownEventDispatcher_ConcurrentSendersNeverPanic asserts that
// requests dispatching while ShutdownEventDispatcher stops the pool never
// panic, and that every event they dispatched is either delivered to the
// pool's target or counted in DroppedEventCount (a full buffer or a
// stopped pool), none lost silently. One gated request is released only
// after the stop returned, so at least one event is always dispatched on
// the stopped pool whatever the stress senders' scheduling.
func TestShutdownEventDispatcher_ConcurrentSendersNeverPanic(t *testing.T) {
	col := &stopEventCollector{}
	r := router.New()
	r.SetAsyncEventDispatcher(col.dispatch, 2, 8)
	r.Get("/", func(c *router.Context) error { return c.NoContent() })

	var (
		dropMu     sync.Mutex
		dropErrs   []error
		dropEvents []router.Event
	)
	r.OnEventDispatchError = func(err error, ev router.Event) {
		dropMu.Lock()
		defer dropMu.Unlock()
		dropErrs = append(dropErrs, err)
		dropEvents = append(dropEvents, ev)
	}

	gateEntered, gate := make(chan struct{}), make(chan struct{})
	r.Get("/gated", func(c *router.Context) error {
		close(gateEntered)
		<-gate
		return c.NoContent()
	})
	gatedEscaped := make(chan any, 1)
	gatedDone := make(chan struct{})
	go func() {
		defer close(gatedDone)
		gatedEscaped <- serveRecovering(r, httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/gated", nil))
	}()
	var openGateOnce sync.Once
	openGate := func() { openGateOnce.Do(func() { close(gate) }) }
	t.Cleanup(func() {
		openGate()
		<-gatedDone
	})
	<-gateEntered

	const senders, perSender = 8, 200
	var wg sync.WaitGroup
	start := make(chan struct{})
	panics := make(chan any, senders)
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < perSender; j++ {
				if p := serveRecovering(r, httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil)); p != nil {
					panics <- p
					return
				}
			}
		}()
	}
	close(start)
	time.Sleep(time.Millisecond)
	if err := r.ShutdownEventDispatcher(context.Background()); err != nil {
		t.Fatalf("ShutdownEventDispatcher: %v", err)
	}
	wg.Wait()
	close(panics)
	for p := range panics {
		t.Fatalf("a request panicked dispatching while the pool stopped: %v", p)
	}

	// Only the gated request is left; its RequestHandled is dispatched
	// after the stop returned.
	droppedBefore := r.DroppedEventCount()
	dropMu.Lock()
	callsBefore := len(dropEvents)
	dropMu.Unlock()
	openGate()
	select {
	case p := <-gatedEscaped:
		if p != nil {
			t.Fatalf("the gated request panicked dispatching after the stop: %v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the gated request never finished")
	}
	if got := r.DroppedEventCount() - droppedBefore; got != 1 {
		t.Errorf("DroppedEventCount grew by %d after the stop, want 1 for the gated RequestHandled", got)
	}
	dropMu.Lock()
	if late := dropEvents[callsBefore:]; len(late) != 1 {
		t.Errorf("OnEventDispatchError calls after the stop = %d, want 1", len(late))
	} else {
		if _, ok := late[0].(*router.RequestHandled); !ok {
			t.Errorf("dropped event = %T, want *router.RequestHandled", late[0])
		}
		if err := dropErrs[callsBefore]; err == nil || errors.Is(err, router.ErrEventBufferFull) {
			t.Errorf("drop error = %v, want the stopped-pool error, not a full buffer", err)
		}
	}
	dropMu.Unlock()

	// RequestStarted, RequestRouted and RequestHandled per request,
	// the gated one included.
	const dispatched = (senders*perSender + 1) * 3
	if got := uint64(col.count()) + r.DroppedEventCount(); got != dispatched {
		t.Errorf("delivered %d + dropped %d = %d, want %d", col.count(), r.DroppedEventCount(), got, dispatched)
	}
}
