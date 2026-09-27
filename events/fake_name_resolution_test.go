package events

import (
	"context"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// OrderShipped has no Name method, so both dispatchers must derive its name
// from the type (order.shipped).
type OrderShipped struct{ ID int }

// firedListener records every event it handles.
type firedListener struct {
	mu     sync.Mutex
	events []interface{}
}

func (l *firedListener) Handle(_ context.Context, event interface{}) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
	return nil
}

func (l *firedListener) Async() bool { return false }

func (l *firedListener) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.events)
}

// TestFakeDispatcher_ResolvesListenersLikeDefault registers the same key on
// a DefaultDispatcher and on a non-faking FakeDispatcher, dispatches the
// same event through both, and requires that the listener fires on both or
// on neither.
func TestFakeDispatcher_ResolvesListenersLikeDefault(t *testing.T) {
	tests := []struct {
		name  string
		key   interface{}
		event interface{}
		fires bool
	}{
		{"exact string", "order.shipped", "order.shipped", true},
		{"string mismatch", "order.shipped", "order.created", false},
		{"type-derived name", "order.shipped", OrderShipped{ID: 1}, true},
		{"type-derived name via pointer", "order.shipped", &OrderShipped{ID: 1}, true},
		{"raw type name is not a name", "OrderShipped", OrderShipped{ID: 1}, false},
		{"Name method", "user.registered", UserRegistered{UserID: 1}, true},
		{"prefix pattern", "order.*", "order.shipped", true},
		{"prefix pattern across segments", "order.*", "order.line.added", true},
		{"suffix pattern", "*.shipped", OrderShipped{}, true},
		{"suffix pattern across segments", "*.failed", "queue.job.failed", true},
		{"match everything", "*", "anything.at.all", true},
		{"event value as key", OrderShipped{}, &OrderShipped{ID: 2}, true},
		{"type key", OfType[*OrderShipped](), &OrderShipped{ID: 3}, true},
		{"type key is exact", OfType[*OrderShipped](), OrderShipped{ID: 3}, false},
		{"interface key", OfType[contract.Event](), UserRegistered{UserID: 1}, true},
		{"interface key skips non-implementers", OfType[contract.Event](), OrderShipped{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			real := &firedListener{}
			d := NewDispatcher()
			d.Listen(tt.key, real)
			if err := d.Dispatch(ctx, tt.event); err != nil {
				t.Fatalf("DefaultDispatcher.Dispatch: %v", err)
			}

			faked := &firedListener{}
			f := NewFakeDispatcher()
			f.StopFaking()
			f.Listen(tt.key, faked)
			if err := f.Dispatch(ctx, tt.event); err != nil {
				t.Fatalf("FakeDispatcher.Dispatch: %v", err)
			}

			if got := real.count() == 1; got != tt.fires {
				t.Errorf("DefaultDispatcher fired = %v, want %v", got, tt.fires)
			}
			if got := faked.count() == 1; got != tt.fires {
				t.Errorf("FakeDispatcher fired = %v, want %v (DefaultDispatcher fired = %v)", got, tt.fires, real.count() == 1)
			}
			if got, want := f.HasListeners(tt.event), d.HasListeners(tt.event); got != want {
				t.Errorf("FakeDispatcher.HasListeners = %v, DefaultDispatcher.HasListeners = %v", got, want)
			}
		})
	}
}

// TestFakeDispatcher_AssertDispatchedByName asserts a recorded event under
// the name the DefaultDispatcher routes it by, whatever form it was
// dispatched in.
func TestFakeDispatcher_AssertDispatchedByName(t *testing.T) {
	ctx := context.Background()
	f := NewFakeDispatcher()
	_ = f.Dispatch(ctx, &OrderShipped{ID: 7})
	_ = f.Dispatch(ctx, UserRegistered{UserID: 1})
	_ = f.Dispatch(ctx, "cart.emptied")

	for _, name := range []string{"order.shipped", "user.registered", "cart.emptied"} {
		if err := f.AssertDispatched(name, nil); err != nil {
			t.Errorf("AssertDispatched(%q): %v", name, err)
		}
	}
	if err := f.AssertDispatchedTimes("order.shipped", 1); err != nil {
		t.Errorf("AssertDispatchedTimes(order.shipped, 1): %v", err)
	}
	if err := f.AssertDispatched("order.shipped", func(e interface{}) bool {
		s, ok := e.(*OrderShipped)
		return ok && s.ID == 7
	}); err != nil {
		t.Errorf("AssertDispatched(order.shipped, callback): %v", err)
	}

	// A name nothing was dispatched under must not match merely because
	// some string event was recorded.
	if err := f.AssertDispatched("cart.filled", nil); err == nil {
		t.Error("AssertDispatched(cart.filled) = nil, want an error: nothing was dispatched under that name")
	}
	if err := f.AssertNotDispatched("cart.filled"); err != nil {
		t.Errorf("AssertNotDispatched(cart.filled): %v", err)
	}
	if err := f.AssertNotDispatched("order.shipped"); err == nil {
		t.Error("AssertNotDispatched(order.shipped) = nil, want an error")
	}
}
