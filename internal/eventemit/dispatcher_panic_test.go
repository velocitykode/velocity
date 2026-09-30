package eventemit

import (
	"context"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// A dispatcher that panics is recovered where the dispatch function
// records failures: the panic is recorded once, as a recovered panic, and
// returned marked, so the component that dispatched survives and does not
// record it again.
func TestFailures_Recording_RecoversADispatcherPanic(t *testing.T) {
	var f Failures
	hooked := make(chan error, 2)
	f.SetHook(func(err error, _ any) { hooked <- err })
	dispatch := f.Recording(hostile.NewDispatcher(hostile.New(t, hostile.Panic, nil)).Dispatch, &recordingLogger{})

	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("dispatcher panic escaped the recording boundary: %v", p)
			}
		}()
		err = dispatch(context.Background(), namedEvent{name: "cache.hit"})
	}()
	var rp contract.RecoveredPanic
	if !errors.As(err, &rp) || rp.Recovered() != hostile.PanicValue {
		t.Fatalf("dispatch returned %#v, want the recovered panic", err)
	}
	if !Recorded(err) {
		t.Errorf("returned error is not marked recorded")
	}
	if got := f.Count(); got != 1 {
		t.Errorf("Count = %d, want 1", got)
	}
	if got := len(hooked); got != 1 {
		t.Errorf("hook calls = %d, want 1", got)
	}

	// The component's emitter does not record it a second time.
	var e Emitter
	e.Share(&f)
	e.Set(dispatch)
	e.EmitBuilt(context.Background(), func() any { return namedEvent{name: "cache.hit"} })
	if got := f.Count(); got != 2 {
		t.Errorf("Count after an emit = %d, want 2 (one per dispatch)", got)
	}
}

// Emit contains a dispatcher that panics: the component that emitted
// survives, the panic is recorded once as a recovered panic (counted, its
// first failure logged, the hook called once), and a later emit through a
// working dispatcher is delivered as usual.
func TestEmitter_Emit_ContainsADispatcherPanic(t *testing.T) {
	var e Emitter
	var hooked []error
	e.failures().SetHook(func(err error, _ any) { hooked = append(hooked, err) })
	e.Set(hostile.NewDispatcher(hostile.New(t, hostile.Panic, nil)).Dispatch)

	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("dispatcher panic escaped Emit: %v", p)
			}
		}()
		if !e.EmitBuilt(context.Background(), func() any { return namedEvent{name: "cache.hit"} }) {
			t.Fatalf("Emit reported no dispatcher")
		}
	}()
	if got := e.FailureCount(); got != 1 {
		t.Errorf("FailureCount = %d, want 1", got)
	}
	if len(hooked) != 1 {
		t.Fatalf("hook calls = %d, want 1", len(hooked))
	}
	var rp contract.RecoveredPanic
	if !errors.As(hooked[0], &rp) || rp.Recovered() != hostile.PanicValue {
		t.Errorf("hook got %#v, want the recovered panic", hooked[0])
	}

	delivered := 0
	e.Set(func(context.Context, any) error { delivered++; return nil })
	e.EmitBuilt(context.Background(), func() any { return namedEvent{name: "cache.hit"} })
	if delivered != 1 || e.FailureCount() != 1 {
		t.Errorf("after recovery: delivered = %d, FailureCount = %d, want 1 and 1", delivered, e.FailureCount())
	}
}

// A dispatcher that emits on the same emitter from inside the dispatch
// (re-entry) and then panics is contained at each level, and each panic is
// recorded once.
func TestEmitter_Emit_ReentrantPanicsAreEachRecordedOnce(t *testing.T) {
	var e Emitter
	depth := 0
	e.Set(func(ctx context.Context, ev any) error {
		depth++
		if depth == 1 {
			e.EmitBuilt(ctx, func() any { return ev })
		}
		panic("dispatcher broke")
	})
	e.EmitBuilt(context.Background(), func() any { return namedEvent{name: "cache.hit"} })
	if got := e.FailureCount(); got != 2 {
		t.Errorf("FailureCount = %d, want 2", got)
	}
}

// panickingUnwrapError is a user error whose Unwrap runs its hostile code,
// as the walk in Recorded reaches it.
type panickingUnwrapError struct{ c *hostile.Code }

func (panickingUnwrapError) Error() string   { return "user error" }
func (e panickingUnwrapError) Unwrap() error { e.c.Run(); return nil }

// A failure whose error panics while Recorded walks it is still recorded
// once, and the panic does not escape Emit or Fail.
func TestEmitter_FailedErrorWithPanickingUnwrapIsRecorded(t *testing.T) {
	var e Emitter
	unwrap := panickingUnwrapError{c: hostile.New(t, hostile.Panic, nil)}
	e.Set(func(context.Context, any) error { return unwrap })
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("panic escaped Emit: %v", p)
			}
		}()
		e.EmitBuilt(context.Background(), func() any { return namedEvent{name: "cache.hit"} })
		e.Fail(context.Background(), unwrap, namedEvent{name: "cache.hit"})
	}()
	if got := e.FailureCount(); got != 2 {
		t.Errorf("FailureCount = %d, want 2", got)
	}
}

// panickingNameEvent is an event whose Name runs its hostile code.
type panickingNameEvent struct{ c *hostile.Code }

func (e panickingNameEvent) Name() string { e.c.Run(); return "evt" }

// A failure of an event whose Name panics is counted, logged under the
// event's Go type and handed to the hook, and the panic never escapes the
// policy (Record, Recording, Emit).
func TestFailures_EventWithPanickingNameIsRecorded(t *testing.T) {
	var f Failures
	hooks := 0
	f.SetHook(func(error, any) { hooks++ })
	logger := &recordingLogger{}
	event := panickingNameEvent{c: hostile.New(t, hostile.Panic, nil)}
	dispatch := f.Recording(func(context.Context, any) error { return errors.New("listener failed") }, logger)
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("panic escaped the failure policy: %v", p)
			}
		}()
		_ = dispatch(context.Background(), event)
	}()
	if f.Count() != 1 || hooks != 1 {
		t.Errorf("Count = %d, hooks = %d, want 1 and 1", f.Count(), hooks)
	}
	if got := EventName(event); got != "eventemit.panickingNameEvent" {
		t.Errorf("EventName = %q, want the Go type", got)
	}
}
