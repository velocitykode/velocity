package orm

import (
	"context"
	"testing"
	"time"
)

// A relay owns its dispatches' goroutines, and only its own.
func TestRelay_OwnsCaller(t *testing.T) {
	m, _ := newOutboxFileManager(t)
	enqueueOne(t, m)
	other := fastRelay(m, RelayCallbacks{})
	var relay *Relay
	got := make(chan [2]bool, 1)
	relay = fastRelay(m, RelayCallbacks{OnJob: func(context.Context, any, string, string) error {
		select {
		case got <- [2]bool{relay.OwnsCaller(), other.OwnsCaller()}:
		default:
		}
		return nil
	}})
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = relay.Stop(context.Background()) })
	select {
	case g := <-got:
		if !g[0] || g[1] {
			t.Fatalf("in a dispatch: relay.OwnsCaller = %v, other.OwnsCaller = %v; want true, false", g[0], g[1])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the dispatch never ran")
	}
	if relay.OwnsCaller() {
		t.Fatal("OwnsCaller = true on the test goroutine")
	}
}

// A manager owns the goroutine delivering its statement events to a
// listener, and only its own.
func TestManager_OwnsCaller(t *testing.T) {
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	other := newTestManager(t)
	t.Cleanup(func() { _ = other.Shutdown(context.Background()) })
	got := make(chan [2]bool, 1)
	m.SetEventDispatcher(func(_ context.Context, ev any) error {
		if _, ok := ev.(*QueryExecuted); ok {
			select {
			case got <- [2]bool{m.OwnsCaller(), other.OwnsCaller()}:
			default:
			}
		}
		return nil
	})
	ctx := context.Background()
	if _, err := m.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if err := m.FlushQueryEvents(ctx); err != nil {
		t.Fatalf("FlushQueryEvents: %v", err)
	}
	select {
	case g := <-got:
		if !g[0] || g[1] {
			t.Fatalf("in a listener: m.OwnsCaller = %v, other.OwnsCaller = %v; want true, false", g[0], g[1])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the listener never ran")
	}
	if m.OwnsCaller() {
		t.Fatal("OwnsCaller = true on the test goroutine")
	}
}
