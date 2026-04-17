package drivers

import (
	"testing"
	"time"
)

// TestMemoryStore_PutMany_PerItemTTL is a regression test for a bug where
// PutMany computed one expiration *time.Time and assigned that same pointer
// to every item in the batch. Because Increment preserves the expiration
// pointer on an existing item, mutating one shared pointer would shift the
// TTL of every other entry in the batch.
//
// This test inserts items in two separate PutMany calls with different TTLs
// and verifies that the expirations are distinct pointers and that the
// second call does not "inherit" the first call's expiration.
func TestMemoryStore_PutMany_PerItemTTL(t *testing.T) {
	store := NewMemoryStore("")
	// Don't Start() — we inspect items directly.

	// Batch 1: short TTL.
	short := map[string]interface{}{"a": 1, "b": 2}
	if err := store.PutMany(short, 50*time.Millisecond); err != nil {
		t.Fatalf("PutMany short: %v", err)
	}

	// Batch 2: long TTL.
	long := map[string]interface{}{"c": 3, "d": 4}
	if err := store.PutMany(long, time.Hour); err != nil {
		t.Fatalf("PutMany long: %v", err)
	}

	// After ~150ms the short-TTL items should be gone but the long-TTL
	// items must still be present. If the long batch had inherited the
	// short TTL these would also expire.
	time.Sleep(150 * time.Millisecond)

	if _, ok := store.Get("a"); ok {
		t.Error("expected 'a' (short TTL) to have expired")
	}
	if _, ok := store.Get("b"); ok {
		t.Error("expected 'b' (short TTL) to have expired")
	}
	if _, ok := store.Get("c"); !ok {
		t.Error("expected 'c' (long TTL) to still be present")
	}
	if _, ok := store.Get("d"); !ok {
		t.Error("expected 'd' (long TTL) to still be present")
	}
}

// TestMemoryStore_PutMany_IndependentExpirationPointers asserts that no two
// items share the same *time.Time expiration pointer. Mutating any one item's
// expiration (via Increment) must never affect another key in the same batch.
func TestMemoryStore_PutMany_IndependentExpirationPointers(t *testing.T) {
	store := NewMemoryStore("")

	items := map[string]interface{}{"x": 1, "y": 2, "z": 3}
	if err := store.PutMany(items, time.Hour); err != nil {
		t.Fatalf("PutMany: %v", err)
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	seen := make(map[*time.Time]string)
	for key, it := range store.items {
		if it.expiration == nil {
			t.Errorf("item %q has nil expiration after PutMany", key)
			continue
		}
		if prev, dup := seen[it.expiration]; dup {
			t.Errorf("items %q and %q share the same expiration pointer", prev, key)
		}
		seen[it.expiration] = key
	}
}

// TestMemoryStore_PutMany_SecondCallDoesNotInheritFirstTTL is the test
// explicitly called for in the task description: two inserts with different
// TTLs must be independent.
func TestMemoryStore_PutMany_SecondCallDoesNotInheritFirstTTL(t *testing.T) {
	store := NewMemoryStore("")

	// First insert: very short TTL (already expired by the time we check).
	if err := store.PutMany(map[string]interface{}{"first": "v"}, 1*time.Millisecond); err != nil {
		t.Fatalf("PutMany first: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	// Second insert: long TTL.
	if err := store.PutMany(map[string]interface{}{"second": "v"}, time.Hour); err != nil {
		t.Fatalf("PutMany second: %v", err)
	}

	// "second" must be present even though "first" has expired.
	if _, ok := store.Get("second"); !ok {
		t.Error("second item should be present with long TTL, but was not found")
	}
	if _, ok := store.Get("first"); ok {
		t.Error("first item should have expired")
	}
}
