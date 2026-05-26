package events

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/queue"
)

// userSignedUp is a representative typed event used by the cross-process
// hydration test. The fields exercise the three things json.Unmarshal must
// preserve to invalidate the previous map[string]any behaviour: a string,
// an int, and an embedded struct.
type userSignedUp struct {
	ID        int           `json:"id"`
	Email     string        `json:"email"`
	Profile   userProfile   `json:"profile"`
	CreatedAt time.Duration `json:"created_at"` // non-stringy field
}

type userProfile struct {
	DisplayName string `json:"display_name"`
	Country     string `json:"country"`
}

// capturingListener stores the last value its Handle received so the test
// can assert on the concrete typed payload (not a map). ShouldQueue is true
// so QueueIntegratedDispatcher.Dispatch routes through the queue.
type capturingListener struct {
	mu       sync.Mutex
	received interface{}
	done     chan struct{}
	failWith error
}

func (l *capturingListener) Handle(ctx context.Context, event interface{}) error {
	l.mu.Lock()
	l.received = event
	l.mu.Unlock()
	if l.done != nil {
		select {
		case l.done <- struct{}{}:
		default:
		}
	}
	return l.failWith
}

func (l *capturingListener) ShouldQueue() bool { return true }

func (l *capturingListener) Got() interface{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.received
}

// TestEventListenerJob_TypedEventSurvivesCrossProcessHydration is the
// follow-up regression test. It walks the full producer -> queue -> worker
// path, where the worker process intentionally does NOT share producer-side
// memory (the in-process job pointer fast path is bypassed): the wire bytes
// are marshalled out of the memory driver and rehydrated via the package
// queue job factory, the same way a database/redis driver would. The
// listener must receive a *userSignedUp with all fields intact, not a
// map[string]any.
func TestEventListenerJob_TypedEventSurvivesCrossProcessHydration(t *testing.T) {
	defer UnregisterListenerFactory("events.capturingListener")
	defer UnregisterEventFactory("*events.userSignedUp")
	defer setFailureReporter(nil)

	listener := &capturingListener{done: make(chan struct{}, 1)}
	RegisterListenerFactory("events.capturingListener", func() Listener { return listener })
	RegisterEventFactory("*events.userSignedUp", func() interface{} { return &userSignedUp{} })

	drv := queue.NewMemoryDriver()
	defer drv.Shutdown(context.Background())

	d := NewQueueIntegratedDispatcher()
	InitializeQueueIntegration(d, drv, nil)
	d.Listen("user.signed.up", listener)

	original := &userSignedUp{
		ID:    42,
		Email: "alice@example.com",
		Profile: userProfile{
			DisplayName: "Alice",
			Country:     "FR",
		},
		CreatedAt: 7 * time.Second,
	}
	if err := d.Dispatch(context.Background(), original); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// Simulate a cross-process worker: pop the job through queue.MarshalJob
	// + queue.HydrateJob so the in-process pointer fast path is bypassed.
	// The memory driver retains the wrapper, but we round-trip the payload
	// explicitly to mirror what a durable driver does on every pop.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rawJob, err := drv.PopCtx(ctx, "default")
	if err != nil {
		t.Fatalf("PopCtx: %v", err)
	}
	if rawJob == nil {
		t.Fatal("no job available")
	}

	payload, err := queue.MarshalJob(rawJob, "default")
	if err != nil {
		t.Fatalf("MarshalJob: %v", err)
	}
	hydrated, err := queue.HydrateJob(payload)
	if err != nil {
		t.Fatalf("HydrateJob: %v", err)
	}
	hj, ok := hydrated.(*EventListenerJob)
	if !ok {
		t.Fatalf("HydrateJob returned %T, want *EventListenerJob", hydrated)
	}
	// After the cross-process round trip the in-process pointer is gone
	// and the listener is nil; this is exactly the worker-side state the
	// test is asserting against.
	if hj.event != nil {
		t.Fatal("cross-process job retained in-process event pointer; test setup invalid")
	}
	if hj.listener != nil {
		t.Fatal("cross-process job retained in-process listener pointer; test setup invalid")
	}

	if err := hj.HandleCtx(ctx); err != nil {
		t.Fatalf("HandleCtx: %v", err)
	}

	select {
	case <-listener.done:
	case <-time.After(time.Second):
		t.Fatal("listener was not invoked")
	}

	got := listener.Got()
	if got == nil {
		t.Fatal("listener received nil event")
	}
	// The listener MUST receive a *userSignedUp, not a map[string]any:
	// that is the entire point of the typed-event preservation fix.
	if _, isMap := got.(map[string]interface{}); isMap {
		t.Fatalf("listener received map[string]any; concrete type lost: %+v", got)
	}
	typed, ok := got.(*userSignedUp)
	if !ok {
		t.Fatalf("listener received %T, want *userSignedUp", got)
	}
	if !reflect.DeepEqual(typed, original) {
		t.Fatalf("listener received mismatched event:\n  got  %+v\n  want %+v", typed, original)
	}
}

// TestPushToQueue_RefusesWithoutEventFactory asserts the dispatch-side
// guard for the typed-event follow-up: when no event factory is registered
// for the dispatched value's reflect type, pushToQueue refuses to enqueue.
// The job never lands on the queue, so the worker can't observe a
// map[string]any silently.
func TestPushToQueue_RefusesWithoutEventFactory(t *testing.T) {
	defer UnregisterListenerFactory("events.capturingListener")
	defer UnregisterEventFactory("*events.userSignedUp")

	// Register only the listener factory; deliberately omit the event
	// factory to assert the refusal path.
	RegisterListenerFactory("events.capturingListener", func() Listener {
		return &capturingListener{}
	})
	UnregisterEventFactory("*events.userSignedUp")

	drv := queue.NewMemoryDriver()
	defer drv.Shutdown(context.Background())

	d := NewQueueIntegratedDispatcher()
	d.SetQueueDriver(drv)
	d.Listen("user.signed.up", &capturingListener{})

	err := d.Dispatch(context.Background(), &userSignedUp{ID: 1, Email: "x@y.z"})
	if err == nil {
		t.Fatal("expected Dispatch to refuse: no event factory registered")
	}
	if !errors.Is(err, ErrEventTypeNotRegistered) {
		t.Fatalf("expected ErrEventTypeNotRegistered, got %v", err)
	}

	// Confirm nothing landed on the queue.
	if size, _ := drv.Size("default"); size != 0 {
		t.Fatalf("queue size after refused push: got %d want 0", size)
	}
}

// TestHandleCtx_RefusesWithoutEventFactoryReportsFailure exercises the
// mismatched-registries scenario: the producer side had an event factory
// registered and pushed a typed event; the consumer side is missing the
// event factory (e.g. binary deployed without the registration call). The
// worker must surface a real error AND route it through FailureReporter so
// the operator sees the silent drop.
func TestHandleCtx_RefusesWithoutEventFactoryReportsFailure(t *testing.T) {
	defer UnregisterListenerFactory("events.capturingListener")
	defer UnregisterEventFactory("*events.userSignedUp")
	defer setFailureReporter(nil)

	// Build a marshalled job by hand to simulate the worker's wire-side
	// state: producer-side EventType is set, raw payload bytes present,
	// but the consumer has no event factory registered.
	payload, err := json.Marshal(&userSignedUp{ID: 99, Email: "z@y.x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	jobBytes, err := json.Marshal(EventListenerJob{
		Event:        payload,
		EventType:    "*events.userSignedUp",
		ListenerType: "events.capturingListener",
	})
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}

	// Register the listener factory so the listener-hydration branch
	// passes; intentionally do NOT register the event factory.
	RegisterListenerFactory("events.capturingListener", func() Listener {
		return &capturingListener{}
	})

	var reported atomic.Int32
	var reportedJob atomic.Pointer[EventListenerJob]
	setFailureReporter(func(job *EventListenerJob, _ error) {
		reported.Add(1)
		reportedJob.Store(job)
	})

	rawJob, err := EventJobFactory(jobBytes)
	if err != nil {
		t.Fatalf("EventJobFactory: %v", err)
	}
	hc, ok := rawJob.(queue.HandleCtxer)
	if !ok {
		t.Fatal("EventJobFactory output does not implement HandleCtxer")
	}

	handleErr := hc.HandleCtx(context.Background())
	if handleErr == nil {
		t.Fatal("expected HandleCtx to error with missing event factory")
	}
	if !errors.Is(handleErr, ErrEventTypeNotRegistered) {
		t.Fatalf("expected ErrEventTypeNotRegistered, got %v", handleErr)
	}

	// Simulate the queue driver calling Failed() after retries are
	// exhausted (the worker's terminal cleanup path).
	rawJob.Failed(handleErr)

	if got := reported.Load(); got != 1 {
		t.Fatalf("reporter invocation count: got %d want 1", got)
	}
	if rj := reportedJob.Load(); rj == nil || rj.EventType != "*events.userSignedUp" {
		t.Fatalf("reporter received wrong job: %+v", rj)
	}
}

// TestEventFactoryRegistry_Concurrent locks in the race-cleanliness of the
// event-factory registry. Concurrent RegisterEventFactory +
// lookupEventFactory + pushToQueue (via Dispatch) + HandleCtx (via
// EventJobFactory + HandleCtx) must pass under -race.
func TestEventFactoryRegistry_Concurrent(t *testing.T) {
	const (
		registrants = 4
		lookups     = 4
		dispatchers = 4
		consumers   = 4
		iters       = 200
	)

	defer UnregisterListenerFactory("events.capturingListener")
	defer UnregisterEventFactory("*events.userSignedUp")

	RegisterListenerFactory("events.capturingListener", func() Listener {
		return &capturingListener{}
	})
	RegisterEventFactory("*events.userSignedUp", func() interface{} {
		return &userSignedUp{}
	})

	drv := queue.NewMemoryDriver()
	defer drv.Shutdown(context.Background())

	d := NewQueueIntegratedDispatcher()
	d.SetQueueDriver(drv)
	d.Listen("user.signed.up", &capturingListener{})

	// Pre-build a wire-encoded job so consumers exercise the
	// HandleCtx event-factory lookup path under contention.
	payload, _ := json.Marshal(&userSignedUp{ID: 7, Email: "race@example.com"})
	jobBytes, _ := json.Marshal(EventListenerJob{
		Event:        payload,
		EventType:    "*events.userSignedUp",
		ListenerType: "events.capturingListener",
	})

	var wg sync.WaitGroup

	wg.Add(registrants)
	for i := 0; i < registrants; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				RegisterEventFactory("*events.userSignedUp", func() interface{} { return &userSignedUp{} })
			}
		}()
	}

	wg.Add(lookups)
	for i := 0; i < lookups; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				_, _ = lookupEventFactory("*events.userSignedUp")
			}
		}()
	}

	wg.Add(dispatchers)
	for i := 0; i < dispatchers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				_ = d.Dispatch(context.Background(), &userSignedUp{ID: j, Email: "x@y.z"})
			}
		}()
	}

	wg.Add(consumers)
	for i := 0; i < consumers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				rj, err := EventJobFactory(jobBytes)
				if err != nil {
					t.Errorf("EventJobFactory: %v", err)
					return
				}
				if hc, ok := rj.(queue.HandleCtxer); ok {
					_ = hc.HandleCtx(context.Background())
				}
			}
		}()
	}

	wg.Wait()
}
