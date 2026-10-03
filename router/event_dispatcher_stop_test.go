package router_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/eventemit"
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

// shutdownAsync runs r.Shutdown on a goroutine of its own and returns its
// result channel once the router refuses a probe request (evidence that
// its stop began), with the number of probes it admitted before that.
func shutdownAsync(t *testing.T, r *router.VelocityRouterV2, probe string) (<-chan error, int) {
	t.Helper()
	stopped := make(chan error, 1)
	go func() { stopped <- r.Shutdown(context.Background()) }() //safe-goroutine: the waiting Shutdown; its result is read by the caller
	for admitted := 0; ; admitted++ {
		w := httptest.NewRecorder()
		if p := serveRecovering(r, w, httptest.NewRequest(http.MethodGet, probe, nil)); p != nil {
			t.Fatalf("probe request panicked: %v", p)
		}
		if w.Code == http.StatusServiceUnavailable {
			return stopped, admitted
		}
		runtime.Gosched()
	}
}

// TestShutdown_StragglerEventsReachThePoolBeforeItStops asserts that a
// handler still running when Shutdown is called holds the router's stop:
// the pool stops only once the straggler returned, so its late
// RequestHandled is delivered like every event queued before it, nothing
// is dropped, and Shutdown returns nil once both drained.
func TestShutdown_StragglerEventsReachThePoolBeforeItStops(t *testing.T) {
	col := &stopEventCollector{}
	r := router.New()
	failures := &eventemit.Failures{}
	r.ShareEventFailures(failures)
	r.SetAsyncEventDispatcher(col.dispatch, 2, 64)

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
	<-entered

	stopped, probes := shutdownAsync(t, r, "/fast")
	select {
	case err := <-stopped:
		t.Fatalf("Shutdown returned while a request it admitted still ran: %v", err)
	default:
	}

	close(release)
	select {
	case p := <-escaped:
		if p != nil {
			t.Fatalf("straggler panicked: %v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("straggler never finished")
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Shutdown = %v, want nil once the straggler and the pool drained", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown never returned after the straggler finished")
	}

	if got := failures.Count(); got != 0 {
		t.Errorf("failed event count = %d, want 0: the pool outlives every admitted request", got)
	}
	// Three fast requests, the admitted probes and the straggler; a
	// refused probe dispatches nothing.
	if got, delivered := col.count(), (4+probes)*3; got != delivered {
		t.Errorf("delivered %d events, want %d", got, delivered)
	}
}

// TestShutdown_TimeoutSurvivorHoldsTheStop asserts that a Timeout handler
// goroutine still running after the middleware answered 503 holds the
// router's stop: Shutdown returns only after the goroutine ended, its late
// panic reported once through the error sink and its late RequestFailed
// delivered to the pool, not dropped.
func TestShutdown_TimeoutSurvivorHoldsTheStop(t *testing.T) {
	col := &stopEventCollector{}
	r := router.New()
	failures := &eventemit.Failures{}
	r.ShareEventFailures(failures)
	r.SetAsyncEventDispatcher(col.dispatch, 2, 64)

	var (
		sinkMu sync.Mutex
		lines  []string
	)
	r.SetLogger(levelLogger{
		onError: func(msg string, kvs ...any) {
			sinkMu.Lock()
			lines = append(lines, msg+" "+fmt.Sprint(kvs...))
			sinkMu.Unlock()
		},
		onWarn: func(string, ...any) {},
	})
	r.Use(router.Timeout(50 * time.Millisecond))

	release := make(chan struct{})
	done := make(chan struct{})
	r.Get("/slow", func(c *router.Context) error {
		defer close(done)
		<-c.Request.Context().Done()
		<-release
		panic(latePanicValue)
	})
	r.Get("/probe", func(c *router.Context) error { return c.NoContent() })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/slow", nil)
	req.Header.Set("Accept", "application/json")
	if p := serveRecovering(r, w, req); p != nil {
		t.Fatalf("ServeHTTP panicked: %v", p)
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}

	stopped, _ := shutdownAsync(t, r, "/probe")
	select {
	case err := <-stopped:
		t.Fatalf("Shutdown returned while the Timeout handler goroutine still ran: %v", err)
	default:
	}

	close(release)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Shutdown = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown never returned after the Timeout handler goroutine ended")
	}
	select {
	case <-done:
	default:
		t.Fatal("Shutdown returned before the late handler finished")
	}

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
	if got := failures.Count(); got != 0 {
		t.Errorf("failed event count = %d, want 0: the late RequestFailed reaches the pool", got)
	}
	failed := 0
	col.mu.Lock()
	for _, ev := range col.events {
		if _, ok := ev.(*router.RequestFailed); ok {
			failed++
		}
	}
	col.mu.Unlock()
	if failed != 2 {
		t.Errorf("delivered RequestFailed events = %d, want 2 (the 503, then the late panic)", failed)
	}
}

// TestShutdown_ConcurrentSendersNeverPanic asserts that requests arriving
// while Shutdown stops the router never panic: each is either admitted,
// and every event it dispatched is delivered to the pool's target or
// counted as a failed event (a full buffer), none lost silently, or
// refused with 503, dispatching nothing. One gated request is released
// only after the stop began, so the stop always waits for one admitted
// request whatever the stress senders' scheduling.
func TestShutdown_ConcurrentSendersNeverPanic(t *testing.T) {
	col := &stopEventCollector{}
	r := router.New()
	failures := &eventemit.Failures{}
	r.ShareEventFailures(failures)
	r.SetAsyncEventDispatcher(col.dispatch, 2, 8)
	r.Get("/", func(c *router.Context) error { return c.NoContent() })

	var (
		dropMu   sync.Mutex
		dropErrs []error
	)
	failures.SetHook(func(err error, ev any) {
		dropMu.Lock()
		defer dropMu.Unlock()
		dropErrs = append(dropErrs, err)
	})

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
	var admitted atomic.Int64
	start := make(chan struct{})
	panics := make(chan any, senders)
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < perSender; j++ {
				w := httptest.NewRecorder()
				if p := serveRecovering(r, w, httptest.NewRequest(http.MethodGet, "/", nil)); p != nil {
					panics <- p
					return
				}
				if w.Code != http.StatusServiceUnavailable {
					admitted.Add(1)
				}
			}
		}()
	}
	close(start)
	stopped, probes := shutdownAsync(t, r, "/")
	wg.Wait()
	close(panics)
	for p := range panics {
		t.Fatalf("a request panicked dispatching while the router stopped: %v", p)
	}

	openGate()
	select {
	case p := <-gatedEscaped:
		if p != nil {
			t.Fatalf("the gated request panicked: %v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the gated request never finished")
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Shutdown = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown never returned")
	}

	dropMu.Lock()
	for _, err := range dropErrs {
		if !errors.Is(err, router.ErrEventBufferFull) {
			t.Errorf("drop error = %v, want only full-buffer drops while the pool runs", err)
		}
	}
	dropMu.Unlock()

	// RequestStarted, RequestRouted and RequestHandled per admitted
	// request, the probes and the gated one included.
	dispatched := (uint64(admitted.Load()) + uint64(probes) + 1) * 3
	if got := uint64(col.count()) + failures.Count(); got != dispatched {
		t.Errorf("delivered %d + dropped %d = %d, want %d", col.count(), failures.Count(), got, dispatched)
	}
}
