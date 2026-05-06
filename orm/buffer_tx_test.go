package orm

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/events"
)

// txDomainEvent is a stand-in domain event used by the buffer integration
// tests. It does not implement orm.Event on purpose to verify that user
// events flow through the untyped flush path.
type txDomainEvent struct{ Tag string }

// TestBuffer_CommitFlushes verifies that events emitted via
// events.Buffer(ctx) inside Transaction fire only after commit.
func TestBuffer_CommitFlushes(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	var fired []any
	m.SetEventDispatcher(func(e any) error {
		fired = append(fired, e)
		return nil
	})

	ctx := events.PrepareBuffer(context.Background())
	err := m.Transaction(ctx, func(tx *sql.Tx) error {
		// In-tx the buffer should be reachable and fired events
		// must NOT have flushed yet.
		if !events.HasBuffer(ctx) {
			t.Fatal("HasBuffer returned false inside Transaction")
		}
		if err := events.Buffer(ctx).Dispatch(&txDomainEvent{Tag: "e1"}); err != nil {
			t.Fatalf("Dispatch e1: %v", err)
		}
		if err := events.Buffer(ctx).Dispatch(&txDomainEvent{Tag: "e2"}); err != nil {
			t.Fatalf("Dispatch e2: %v", err)
		}
		// Buffered events must not have reached the dispatcher yet
		// (other than orm internal events, which we filter).
		for _, e := range fired {
			if _, ok := e.(*txDomainEvent); ok {
				t.Fatalf("domain event fired before commit: %#v", e)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction returned %v", err)
	}

	var seen []string
	for _, e := range fired {
		if d, ok := e.(*txDomainEvent); ok {
			seen = append(seen, d.Tag)
		}
	}
	if len(seen) != 2 || seen[0] != "e1" || seen[1] != "e2" {
		t.Fatalf("flushed domain events = %v, want [e1 e2]", seen)
	}

	// After Transaction returns, the buffer slot must be empty so
	// subsequent buffered dispatches against the same ctx are
	// standalone (silently discarded).
	if events.HasBuffer(ctx) {
		t.Fatal("HasBuffer returned true after Transaction returned")
	}
}

// TestBuffer_RollbackDrops verifies events buffered inside a tx that
// returns an error never reach the dispatcher.
func TestBuffer_RollbackDrops(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	var fired []any
	m.SetEventDispatcher(func(e any) error {
		fired = append(fired, e)
		return nil
	})

	rollbackErr := errors.New("rollback please")
	ctx := events.PrepareBuffer(context.Background())
	err := m.Transaction(ctx, func(tx *sql.Tx) error {
		_ = events.Buffer(ctx).Dispatch(&txDomainEvent{Tag: "drop-me"})
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("Transaction err = %v, want %v", err, rollbackErr)
	}
	for _, e := range fired {
		if _, ok := e.(*txDomainEvent); ok {
			t.Fatalf("domain event fired after rollback: %#v", e)
		}
	}
}

// TestBuffer_PanicDrops verifies that a panic inside the tx fn drops
// buffered events (and the panic still propagates).
func TestBuffer_PanicDrops(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	var fired []any
	m.SetEventDispatcher(func(e any) error {
		fired = append(fired, e)
		return nil
	})

	ctx := events.PrepareBuffer(context.Background())
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic to propagate from Transaction")
			}
		}()
		_ = m.Transaction(ctx, func(tx *sql.Tx) error {
			_ = events.Buffer(ctx).Dispatch(&txDomainEvent{Tag: "panic-drop"})
			panic("kaboom")
		})
	}()
	for _, e := range fired {
		if _, ok := e.(*txDomainEvent); ok {
			t.Fatalf("domain event fired after panic: %#v", e)
		}
	}
}

// TestBuffer_Nested verifies nested Transaction semantics: inner
// rollback drops only inner events, outer commit flushes outer events.
func TestBuffer_Nested(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	var fired []*txDomainEvent
	m.SetEventDispatcher(func(e any) error {
		if d, ok := e.(*txDomainEvent); ok {
			fired = append(fired, d)
		}
		return nil
	})

	innerErr := errors.New("inner rollback")
	ctx := events.PrepareBuffer(context.Background())
	err := m.Transaction(ctx, func(tx *sql.Tx) error {
		_ = events.Buffer(ctx).Dispatch(&txDomainEvent{Tag: "outer-1"})

		// Nested transaction that fails -- only inner events should drop.
		nestedErr := m.Transaction(ctx, func(_ *sql.Tx) error {
			_ = events.Buffer(ctx).Dispatch(&txDomainEvent{Tag: "inner-1"})
			return innerErr
		})
		if !errors.Is(nestedErr, innerErr) {
			t.Fatalf("nested Transaction err = %v, want %v", nestedErr, innerErr)
		}

		_ = events.Buffer(ctx).Dispatch(&txDomainEvent{Tag: "outer-2"})
		return nil
	})
	if err != nil {
		t.Fatalf("outer Transaction returned %v", err)
	}

	var tags []string
	for _, e := range fired {
		tags = append(tags, e.Tag)
	}
	if len(tags) != 2 || tags[0] != "outer-1" || tags[1] != "outer-2" {
		t.Fatalf("flushed tags = %v, want [outer-1 outer-2]", tags)
	}
}

// TestBuffer_PanicInListenerAfterFlush verifies that a panic inside a
// flushed listener does not corrupt buffer cleanup (the buffer slot is
// cleared by the deferred release regardless).
func TestBuffer_PanicInListenerAfterFlush(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	var calls atomic.Int32
	m.SetEventDispatcher(func(e any) error {
		if _, ok := e.(*txDomainEvent); ok {
			calls.Add(1)
			panic("listener boom")
		}
		return nil
	})

	ctx := events.PrepareBuffer(context.Background())
	func() {
		defer func() { _ = recover() }()
		_ = m.Transaction(ctx, func(tx *sql.Tx) error {
			_ = events.Buffer(ctx).Dispatch(&txDomainEvent{Tag: "x"})
			return nil
		})
	}()
	if calls.Load() != 1 {
		t.Fatalf("listener calls = %d, want 1", calls.Load())
	}
	// The holder slot must be empty regardless of listener panic.
	if events.HasBuffer(ctx) {
		t.Fatal("HasBuffer returned true after Transaction with panicking listener")
	}
}

// TestBuffer_Concurrent verifies multiple concurrent transactions each
// get their own buffer (no cross-talk) under -race.
func TestBuffer_Concurrent(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	const goroutines = 16
	const perTx = 8

	var (
		mu    sync.Mutex
		fired = make(map[int]int) // gID -> count
	)
	m.SetEventDispatcher(func(e any) error {
		if d, ok := e.(*txDomainEventID); ok {
			mu.Lock()
			fired[d.GID]++
			mu.Unlock()
		}
		return nil
	})

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			ctx := events.PrepareBuffer(context.Background())
			err := m.Transaction(ctx, func(tx *sql.Tx) error {
				for i := 0; i < perTx; i++ {
					_ = events.Buffer(ctx).Dispatch(&txDomainEventID{GID: g})
				}
				return nil
			})
			if err != nil {
				t.Errorf("g=%d Transaction: %v", g, err)
			}
		}(g)
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(fired) != goroutines {
		t.Fatalf("fired groups = %d, want %d", len(fired), goroutines)
	}
	for g, n := range fired {
		if n != perTx {
			t.Fatalf("g=%d count = %d, want %d", g, n, perTx)
		}
	}
}

// txDomainEventID is a per-goroutine domain event used by the concurrent
// integration test.
type txDomainEventID struct{ GID int }

// TestBuffer_FakeDispatcher_Integration verifies that a FakeDispatcher
// installed as the orm event sink records flushed events as if they had
// been dispatched directly.
func TestBuffer_FakeDispatcher_Integration(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	fake := events.NewFakeDispatcher()
	m.SetEventDispatcher(func(e any) error {
		return fake.Dispatch(e)
	})

	ctx := events.PrepareBuffer(context.Background())
	err := m.Transaction(ctx, func(tx *sql.Tx) error {
		_ = events.Buffer(ctx).Dispatch(&txDomainEvent{Tag: "fake-1"})
		// Pre-flush, fake should not contain the domain event.
		if err := fake.AssertNotDispatched(&txDomainEvent{}); err != nil {
			t.Fatalf("domain event reached fake before commit: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if err := fake.AssertDispatched(&txDomainEvent{}, func(e interface{}) bool {
		d, ok := e.(*txDomainEvent)
		return ok && d.Tag == "fake-1"
	}); err != nil {
		t.Fatalf("AssertDispatched: %v", err)
	}
}

// TestBuffer_NoEventDispatcher verifies Transaction works when no
// dispatcher is wired (buffered events are silently discarded on
// commit).
func TestBuffer_NoEventDispatcher(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	ctx := events.PrepareBuffer(context.Background())
	err := m.Transaction(ctx, func(tx *sql.Tx) error {
		_ = events.Buffer(ctx).Dispatch(&txDomainEvent{Tag: "no-sink"})
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
}

// TestBuffer_TxEventBusKindRouting verifies that when SetTxEventBus is
// wired the buffered flush routes each entry through the matching
// dispatcher method so DispatchAsync / DispatchAfter / Until preserve
// their original semantics across the transactional buffer.
func TestBuffer_TxEventBusKindRouting(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	bus := &recordingDispatcher{}
	m.SetTxEventBus(bus)

	ctx := events.PrepareBuffer(context.Background())
	err := m.Transaction(ctx, func(tx *sql.Tx) error {
		_ = events.Buffer(ctx).Dispatch(&txDomainEvent{Tag: "sync"})
		_ = events.Buffer(ctx).DispatchNow(&txDomainEvent{Tag: "now"})
		_ = events.Buffer(ctx).DispatchAsync(&txDomainEvent{Tag: "async"})
		_ = events.Buffer(ctx).DispatchAfter(&txDomainEvent{Tag: "after"}, 7)
		if _, err := events.Buffer(ctx).Until(&txDomainEvent{Tag: "until"}); err != nil {
			t.Fatalf("Until: %v", err)
		}
		// Pre-flush: nothing should have hit the bus.
		if got := len(bus.calls); got != 0 {
			t.Fatalf("bus calls before commit = %d, want 0", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}

	want := []string{"Dispatch:sync", "DispatchNow:now", "DispatchAsync:async", "DispatchAfter:after:7", "Until:until"}
	if len(bus.calls) != len(want) {
		t.Fatalf("bus calls = %v, want %v", bus.calls, want)
	}
	for i, c := range bus.calls {
		if c != want[i] {
			t.Fatalf("bus.calls[%d] = %q, want %q", i, c, want[i])
		}
	}
}

// recordingDispatcher captures the kind + delay used to dispatch each
// event. Methods unrelated to dispatch are unimplemented because the
// flush callback exercises only the dispatch surface.
type recordingDispatcher struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingDispatcher) Dispatch(e interface{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := e.(*txDomainEvent)
	r.calls = append(r.calls, "Dispatch:"+d.Tag)
	return nil
}
func (r *recordingDispatcher) DispatchNow(e interface{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := e.(*txDomainEvent)
	r.calls = append(r.calls, "DispatchNow:"+d.Tag)
	return nil
}
func (r *recordingDispatcher) DispatchAsync(e interface{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := e.(*txDomainEvent)
	r.calls = append(r.calls, "DispatchAsync:"+d.Tag)
	return nil
}
func (r *recordingDispatcher) DispatchAfter(e interface{}, delay time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := e.(*txDomainEvent)
	r.calls = append(r.calls, "DispatchAfter:"+d.Tag+":"+strconv.FormatInt(int64(delay), 10))
	return nil
}
func (r *recordingDispatcher) Until(e interface{}) (interface{}, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := e.(*txDomainEvent)
	r.calls = append(r.calls, "Until:"+d.Tag)
	return nil, nil
}
func (r *recordingDispatcher) Listen(_ interface{}, _ events.Listener) int { return 0 }
func (r *recordingDispatcher) Off(_ int) bool                              { return false }
func (r *recordingDispatcher) Subscribe(_ events.Subscriber)               {}
func (r *recordingDispatcher) Flush(_ string)                              {}
func (r *recordingDispatcher) Forget(_ string)                             {}
func (r *recordingDispatcher) HasListeners(_ interface{}) bool             { return false }
func (r *recordingDispatcher) GetListeners(_ interface{}) []events.Listener {
	return nil
}

// TestBuffer_NoPrepareIsSafeNoOp verifies Transaction works without
// PrepareBuffer (buffered events are unreachable from user code so nothing
// is recorded, but Transaction itself succeeds).
func TestBuffer_NoPrepareIsSafeNoOp(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	var fired []any
	m.SetEventDispatcher(func(e any) error {
		fired = append(fired, e)
		return nil
	})

	ctx := context.Background() // no PrepareBuffer
	err := m.Transaction(ctx, func(tx *sql.Tx) error {
		// Buffer(ctx) returns a standalone buffer; dispatching to it
		// is a no-op as far as the orm is concerned.
		_ = events.Buffer(ctx).Dispatch(&txDomainEvent{Tag: "lost"})
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	for _, e := range fired {
		if _, ok := e.(*txDomainEvent); ok {
			t.Fatalf("domain event reached dispatcher without PrepareBuffer: %#v", e)
		}
	}
}
