package eventemit

import (
	"context"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// A dispatcher that panics is recovered where the dispatch function
// records failures: the panic is recorded once, as a recovered panic, and
// returned marked, so the component that dispatched survives and does not
// record it again.
func TestFailures_Recording_RecoversADispatcherPanic(t *testing.T) {
	var f Failures
	hooked := make(chan error, 2)
	f.SetHook(func(err error, _ any) { hooked <- err })
	dispatch := f.Recording(func(context.Context, any) error { panic("dispatcher broke") }, &recordingLogger{})

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
	if !errors.As(err, &rp) || rp.Recovered() != "dispatcher broke" {
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
	e.Emit(context.Background(), namedEvent{name: "cache.hit"})
	if got := f.Count(); got != 2 {
		t.Errorf("Count after an emit = %d, want 2 (one per dispatch)", got)
	}
}
