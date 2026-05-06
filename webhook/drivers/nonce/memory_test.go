package nonce

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNonceStore_Memory_BasicCheckAndMark(t *testing.T) {
	t.Parallel()

	m := NewMemory(0) // no sweep
	defer func() { _ = m.Close(context.Background()) }()

	ctx := context.Background()
	seen, err := m.CheckAndMark(ctx, "x", time.Minute)
	if err != nil || seen {
		t.Fatalf("first call: expected !seen, nil err; got seen=%v err=%v", seen, err)
	}
	seen, err = m.CheckAndMark(ctx, "x", time.Minute)
	if err != nil || !seen {
		t.Fatalf("second call: expected seen, nil err; got seen=%v err=%v", seen, err)
	}
	if m.Len() != 1 {
		t.Fatalf("expected Len=1, got %d", m.Len())
	}
}

func TestNonceStore_Memory_TTLExpires(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	m := &Memory{
		items:     make(map[string]time.Time),
		stopCh:    make(chan struct{}),
		stoppedCh: make(chan struct{}),
		now:       func() time.Time { return now },
	}
	close(m.stoppedCh) // no sweep
	defer func() { _ = m.Close(context.Background()) }()

	ctx := context.Background()
	seen, err := m.CheckAndMark(ctx, "n", 50*time.Millisecond)
	if err != nil || seen {
		t.Fatalf("first CheckAndMark: seen=%v err=%v", seen, err)
	}
	// Within TTL, second call sees it.
	if seen, _ := m.CheckAndMark(ctx, "n", 50*time.Millisecond); !seen {
		t.Fatalf("expected seen within TTL")
	}
	// Move clock past TTL: third call should see expired entry as fresh.
	now = now.Add(time.Second)
	if seen, _ := m.CheckAndMark(ctx, "n", 50*time.Millisecond); seen {
		t.Fatalf("expected not seen after TTL")
	}
}

func TestNonceStore_Memory_SweepCollects(t *testing.T) {
	t.Parallel()

	m := NewMemory(5 * time.Millisecond)
	defer func() { _ = m.Close(context.Background()) }()

	ctx := context.Background()
	for i := 0; i < 50; i++ {
		if _, err := m.CheckAndMark(ctx, strconv.Itoa(i), 10*time.Millisecond); err != nil {
			t.Fatalf("CheckAndMark: %v", err)
		}
	}
	if m.Len() == 0 {
		t.Fatalf("expected entries to be present immediately after CheckAndMark")
	}

	// Wait for sweeper to drain expired entries.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m.Len() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("sweep did not collect expired entries (Len=%d)", m.Len())
}

func TestNonceStore_Memory_Concurrent(t *testing.T) {
	t.Parallel()

	m := NewMemory(20 * time.Millisecond)
	defer func() { _ = m.Close(context.Background()) }()

	const (
		writers = 16
		perW    = 200
	)
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		w := w
		go func() {
			defer wg.Done()
			ctx := context.Background()
			for i := 0; i < perW; i++ {
				key := strconv.Itoa(w*1_000_000 + i)
				_, _ = m.CheckAndMark(ctx, key, 100*time.Millisecond)
				_, _ = m.CheckAndMark(ctx, key, 100*time.Millisecond)
			}
		}()
	}
	wg.Wait()
}

// TestNonceStore_Memory_CheckAndMark_Atomic asserts that even when many
// goroutines race CheckAndMark on the same nonce, exactly one observes
// alreadySeen=false. This is the property Verifier relies on to prevent
// TOCTOU replays.
func TestNonceStore_Memory_CheckAndMark_Atomic(t *testing.T) {
	t.Parallel()

	m := NewMemory(0)
	defer func() { _ = m.Close(context.Background()) }()

	const goroutines = 256
	var (
		firstWrites atomic.Int64
		wg          sync.WaitGroup
		start       = make(chan struct{})
	)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			seen, err := m.CheckAndMark(context.Background(), "race-nonce", time.Minute)
			if err != nil {
				t.Errorf("CheckAndMark: %v", err)
				return
			}
			if !seen {
				firstWrites.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := firstWrites.Load(); got != 1 {
		t.Fatalf("expected exactly 1 first-writer, got %d", got)
	}
	if m.Len() != 1 {
		t.Fatalf("expected Len=1 after atomic race, got %d", m.Len())
	}
}

func TestNonceStore_Memory_CloseIdempotent(t *testing.T) {
	t.Parallel()

	m := NewMemory(time.Hour) // sweep would basically never run
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("Close (second): %v", err)
	}
}

func TestNonceStore_Memory_CloseRespectsContext(t *testing.T) {
	t.Parallel()

	// Start with a sweep that never fires, then never call stopCh; instead
	// pass an already-cancelled context to Close. We need a non-stopped
	// store, so build it manually and skip the close-of-stopCh.
	m := &Memory{
		items:     make(map[string]time.Time),
		stopCh:    make(chan struct{}),
		stoppedCh: make(chan struct{}),
	}
	go m.sweep(time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// stopOnce is fresh, so Close will close stopCh and then wait on
	// stoppedCh; the sweep goroutine will drain quickly via stopCh, but
	// in the worst case the cancelled ctx returns first.
	err := m.Close(ctx)
	if err != nil && err != context.Canceled {
		t.Fatalf("expected nil or context.Canceled, got %v", err)
	}
	// Drain the goroutine so -race / goleak stays happy.
	<-m.stoppedCh
}

// TestNonceStore_Memory_TickRecoverIsolated asserts that a panic inside
// tickWithRecover is observed via SetOnPanic and does not propagate.
func TestNonceStore_Memory_TickRecoverIsolated(t *testing.T) {
	t.Parallel()

	m := NewMemory(0)
	defer func() { _ = m.Close(context.Background()) }()

	var saw atomic.Int64
	m.SetOnPanic(func(any) { saw.Add(1) })

	// Inject a panicking now() so collect() (called from tickWithRecover)
	// triggers a panic. tickWithRecover must absorb it and call onPanic.
	m.now = func() time.Time { panic("boom") }
	m.tickWithRecover()

	if saw.Load() != 1 {
		t.Fatalf("expected onPanic to fire exactly once, got %d", saw.Load())
	}
}

// TestNonceStore_Memory_SweepLoopSurvivesPanic is the regression test for
// the original bug: a panic from collect() must not terminate the sweep
// goroutine. We arm a now() that panics on the first invocation only,
// then assert the sweeper still expires entries on subsequent ticks.
func TestNonceStore_Memory_SweepLoopSurvivesPanic(t *testing.T) {
	t.Parallel()

	wallStart := time.Now()
	var (
		mu       sync.Mutex
		panicked bool
	)
	nowFn := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		if !panicked {
			panicked = true
			panic("sweep panic on first tick")
		}
		// After the panic, advance the clock far past any TTL we'll set.
		return wallStart.Add(time.Hour)
	}

	m := &Memory{
		items:     make(map[string]time.Time),
		stopCh:    make(chan struct{}),
		stoppedCh: make(chan struct{}),
		now:       nowFn,
	}
	var sawPanic atomic.Int64
	m.SetOnPanic(func(any) { sawPanic.Add(1) })

	// Seed an entry directly (do NOT route through CheckAndMark to avoid
	// nowFn() panicking before the sweeper runs).
	m.mu.Lock()
	m.items["k"] = wallStart.Add(time.Millisecond)
	m.mu.Unlock()

	go m.sweep(2 * time.Millisecond)
	defer func() { _ = m.Close(context.Background()) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sawPanic.Load() >= 1 && m.Len() == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("sweep did not survive panic and continue collecting (sawPanic=%d, Len=%d)", sawPanic.Load(), m.Len())
}

func TestNonceStore_Memory_CheckAndMarkZeroTTL(t *testing.T) {
	t.Parallel()

	frozen := time.Unix(1000, 0)
	m := &Memory{
		items:     make(map[string]time.Time),
		stopCh:    make(chan struct{}),
		stoppedCh: make(chan struct{}),
		now:       func() time.Time { return frozen },
	}
	close(m.stoppedCh)
	defer func() { _ = m.Close(context.Background()) }()

	ctx := context.Background()
	if seen, _ := m.CheckAndMark(ctx, "n", 0); seen {
		t.Fatalf("first call with ttl=0 should report unseen")
	}
	// Advance the clock; the entry has expired (exp == initial frozen) so
	// the next call should also report unseen and replace it.
	frozen = frozen.Add(time.Second)
	if seen, _ := m.CheckAndMark(ctx, "n", 0); seen {
		t.Fatalf("expected unseen after TTL elapsed")
	}
}

// seen() is a private test helper; this exercise keeps it covered without
// promising it as part of the public API.
func TestNonceStore_Memory_SeenHelper(t *testing.T) {
	t.Parallel()

	m := NewMemory(0)
	defer func() { _ = m.Close(context.Background()) }()

	if m.seen("absent") {
		t.Fatalf("expected absent nonce to report false")
	}
	if _, err := m.CheckAndMark(context.Background(), "present", time.Minute); err != nil {
		t.Fatalf("CheckAndMark: %v", err)
	}
	if !m.seen("present") {
		t.Fatalf("expected present nonce to report true")
	}
}
