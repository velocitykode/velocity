package events

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/queue"
)

// scalarCapturingListener is a queued listener that records the last value
// its Handle received so the scalar round-trip tests can assert the
// listener saw the same form (string / bool / int / etc.) the producer
// dispatched.
type scalarCapturingListener struct {
	mu       sync.Mutex
	received interface{}
	done     chan struct{}
}

func (l *scalarCapturingListener) Handle(ctx context.Context, event interface{}) error {
	l.mu.Lock()
	l.received = event
	l.mu.Unlock()
	if l.done != nil {
		select {
		case l.done <- struct{}{}:
		default:
		}
	}
	return nil
}
func (l *scalarCapturingListener) Async() bool { return true }
func (l *scalarCapturingListener) Got() interface{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.received
}

// runQueueRoundTrip pushes the event through the queue and re-hydrates the
// job via queue.MarshalJob + queue.HydrateJob so the in-process pointer
// fast path is bypassed (the same shape a durable driver exercises on every
// pop). Returns the rehydrated job after running its HandleCtx.
func runQueueRoundTrip(t *testing.T, d *QueueIntegratedDispatcher, drv queue.Driver, event interface{}, listener *scalarCapturingListener, queueName string) {
	t.Helper()
	if err := d.Dispatch(context.Background(), event); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rawJob, err := drv.PopCtx(ctx, queueName)
	if err != nil {
		t.Fatalf("PopCtx: %v", err)
	}
	if rawJob == nil {
		t.Fatal("no job available")
	}

	payload, err := queue.MarshalJob(rawJob, queueName)
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
}

// TestScalarEventRoundTrip_String dispatches a bare string event through
// the queued-listener path WITHOUT any RegisterEventFactory call. Before
// the scalar shortcut, pushToQueue refused with ErrEventTypeNotRegistered
// because reflect.TypeOf("").String() == "string" had no registered
// factory. The fix recognises the scalar set and hydrates without a
// user factory; this test pins that contract.
func TestScalarEventRoundTrip_String(t *testing.T) {
	defer UnregisterListenerFactory("events.scalarCapturingListener")

	listener := &scalarCapturingListener{done: make(chan struct{}, 1)}
	RegisterListenerFactory("events.scalarCapturingListener", func() Listener { return listener })

	drv := queue.NewMemoryDriver()
	defer drv.Shutdown(context.Background())
	d := NewQueueIntegratedDispatcher()
	InitializeQueueIntegration(d, drv, nil)
	d.Listen("user.created", listener)

	const want = "user.created"
	runQueueRoundTrip(t, d, drv, want, listener, "default")

	got := listener.Got()
	if _, isMap := got.(map[string]interface{}); isMap {
		t.Fatalf("scalar event hydrated as map[string]any: %v", got)
	}
	s, ok := got.(string)
	if !ok {
		t.Fatalf("listener received %T, want string", got)
	}
	if s != want {
		t.Fatalf("listener received %q, want %q", s, want)
	}
}

// TestScalarEventRoundTrip_Bool dispatches a bool event through the
// queued-listener path without registering an event factory.
func TestScalarEventRoundTrip_Bool(t *testing.T) {
	defer UnregisterListenerFactory("events.scalarCapturingListener")

	listener := &scalarCapturingListener{done: make(chan struct{}, 1)}
	RegisterListenerFactory("events.scalarCapturingListener", func() Listener { return listener })

	drv := queue.NewMemoryDriver()
	defer drv.Shutdown(context.Background())
	d := NewQueueIntegratedDispatcher()
	InitializeQueueIntegration(d, drv, nil)
	d.Listen("bool", listener)

	// Pass true as a *bool addressable value? No -- bool collapses to "bool"
	// type name via reflect, and getEventName -> camelToDot("Bool") == "bool".
	runQueueRoundTrip(t, d, drv, true, listener, "default")

	got := listener.Got()
	b, ok := got.(bool)
	if !ok {
		t.Fatalf("listener received %T, want bool", got)
	}
	if !b {
		t.Fatalf("listener received %v, want true", b)
	}
}

// TestScalarEventRoundTrip_Int dispatches an int and asserts it round-trips
// as the same Go type. Goes through the queued path without a registered
// event factory.
func TestScalarEventRoundTrip_Int(t *testing.T) {
	defer UnregisterListenerFactory("events.scalarCapturingListener")

	listener := &scalarCapturingListener{done: make(chan struct{}, 1)}
	RegisterListenerFactory("events.scalarCapturingListener", func() Listener { return listener })

	drv := queue.NewMemoryDriver()
	defer drv.Shutdown(context.Background())
	d := NewQueueIntegratedDispatcher()
	InitializeQueueIntegration(d, drv, nil)
	d.Listen("int", listener)

	runQueueRoundTrip(t, d, drv, 42, listener, "default")

	got := listener.Got()
	n, ok := got.(int)
	if !ok {
		t.Fatalf("listener received %T, want int", got)
	}
	if n != 42 {
		t.Fatalf("listener received %d, want 42", n)
	}
}

// TestScalarEventRoundTrip_Int64 covers the int64 scalar specifically:
// JSON's default numeric unmarshal target is float64, so the scalar branch
// must declare an int64 target to preserve the producer-side type.
func TestScalarEventRoundTrip_Int64(t *testing.T) {
	defer UnregisterListenerFactory("events.scalarCapturingListener")

	listener := &scalarCapturingListener{done: make(chan struct{}, 1)}
	RegisterListenerFactory("events.scalarCapturingListener", func() Listener { return listener })

	drv := queue.NewMemoryDriver()
	defer drv.Shutdown(context.Background())
	d := NewQueueIntegratedDispatcher()
	InitializeQueueIntegration(d, drv, nil)
	d.Listen("int64", listener)

	runQueueRoundTrip(t, d, drv, int64(1<<40), listener, "default")

	got := listener.Got()
	n, ok := got.(int64)
	if !ok {
		t.Fatalf("listener received %T, want int64", got)
	}
	if want := int64(1 << 40); n != want {
		t.Fatalf("listener received %d, want %d", n, want)
	}
}

// TestScalarEventRoundTrip_Float64 covers the float64 scalar.
func TestScalarEventRoundTrip_Float64(t *testing.T) {
	defer UnregisterListenerFactory("events.scalarCapturingListener")

	listener := &scalarCapturingListener{done: make(chan struct{}, 1)}
	RegisterListenerFactory("events.scalarCapturingListener", func() Listener { return listener })

	drv := queue.NewMemoryDriver()
	defer drv.Shutdown(context.Background())
	d := NewQueueIntegratedDispatcher()
	InitializeQueueIntegration(d, drv, nil)
	d.Listen("float64", listener)

	runQueueRoundTrip(t, d, drv, 3.5, listener, "default")

	got := listener.Got()
	f, ok := got.(float64)
	if !ok {
		t.Fatalf("listener received %T, want float64", got)
	}
	if f != 3.5 {
		t.Fatalf("listener received %v, want 3.5", f)
	}
}

// TestScalarEventRoundTrip_Bytes covers the []byte scalar: the producer
// dispatches []byte, the listener should observe []byte after the round
// trip. json.Marshal emits a base64 string for []byte; the scalar branch
// must mirror that on unmarshal.
func TestScalarEventRoundTrip_Bytes(t *testing.T) {
	defer UnregisterListenerFactory("events.scalarCapturingListener")

	listener := &scalarCapturingListener{done: make(chan struct{}, 1)}
	RegisterListenerFactory("events.scalarCapturingListener", func() Listener { return listener })

	drv := queue.NewMemoryDriver()
	defer drv.Shutdown(context.Background())
	d := NewQueueIntegratedDispatcher()
	InitializeQueueIntegration(d, drv, nil)
	d.Listen("[]uint8", listener)

	payload := []byte{0x01, 0x02, 0x03, 0xff}
	runQueueRoundTrip(t, d, drv, payload, listener, "default")

	got := listener.Got()
	b, ok := got.([]byte)
	if !ok {
		t.Fatalf("listener received %T, want []byte", got)
	}
	if !reflect.DeepEqual(b, payload) {
		t.Fatalf("listener received %v, want %v", b, payload)
	}
}

// TestScalarEventRoundTrip_JSONRawMessage covers json.RawMessage, which has
// the same underlying type as []byte but reflect emits the distinct name
// "json.RawMessage". Both keys are recognised by the scalar branch.
func TestScalarEventRoundTrip_JSONRawMessage(t *testing.T) {
	defer UnregisterListenerFactory("events.scalarCapturingListener")

	listener := &scalarCapturingListener{done: make(chan struct{}, 1)}
	RegisterListenerFactory("events.scalarCapturingListener", func() Listener { return listener })

	drv := queue.NewMemoryDriver()
	defer drv.Shutdown(context.Background())
	d := NewQueueIntegratedDispatcher()
	InitializeQueueIntegration(d, drv, nil)
	d.Listen("raw.message", listener)

	payload := json.RawMessage(`{"a":1}`)
	runQueueRoundTrip(t, d, drv, payload, listener, "default")

	got := listener.Got()
	rm, ok := got.(json.RawMessage)
	if !ok {
		t.Fatalf("listener received %T, want json.RawMessage", got)
	}
	if !reflect.DeepEqual(rm, payload) {
		t.Fatalf("listener received %s, want %s", rm, payload)
	}
}

// TestPushToQueue_AllowsScalarWithoutFactory asserts the dispatch-side
// guard is exempt from the factory requirement for built-in scalars. A
// regression here would block any app that dispatches string-named events
// through a queued listener.
func TestPushToQueue_AllowsScalarWithoutFactory(t *testing.T) {
	defer UnregisterListenerFactory("events.scalarCapturingListener")

	listener := &scalarCapturingListener{done: make(chan struct{}, 1)}
	RegisterListenerFactory("events.scalarCapturingListener", func() Listener { return listener })

	drv := queue.NewMemoryDriver()
	defer drv.Shutdown(context.Background())
	d := NewQueueIntegratedDispatcher()
	d.SetQueueDriver(drv)
	d.Listen("user.created", listener)

	if err := d.Dispatch(context.Background(), "user.created"); err != nil {
		t.Fatalf("Dispatch refused a scalar string without a registered event factory: %v", err)
	}

	// One job must have landed on the queue.
	if size, _ := drv.Size("default"); size != 1 {
		t.Fatalf("queue size after scalar push: got %d want 1", size)
	}
}

// TestIsScalarEventType_TableDriven locks in the scalar predicate so future
// edits do not silently drop coverage of a primitive type.
func TestIsScalarEventType_TableDriven(t *testing.T) {
	cases := map[string]bool{
		"string":           true,
		"bool":             true,
		"int":              true,
		"int8":             true,
		"int16":            true,
		"int32":            true,
		"int64":            true,
		"uint":             true,
		"uint8":            true,
		"uint16":           true,
		"uint32":           true,
		"uint64":           true,
		"float32":          true,
		"float64":          true,
		"[]uint8":          true,
		"json.RawMessage":  true,
		"pkg.UserSignedUp": false,
		"*pkg.UserFoo":     false,
		"map[string]any":   false,
		"":                 false,
	}
	for name, want := range cases {
		if got := isScalarEventType(name); got != want {
			t.Errorf("isScalarEventType(%q) = %v, want %v", name, got, want)
		}
	}
}
