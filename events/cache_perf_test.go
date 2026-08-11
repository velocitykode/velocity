package events

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// cacheCountListener records how many events it handled.
type cacheCountListener struct{ count int32 }

func (l *cacheCountListener) Handle(_ context.Context, _ interface{}) error {
	atomic.AddInt32(&l.count, 1)
	return nil
}

func (l *cacheCountListener) Async() bool { return false }

// cacheReflectEvent is a plain struct WITHOUT a Name() method, so its name
// resolves via reflection (CacheReflectEvent -> cache.reflect.event),
// exercising the type-name cache rather than the Event interface branch.
type CacheReflectEvent struct{ ID int }

// --- E1: resolved-listener cache correctness ---

// TestResolvedCache_RegisterAfterDispatch is the key invalidation guarantee:
// a listener registered AFTER an event has already been dispatched (which
// populates the resolved-listener cache) must still fire on the next dispatch.
func TestResolvedCache_RegisterAfterDispatch(t *testing.T) {
	d := NewDispatcher()
	ctx := context.Background()

	first := &cacheCountListener{}
	d.Listen("user.created", first)

	// Prime the cache for this event name.
	if err := d.Dispatch(ctx, "user.created"); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if got := atomic.LoadInt32(&first.count); got != 1 {
		t.Fatalf("first listener count = %d, want 1", got)
	}

	// Register a second listener AFTER the cache was primed.
	second := &cacheCountListener{}
	d.Listen("user.created", second)

	if err := d.Dispatch(ctx, "user.created"); err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	if got := atomic.LoadInt32(&second.count); got != 1 {
		t.Fatalf("listener registered after dispatch did not fire (count = %d, want 1)", got)
	}
	if got := atomic.LoadInt32(&first.count); got != 2 {
		t.Fatalf("first listener count = %d, want 2", got)
	}
}

// TestResolvedCache_WildcardRegisterAfterDispatch verifies a wildcard
// registered after a dispatch invalidates the cache and matches.
func TestResolvedCache_WildcardRegisterAfterDispatch(t *testing.T) {
	d := NewDispatcher()
	ctx := context.Background()

	exact := &cacheCountListener{}
	d.Listen("order.shipped", exact)
	_ = d.Dispatch(ctx, "order.shipped") // prime cache

	wild := &cacheCountListener{}
	d.Listen("order.*", wild)

	_ = d.Dispatch(ctx, "order.shipped")
	if got := atomic.LoadInt32(&wild.count); got != 1 {
		t.Fatalf("wildcard registered after dispatch did not fire (count = %d, want 1)", got)
	}
}

// TestResolvedCache_OffInvalidates verifies removing a listener with Off
// invalidates the cache so it no longer fires.
func TestResolvedCache_OffInvalidates(t *testing.T) {
	d := NewDispatcher()
	ctx := context.Background()

	l := &cacheCountListener{}
	id := d.Listen("a.b", l)
	_ = d.Dispatch(ctx, "a.b") // prime
	if got := atomic.LoadInt32(&l.count); got != 1 {
		t.Fatalf("count = %d, want 1", got)
	}

	if !d.Off(id) {
		t.Fatal("Off returned false")
	}
	_ = d.Dispatch(ctx, "a.b")
	if got := atomic.LoadInt32(&l.count); got != 1 {
		t.Fatalf("listener fired after Off (count = %d, want 1)", got)
	}
}

// TestResolvedCache_FlushInvalidates verifies Flush invalidates the cache.
func TestResolvedCache_FlushInvalidates(t *testing.T) {
	d := NewDispatcher()
	ctx := context.Background()

	l := &cacheCountListener{}
	d.Listen("c.d", l)
	_ = d.Dispatch(ctx, "c.d") // prime
	d.Flush("c.d")

	_ = d.Dispatch(ctx, "c.d")
	if got := atomic.LoadInt32(&l.count); got != 1 {
		t.Fatalf("listener fired after Flush (count = %d, want 1)", got)
	}
}

// TestResolvedCache_ConcurrentDispatchAndRegister stresses the cache under
// concurrent dispatch + registration to catch races (run with -race).
func TestResolvedCache_ConcurrentDispatchAndRegister(t *testing.T) {
	d := NewDispatcher()
	ctx := context.Background()
	d.Listen("evt", &cacheCountListener{})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = d.Dispatch(ctx, "evt")
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				id := d.Listen("evt", &cacheCountListener{})
				d.Off(id)
			}
		}()
	}
	wg.Wait()
}

// TestPriorityDispatcher_CacheNotCorrupted ensures the priority sort (which
// mutates its slice in place) clones the cached slice rather than reordering
// the shared cache entry.
func TestPriorityDispatcher_CacheNotCorrupted(t *testing.T) {
	d := NewPriorityDispatcher()
	a := &cacheCountListener{}
	b := &cacheCountListener{}
	d.Listen("p.evt", a)
	d.Listen("p.evt", b)

	first := d.getListenersForEvent("p.evt")
	second := d.getListenersForEvent("p.evt")
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("len mismatch: %d, %d", len(first), len(second))
	}
	// Both calls must yield the same ordering; an in-place sort over a shared
	// cache slice could otherwise diverge or race.
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("priority order not stable across calls at %d", i)
		}
	}
}

// --- E2: type-name cache correctness ---

func TestResolveEventName_CachedConsistent(t *testing.T) {
	// First call populates the type cache, second hits it; both must agree.
	// CacheReflectEvent has no Name() method, so this drives the reflect path.
	want := "cache.reflect.event"
	if got := resolveEventName(CacheReflectEvent{}); got != want {
		t.Fatalf("resolveEventName = %q, want %q", got, want)
	}
	if got := resolveEventName(CacheReflectEvent{}); got != want {
		t.Fatalf("cached resolveEventName = %q, want %q", got, want)
	}
	// Pointer type caches under a distinct key, same resolved name.
	if got := resolveEventName(&CacheReflectEvent{}); got != want {
		t.Fatalf("pointer resolveEventName = %q, want %q", got, want)
	}
}

// --- Benchmarks ---

// BenchmarkDispatch_ExactMatch exercises the cached exact-match path; allocs/op
// should be ~0 once the resolved-listener cache is primed.
func BenchmarkDispatch_ExactMatch(b *testing.B) {
	d := NewDispatcher()
	ctx := context.Background()
	d.Listen("user.created", &cacheCountListener{})
	// Add an unrelated wildcard so the pre-cache code path would have scanned
	// d.wildcards twice; the cache must avoid that.
	d.Listen("order.*", &cacheCountListener{})

	// Pre-box the event so the benchmark measures the cached dispatch path,
	// not a per-iteration string->interface conversion at the call site.
	var ev interface{} = "user.created"
	_ = d.Dispatch(ctx, ev) // prime cache

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = d.Dispatch(ctx, ev)
	}
}

// BenchmarkResolveEventName exercises the type-name cache; allocs/op should be
// ~0 on the cached path.
func BenchmarkResolveEventName(b *testing.B) {
	// Pre-box so the benchmark isolates the cached reflect path, not the
	// struct->interface conversion at the call site.
	var evt interface{} = CacheReflectEvent{ID: 1}
	_ = resolveEventName(evt) // prime cache

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = resolveEventName(evt)
	}
}
