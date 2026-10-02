package events

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/internal/panicerr"
)

// hostileObserver panics on Created with a value whose Error method
// panics too, so the panic error's text must come through errchain.
type hostileObserver struct {
	BaseObserver
	calls atomic.Int32
}

type hostilePanicValue struct{}

func (hostilePanicValue) Error() string { panic("Error ran") }

func (o *hostileObserver) Created(context.Context, interface{}) error {
	o.calls.Add(1)
	panic(hostilePanicValue{})
}

// countingObserver counts the Created calls it receives.
type countingObserver struct {
	BaseObserver
	calls atomic.Int32
}

func (o *countingObserver) Created(context.Context, interface{}) error {
	o.calls.Add(1)
	return nil
}

// A model observer that panics fails FireModelEvent with the typed panic
// error, the way an observer error fails it: no panic reaches the caller,
// the later observers are not called and the ModelEvent is not
// dispatched. The text is the errchain text, which a panicking Error
// method cannot break.
func TestFireModelEvent_ObserverPanicIsContained(t *testing.T) {
	d := NewObservableDispatcher()
	hostile := &hostileObserver{}
	after := &countingObserver{}
	d.Observe("TestUser", hostile)
	d.Observe("TestUser", after)
	capture := &modelEventCapture{}
	d.Listen(OfType[*ModelEvent](), capture)

	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("FireModelEvent panicked: %v", p)
			}
		}()
		err = d.FireModelEvent(context.Background(), "created", &TestUser{ID: 1})
	}()

	pe := panicerr.AsTyped(err)
	if pe == nil {
		t.Fatalf("FireModelEvent = %v (%T), want a *panicerr.Error", err, err)
	}
	if _, ok := pe.Recovered().(hostilePanicValue); !ok {
		t.Errorf("recovered value = %T, want hostilePanicValue", pe.Recovered())
	}
	if got := pe.Error(); !strings.HasPrefix(got, "panic: ") || strings.Contains(got, "Error ran") {
		t.Errorf("panic error text = %q, want the errchain fallback text", got)
	}
	if got, want := err.Error(), `velocity/events: observer *events.hostileObserver panicked on "created": panic: `; !strings.HasPrefix(got, want) {
		t.Errorf("error text = %q, want it to name the observer and event: %q...", got, want)
	}
	if n := hostile.calls.Load(); n != 1 {
		t.Errorf("hostile observer called %d times, want 1", n)
	}
	if n := after.calls.Load(); n != 0 {
		t.Errorf("later observer called %d times, want 0", n)
	}
	if got := capture.all(); len(got) != 0 {
		t.Errorf("listener saw %d ModelEvents after a failed observer, want 0", len(got))
	}

	// The registry stays usable: an observer that does not panic fires.
	d2 := NewObservableDispatcher()
	ok := &countingObserver{}
	d2.Observe("TestUser", ok)
	if err := d2.FireModelEvent(context.Background(), "created", &TestUser{ID: 2}); err != nil {
		t.Fatalf("FireModelEvent = %v", err)
	}
	if ok.calls.Load() != 1 {
		t.Errorf("observer called %d times, want 1", ok.calls.Load())
	}
}

// Every lifecycle method is contained, not only Created.
func TestObserverRegistry_EveryEventContained(t *testing.T) {
	events := []string{"creating", "created", "updating", "updated", "saving", "saved", "deleting", "deleted", "restoring", "restored"}
	r := NewObserverRegistry()
	r.Observe("TestUser", NewConditionalObserver(&BaseObserver{}, func(_ context.Context, ev string, _ interface{}) bool {
		panic("condition " + ev)
	}))
	for _, ev := range events {
		err := r.Fire(context.Background(), ev, &TestUser{})
		pe := panicerr.AsTyped(err)
		if pe == nil || pe.Recovered() != "condition "+ev {
			t.Errorf("Fire(%s) = %v, want the contained panic", ev, err)
		}
	}
}

// A typed nil model is a nil model: FireModelEvent and Fire return the
// error an untyped nil gets, and no observer or listener sees it.
func TestFireModelEvent_TypedNilModel(t *testing.T) {
	d := NewObservableDispatcher()
	obs := &countingObserver{}
	d.Observe("TestUser", obs)
	capture := &modelEventCapture{}
	d.Listen(OfType[*ModelEvent](), capture)

	untyped := d.FireModelEvent(context.Background(), "created", nil)
	for _, model := range []any{(*TestUser)(nil), (*panickyModel)(nil)} {
		err := d.FireModelEvent(context.Background(), "created", model)
		if !errors.Is(err, errNilModel) || err != untyped {
			t.Errorf("FireModelEvent(%T nil) = %v, want %v", model, err, untyped)
		}
		if err := d.registry.Fire(context.Background(), "created", model); !errors.Is(err, errNilModel) {
			t.Errorf("Fire(%T nil) = %v, want errNilModel", model, err)
		}
	}
	if obs.calls.Load() != 0 {
		t.Errorf("observer called %d times for a nil model", obs.calls.Load())
	}
	if got := capture.all(); len(got) != 0 {
		t.Errorf("listener saw %d ModelEvents for a nil model", len(got))
	}
}

// Concurrent Fire on one registry, half the models observed by a
// panicking observer, while observers are added: no panic escapes, each
// call returns its own observer's outcome. Run with -race -cpu 1,2.
func TestObserverRegistry_ConcurrentFireContained(t *testing.T) {
	r := NewObserverRegistry()
	hostile := &hostileObserver{}
	ok := &countingObserver{}
	r.Observe("TestUser", ok)
	r.Observe("hostileModel", hostile)

	const workers, iters = 16, 200
	var wg sync.WaitGroup
	var escaped, contained, clean atomic.Int32
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			defer func() {
				if recover() != nil {
					escaped.Add(1)
				}
			}()
			for i := 0; i < iters; i++ {
				if w%2 == 0 {
					if panicerr.AsTyped(r.Fire(context.Background(), "created", &hostileModel{})) != nil {
						contained.Add(1)
					}
				} else if err := r.Fire(context.Background(), "created", &TestUser{ID: i}); err == nil {
					clean.Add(1)
				}
				if i%50 == 0 {
					r.Observe(fmt.Sprintf("Other%d", w), &BaseObserver{})
				}
			}
		}(w)
	}
	wg.Wait()
	want := int32(workers / 2 * iters)
	if escaped.Load() != 0 || contained.Load() != want || clean.Load() != want {
		t.Fatalf("escaped=%d contained=%d clean=%d, want 0/%d/%d", escaped.Load(), contained.Load(), clean.Load(), want, want)
	}
	if hostile.calls.Load() != want || ok.calls.Load() != want {
		t.Fatalf("hostile=%d ok=%d calls, want %d each", hostile.calls.Load(), ok.calls.Load(), want)
	}
}

type hostileModel struct{}

// With no observer registered, Fire does not allocate: the containment
// adds nothing to the empty fan-out. Held to zero allocs/op by
// scripts/ci/check-zero-alloc-benchmarks.sh.
func BenchmarkObserverFire_NoObserver(b *testing.B) {
	r := NewObserverRegistry()
	user := &TestUser{ID: 1}
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = r.Fire(ctx, "created", user)
	}
}

// The fake dispatcher runs listeners contained, as the real one does.
func TestFakeDispatcher_ListenerPanicIsContained(t *testing.T) {
	f := NewFakeDispatcher()
	f.StopFaking()
	f.Listen("probe.event", &observerTestPanicListener{})
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("Dispatch panicked: %v", p)
			}
		}()
		err = f.Dispatch(context.Background(), &BaseEvent{EventName: "probe.event"})
	}()
	if panicerr.AsTyped(err) == nil {
		t.Fatalf("Dispatch = %v, want the contained panic", err)
	}
}

type observerTestPanicListener struct{}

func (observerTestPanicListener) Handle(context.Context, interface{}) error {
	panic("listener panicked")
}
func (observerTestPanicListener) Async() bool { return false }

// A queued listener job whose listener panics returns the typed panic
// error from HandleCtx on any caller, as the worker would make of it.
func TestEventListenerJob_HandleCtxPanicIsContained(t *testing.T) {
	job := &EventListenerJob{listener: observerTestPanicListener{}, event: &BaseEvent{EventName: "x"}}
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("HandleCtx panicked: %v", p)
			}
		}()
		err = job.HandleCtx(context.Background())
	}()
	if pe := panicerr.AsTyped(err); pe == nil || pe.Recovered() != "listener panicked" {
		t.Fatalf("HandleCtx = %v, want the contained panic", err)
	}
}

// priorityListener has a priority and records its delivery.
type priorityListener struct {
	prio  int
	panic bool
	order *[]int
	mu    *sync.Mutex
}

func (l priorityListener) Handle(context.Context, interface{}) error {
	l.mu.Lock()
	*l.order = append(*l.order, l.prio)
	l.mu.Unlock()
	return nil
}
func (l priorityListener) Async() bool { return false }
func (l priorityListener) Priority() int {
	if l.panic {
		panic("priority panicked")
	}
	return l.prio
}

// A listener whose Priority panics fails its own delivery with that
// panic; the other listeners still run, in priority order.
func TestPriorityDispatcher_PriorityPanicFailsThatListener(t *testing.T) {
	d := NewPriorityDispatcher()
	var order []int
	var mu sync.Mutex
	d.Listen("probe.event", priorityListener{prio: 1, order: &order, mu: &mu})
	d.Listen("probe.event", priorityListener{prio: 99, panic: true, order: &order, mu: &mu})
	d.Listen("probe.event", priorityListener{prio: 5, order: &order, mu: &mu})
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("Dispatch panicked: %v", p)
			}
		}()
		err = d.Dispatch(context.Background(), &BaseEvent{EventName: "probe.event"})
	}()
	if pe := panicerr.AsTyped(err); pe == nil || pe.Recovered() != "priority panicked" {
		t.Fatalf("Dispatch = %v, want the contained Priority panic", err)
	}
	if fmt.Sprint(order) != "[5 1]" {
		t.Errorf("delivery order = %v, want [5 1]", order)
	}
}
