package auth

import (
	"reflect"
	"sync"
	"testing"
)

// TestBaseSession_FlushFlash_EmptyReturnsNil verifies that FlushFlash on a
// fresh session returns nil (not an empty map) and does not mark the session
// as modified, so callers can rely on JSON omitempty and unaffected dirty
// tracking.
func TestBaseSession_FlushFlash_EmptyReturnsNil(t *testing.T) {
	session := NewSession("test-id")

	got := session.FlushFlash()
	if got != nil {
		t.Errorf("FlushFlash() on empty bag = %v, want nil", got)
	}

	if session.IsModified() {
		t.Error("FlushFlash() on empty bag should not mark session modified")
	}
}

// TestBaseSession_FlushFlash_DrainsAndMarksModified populates the flash bag,
// flushes it, and confirms the bag is cleared, IsModified is true, and a
// second flush returns nil.
func TestBaseSession_FlushFlash_DrainsAndMarksModified(t *testing.T) {
	session := NewSession("test-id")
	session.Flash("message", "Hello")
	session.Flash("error", "Boom")

	// Reset modified so we observe only FlushFlash's effect on it.
	session.mu.Lock()
	session.modified = false
	session.mu.Unlock()

	got := session.FlushFlash()
	want := map[string]interface{}{
		"message": "Hello",
		"error":   "Boom",
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("FlushFlash() = %v, want %v", got, want)
	}

	if !session.IsModified() {
		t.Error("FlushFlash() with values should mark session modified")
	}

	// Internal flash bag must be cleared.
	if remaining := session.GetFlashData(); len(remaining) != 0 {
		t.Errorf("flash bag after FlushFlash = %v, want empty", remaining)
	}

	// Second flush must return nil (bag has been drained).
	if again := session.FlushFlash(); again != nil {
		t.Errorf("second FlushFlash() = %v, want nil", again)
	}
}

// TestBaseSession_FlushFlash_ReturnedMapIsDetached verifies that mutating the
// returned map does not affect a freshly-populated flash bag on the session.
// FlushFlash hands callers the drained map and installs a fresh empty map on
// the session in the same call, so the two are independent thereafter.
func TestBaseSession_FlushFlash_ReturnedMapIsDetached(t *testing.T) {
	session := NewSession("test-id")
	session.Flash("a", 1)
	session.Flash("b", 2)

	flushed := session.FlushFlash()
	if flushed == nil {
		t.Fatal("FlushFlash() = nil, want populated map")
	}

	// Mutate the returned map; the session's NEW (empty) flash bag must not
	// see these mutations.
	flushed["c"] = 3
	delete(flushed, "a")

	// Add a fresh entry to the session and confirm it is independent of the
	// returned map.
	session.Flash("d", 4)

	current := session.GetFlashData()
	if _, ok := current["c"]; ok {
		t.Error("post-flush mutation of returned map leaked into session bag")
	}
	if _, ok := current["a"]; ok {
		t.Error("session bag should not retain pre-flush keys")
	}
	if current["d"] != 4 {
		t.Errorf("session bag missing fresh Flash entry: %v", current)
	}
}

// TestBaseSession_FlushFlash_Concurrent runs Flash and FlushFlash concurrently
// across many goroutines under -race to confirm the mutex protects both
// readers and writers. Failures here surface as Go race detector reports.
func TestBaseSession_FlushFlash_Concurrent(t *testing.T) {
	session := NewSession("test-id")

	const goroutines = 80
	const iterations = 50

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Half the goroutines write flash entries.
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				key := string(rune('a' + (idx+j)%26))
				session.Flash(key, idx*1000+j)
			}
		}(i)
	}

	// The other half drain the bag.
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = session.FlushFlash()
			}
		}()
	}

	wg.Wait()

	// Final drain so the test leaves no live entries; assertion is loose
	// because writers and drainers interleave non-deterministically.
	_ = session.FlushFlash()
}
