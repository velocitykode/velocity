package velocity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/internal/eventemit"
)

// panickingDispatcher is a dispatcher whose Dispatch panics.
type panickingDispatcher struct{ *events.DefaultDispatcher }

func (panickingDispatcher) Dispatch(context.Context, interface{}) error { panic("dispatcher broke") }

// swapEventsModule installs its dispatcher as Services.Events in Start.
type swapEventsModule struct{ d contract.Dispatcher }

func (swapEventsModule) Init(*app.Services) error       { return nil }
func (m swapEventsModule) Start(s *app.Services) error  { s.Events = m.d; return nil }
func (swapEventsModule) Shutdown(context.Context) error { return nil }

// A dispatcher a module installed that panics does not fail the framework
// operation that dispatched: the panic is counted once, handed to the hook
// once as a recovered panic, and never escapes.
func TestFailedEventCount_PanickingDispatcherRecordedOnce(t *testing.T) {
	rec := &hookRecorder{}
	a, _ := newLoggerWiringApp(t, nil,
		WithFailedEventHook(rec.hook),
		WithModules(swapEventsModule{d: panickingDispatcher{events.NewDispatcher()}}))
	if err := a.Cache.Put("k", "v", time.Minute); err != nil {
		t.Fatalf("put: %v", err)
	}
	before, beforeCalls := a.FailedEventCount(), rec.calls()

	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("the dispatcher's panic escaped the cache read: %v", p)
			}
		}()
		a.Cache.Get("k")
	}()
	if got := a.FailedEventCount() - before; got != 1 {
		t.Errorf("FailedEventCount grew by %d, want 1", got)
	}
	if got := rec.calls() - beforeCalls; got != 1 {
		t.Fatalf("hook calls grew by %d, want 1", got)
	}
	rec.mu.Lock()
	err := rec.errs[len(rec.errs)-1]
	rec.mu.Unlock()
	var rp contract.RecoveredPanic
	if !errors.As(err, &rp) || rp.Recovered() != "dispatcher broke" {
		t.Errorf("hook got %#v, want the recovered panic", err)
	}
	if eventemit.Recorded(err) {
		t.Errorf("hook got the marked error, want the failure itself")
	}
}
