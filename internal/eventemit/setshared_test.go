package eventemit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// A dispatch in flight when SetShared hands the emitter to a new owner
// records its failure in the Failures and logger bound with the dispatcher
// it ran, never in the new owner's.
func TestEmitter_SetShared_InFlightFailureStaysWithItsOwner(t *testing.T) {
	var e Emitter
	oldF, newF := &Failures{}, &Failures{}
	oldLog, newLog := &recordingLogger{}, &recordingLogger{}
	entered, release := make(chan struct{}), make(chan struct{})
	e.SetShared(func(context.Context, any) error {
		close(entered)
		<-release
		return errListener
	}, oldF, func() contract.Logger { return oldLog })

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.EmitBuilt(context.Background(), func() any { return namedEvent{"a"} })
	}()
	<-entered
	e.SetShared(failing, newF, func() contract.Logger { return newLog })
	close(release)
	<-done

	if oldF.Count() != 1 || newF.Count() != 0 {
		t.Errorf("old owner counted %d, new owner %d; want 1 and 0", oldF.Count(), newF.Count())
	}
	if len(oldLog.at("warn")) != 1 || len(newLog.at("warn")) != 0 {
		t.Errorf("old owner logged %d warn lines, new owner %d; want 1 and 0", len(oldLog.at("warn")), len(newLog.at("warn")))
	}
}

// SetShared races Emit from many goroutines while the emitter is handed
// between owners: every failed dispatch is counted exactly once, in the
// Failures installed together with the dispatcher that ran.
func TestEmitter_SetShared_ConcurrentHandoverKeepsThePair(t *testing.T) {
	const owners = 4
	type owner struct {
		f          Failures
		dispatched atomic.Uint64
		fn         func(context.Context, any) error
	}
	var all [owners]*owner
	for i := range all {
		o := &owner{}
		o.fn = func(context.Context, any) error { o.dispatched.Add(1); return errListener }
		all[i] = o
	}
	var e Emitter
	e.SetShared(all[0].fn, &all[0].f, nil)

	stop := make(chan struct{})
	var configurer sync.WaitGroup
	configurer.Add(1)
	go func() {
		defer configurer.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			o := all[i%owners]
			e.SetShared(o.fn, &o.f, nil)
		}
	}()
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				e.EmitBuilt(context.Background(), func() any { return namedEvent{"a"} })
			}
		}()
	}
	wg.Wait()
	close(stop)
	configurer.Wait()

	for i, o := range all {
		if got, want := o.f.Count(), o.dispatched.Load(); got != want {
			t.Errorf("owner %d: counted %d failures for %d failed dispatches of its dispatcher", i, got, want)
		}
	}
}

// SetShared with nils removes the dispatcher and returns the emitter to
// its own Failures and the fallback logger; Share and Set after SetShared
// change only their own part.
func TestEmitter_SetShared_NilAndPartialUpdates(t *testing.T) {
	var e Emitter
	f := &Failures{}
	e.SetShared(failing, f, nil)
	if !e.Installed() || e.Dispatcher() == nil {
		t.Fatal("SetShared(fn, ...) did not install fn")
	}
	e.Share(nil)
	if !e.Installed() {
		t.Error("Share(nil) removed the dispatcher")
	}
	e.EmitBuilt(context.Background(), func() any { return namedEvent{"a"} })
	if f.Count() != 0 || e.FailureCount() != 1 {
		t.Errorf("after Share(nil): shared = %d, own = %d; want 0 and 1", f.Count(), e.FailureCount())
	}
	e.SetShared(failing, f, nil)
	e.Set(nil)
	if e.Installed() || e.Dispatcher() != nil {
		t.Error("Set(nil) left a dispatcher")
	}
	e.Fail(context.Background(), errListener, namedEvent{"a"})
	if f.Count() != 1 {
		t.Errorf("Set(nil) dropped the shared Failures: shared = %d, want 1", f.Count())
	}
	e.SetShared(nil, nil, nil)
	if e.Installed() {
		t.Error("SetShared(nil, nil, nil) left a dispatcher")
	}
	if e.EmitBuilt(context.Background(), func() any { return namedEvent{"a"} }) {
		t.Error("Emit reported a dispatcher after SetShared(nil, nil, nil)")
	}
	e.Fail(context.Background(), errListener, namedEvent{"a"})
	if f.Count() != 1 || e.FailureCount() != 2 {
		t.Errorf("after SetShared(nil, nil, nil): shared = %d, own = %d; want 1 and 2", f.Count(), e.FailureCount())
	}
}
