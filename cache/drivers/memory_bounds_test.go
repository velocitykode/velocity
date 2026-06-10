package drivers

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMemoryStoreMaxEntriesResolution(t *testing.T) {
	tests := []struct {
		name string
		opts []MemoryOption
		want int
	}{
		{"no option applies default", nil, DefaultMaxEntries},
		{"zero applies default", []MemoryOption{WithMaxEntries(0)}, DefaultMaxEntries},
		{"positive caps at n", []MemoryOption{WithMaxEntries(5)}, 5},
		{"negative means unlimited", []MemoryOption{WithMaxEntries(-1)}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore("", tt.opts...)
			if got := store.MaxEntries(); got != tt.want {
				t.Fatalf("MaxEntries() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMemoryStoreCapEnforced(t *testing.T) {
	store := NewMemoryStore("", WithMaxEntries(3))

	for i := 0; i < 4; i++ {
		if err := store.Put(fmt.Sprintf("key%d", i), i, time.Minute); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	store.mu.RLock()
	size := len(store.items)
	store.mu.RUnlock()
	if size != 3 {
		t.Fatalf("store holds %d entries, want 3", size)
	}

	// key0 was least recently used and must have been evicted. (The cap
	// is below evictionSampleSize, so eviction is exact LRU here.)
	if _, found := store.Get("key0"); found {
		t.Fatal("key0 should have been evicted")
	}
	for i := 1; i < 4; i++ {
		if _, found := store.Get(fmt.Sprintf("key%d", i)); !found {
			t.Fatalf("key%d should still be present", i)
		}
	}
}

func TestMemoryStoreLRURecency(t *testing.T) {
	store := NewMemoryStore("", WithMaxEntries(3))

	for i := 0; i < 3; i++ {
		if err := store.Put(fmt.Sprintf("key%d", i), i, time.Minute); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	// Touch key0 so key1 becomes the LRU victim.
	if _, found := store.Get("key0"); !found {
		t.Fatal("key0 should be present before eviction")
	}

	if err := store.Put("key3", 3, time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if _, found := store.Get("key0"); !found {
		t.Fatal("recently-used key0 should have survived eviction")
	}
	if _, found := store.Get("key1"); found {
		t.Fatal("least-recently-used key1 should have been evicted")
	}
}

func TestMemoryStoreForeverEntriesEvictable(t *testing.T) {
	store := NewMemoryStore("", WithMaxEntries(2))

	if err := store.Forever("eternal0", "v0"); err != nil {
		t.Fatalf("Forever: %v", err)
	}
	if err := store.Forever("eternal1", "v1"); err != nil {
		t.Fatalf("Forever: %v", err)
	}
	if err := store.Put("fresh", "v2", time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if _, found := store.Get("eternal0"); found {
		t.Fatal("LRU Forever entry should have been evicted at cap")
	}
	if _, found := store.Get("eternal1"); !found {
		t.Fatal("eternal1 should still be present")
	}
	if _, found := store.Get("fresh"); !found {
		t.Fatal("fresh should be present")
	}
}

// TestMemoryStoreEvictionPrefersExpired proves eviction removes an
// already-expired entry over the least-recently-used live one.
func TestMemoryStoreEvictionPrefersExpired(t *testing.T) {
	store := NewMemoryStore("", WithMaxEntries(2))

	// "live" is the LRU victim by recency; "dead" expires first.
	if err := store.Put("live", 1, time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Put("dead", 2, time.Millisecond); err != nil {
		t.Fatalf("Put: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	if err := store.Put("fresh", 3, time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if _, found := store.Get("live"); !found {
		t.Fatal("live entry evicted while an expired entry was available")
	}
	if _, found := store.Get("dead"); found {
		t.Fatal("expired entry should have been evicted")
	}
	if _, found := store.Get("fresh"); !found {
		t.Fatal("fresh should be present")
	}
}

func TestMemoryStoreUnlimitedSentinel(t *testing.T) {
	store := NewMemoryStore("", WithMaxEntries(-1))

	const n = 5000
	for i := 0; i < n; i++ {
		if err := store.Put(fmt.Sprintf("key%d", i), i, time.Minute); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	store.mu.RLock()
	size := len(store.items)
	store.mu.RUnlock()
	if size != n {
		t.Fatalf("unbounded store holds %d entries, want %d", size, n)
	}
}

func TestMemoryStoreReplaceDoesNotEvict(t *testing.T) {
	store := NewMemoryStore("", WithMaxEntries(2))

	if err := store.Put("a", 1, time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Put("b", 2, time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Overwrite at cap: must update in place, not evict.
	if err := store.Put("a", 10, time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}

	v, found := store.Get("a")
	if !found || v != 10 {
		t.Fatalf("a = %v (found=%v), want 10", v, found)
	}
	if _, found := store.Get("b"); !found {
		t.Fatal("b should not have been evicted by an overwrite")
	}
}

func TestMemoryStoreIncrementAtCap(t *testing.T) {
	store := NewMemoryStore("", WithMaxEntries(2))

	if _, err := store.Increment("counter", 1); err != nil {
		t.Fatalf("Increment: %v", err)
	}
	if err := store.Put("other", "x", time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Increment of an existing key at cap must not evict anything.
	v, err := store.Increment("counter", 1)
	if err != nil {
		t.Fatalf("Increment: %v", err)
	}
	if v != 2 {
		t.Fatalf("counter = %d, want 2", v)
	}
	if _, found := store.Get("other"); !found {
		t.Fatal("other should not have been evicted by incrementing an existing key")
	}

	// Increment of a NEW key at cap evicts the LRU entry ("counter" was
	// touched most recently above, so "other" goes).
	if _, found := store.Get("counter"); !found {
		t.Fatal("counter should be present")
	}
	if _, err := store.Increment("counter2", 5); err != nil {
		t.Fatalf("Increment: %v", err)
	}
	store.mu.RLock()
	size := len(store.items)
	store.mu.RUnlock()
	if size != 2 {
		t.Fatalf("store holds %d entries, want 2", size)
	}
	if _, found := store.Get("counter2"); !found {
		t.Fatal("counter2 should have been inserted")
	}
}

func TestMemoryStoreAddAtCapEvicts(t *testing.T) {
	store := NewMemoryStore("", WithMaxEntries(2))

	if err := store.Put("a", 1, time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Put("b", 2, time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Add on an existing live key still loses (atomicity preserved).
	ok, err := store.Add("a", 99, time.Minute)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if ok {
		t.Fatal("Add on existing key should return false")
	}

	// Add of a new key at cap evicts and inserts.
	ok, err = store.Add("c", 3, time.Minute)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !ok {
		t.Fatal("Add of new key should return true")
	}
	store.mu.RLock()
	size := len(store.items)
	store.mu.RUnlock()
	if size != 2 {
		t.Fatalf("store holds %d entries, want 2", size)
	}
}

func TestMemoryStoreForgetFlushMaintainBound(t *testing.T) {
	store := NewMemoryStore("", WithMaxEntries(3))

	for i := 0; i < 3; i++ {
		if err := store.Put(fmt.Sprintf("key%d", i), i, time.Minute); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := store.Forget("key1"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	store.mu.RLock()
	size := len(store.items)
	store.mu.RUnlock()
	if size != 0 {
		t.Fatalf("after Forget+Flush: map len %d, want 0", size)
	}

	// Store still usable and bounded after Flush.
	for i := 0; i < 4; i++ {
		if err := store.Put(fmt.Sprintf("post%d", i), i, time.Minute); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	store.mu.RLock()
	size = len(store.items)
	store.mu.RUnlock()
	if size != 3 {
		t.Fatalf("after refill: map len %d, want 3", size)
	}
}

// TestMemoryStoreBoundedConcurrent hammers a small bounded store from many
// goroutines across every mutating path. Run under -race (reads stamp
// recency atomically under RLock concurrently with sampled eviction under
// the write lock); also asserts the cap held.
func TestMemoryStoreBoundedConcurrent(t *testing.T) {
	const cap = 100
	store := NewMemoryStore("", WithMaxEntries(cap))

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				key := fmt.Sprintf("g%d-k%d", g, i%150)
				switch i % 6 {
				case 0:
					_ = store.Put(key, i, time.Minute)
				case 1:
					_, _ = store.Get(key)
				case 2:
					_, _ = store.Increment(key, 1)
				case 3:
					_, _ = store.Add(key, i, time.Minute)
				case 4:
					_ = store.Forever(key, i)
				case 5:
					_ = store.Forget(key)
				}
			}
		}(g)
	}
	wg.Wait()

	store.mu.RLock()
	size := len(store.items)
	store.mu.RUnlock()
	if size > cap {
		t.Fatalf("store holds %d entries, cap is %d", size, cap)
	}
}
