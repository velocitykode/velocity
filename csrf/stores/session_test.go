package stores

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestNewSessionStore(t *testing.T) {
	store := NewSessionStore()
	if store == nil {
		t.Fatal("NewSessionStore returned nil")
	}

	if store.tokens == nil {
		t.Error("Store tokens map not initialized")
	}
}

func TestSessionStore_SetAndGet(t *testing.T) {
	store := NewSessionStore()
	sessionID := "session123"
	token := "token456"

	// Set token
	err := store.Set(sessionID, token)
	if err != nil {
		t.Fatalf("Failed to set token: %v", err)
	}

	// Get token
	retrieved, err := store.Get(sessionID)
	if err != nil {
		t.Fatalf("Failed to get token: %v", err)
	}

	if retrieved != token {
		t.Errorf("Expected token %s, got %s", token, retrieved)
	}
}

func TestSessionStore_GetNonExistent(t *testing.T) {
	store := NewSessionStore()

	// Try to get non-existent token
	_, err := store.Get("nonexistent")
	if err != ErrTokenNotFound {
		t.Errorf("Expected ErrTokenNotFound, got %v", err)
	}
}

func TestSessionStore_Delete(t *testing.T) {
	store := NewSessionStore()
	sessionID := "session123"
	token := "token456"

	// Set token
	store.Set(sessionID, token)

	// Verify it exists
	if !store.Exists(sessionID) {
		t.Error("Token should exist after Set")
	}

	// Delete token
	err := store.Delete(sessionID)
	if err != nil {
		t.Fatalf("Failed to delete token: %v", err)
	}

	// Verify it's deleted
	if store.Exists(sessionID) {
		t.Error("Token should not exist after Delete")
	}

	// Try to get deleted token
	_, err = store.Get(sessionID)
	if err != ErrTokenNotFound {
		t.Error("Expected ErrTokenNotFound after deletion")
	}
}

func TestSessionStore_Exists(t *testing.T) {
	store := NewSessionStore()
	sessionID := "session123"

	// Should not exist initially
	if store.Exists(sessionID) {
		t.Error("Token should not exist initially")
	}

	// Set token
	store.Set(sessionID, "token456")

	// Should exist now
	if !store.Exists(sessionID) {
		t.Error("Token should exist after Set")
	}
}

func TestSessionStore_Expiration(t *testing.T) {
	store := NewSessionStore()
	sessionID := "session123"
	token := "token456"

	// Set token
	store.Set(sessionID, token)

	// Manually expire the token
	store.mu.Lock()
	store.tokens[sessionID].expiresAt = time.Now().Add(-1 * time.Hour)
	store.mu.Unlock()

	// Try to get expired token
	_, err := store.Get(sessionID)
	if err != ErrTokenNotFound {
		t.Error("Expected ErrTokenNotFound for expired token")
	}

	// Exists should return false for expired token
	if store.Exists(sessionID) {
		t.Error("Expired token should not exist")
	}
}

func TestSessionStore_ConcurrentAccess(t *testing.T) {
	store := NewSessionStore()
	var wg sync.WaitGroup
	iterations := 100

	// Concurrent Set operations
	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sessionID := fmt.Sprintf("session%d", n)
			token := fmt.Sprintf("token%d", n)
			store.Set(sessionID, token)
		}(i)
	}

	wg.Wait()

	// Concurrent Get operations
	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sessionID := fmt.Sprintf("session%d", n)
			token, err := store.Get(sessionID)
			if err != nil {
				t.Errorf("Failed to get token for session %d: %v", n, err)
				return
			}
			expected := fmt.Sprintf("token%d", n)
			if token != expected {
				t.Errorf("Expected token %s, got %s", expected, token)
			}
		}(i)
	}

	wg.Wait()
}

func TestSessionStore_ConcurrentSetAndDelete(t *testing.T) {
	store := NewSessionStore()
	var wg sync.WaitGroup
	iterations := 50

	// Concurrent Set and Delete operations
	for i := 0; i < iterations; i++ {
		wg.Add(2)

		// Setter
		go func(n int) {
			defer wg.Done()
			sessionID := fmt.Sprintf("session%d", n)
			token := fmt.Sprintf("token%d", n)
			store.Set(sessionID, token)
		}(i)

		// Deleter
		go func(n int) {
			defer wg.Done()
			sessionID := fmt.Sprintf("session%d", n)
			store.Delete(sessionID)
		}(i)
	}

	wg.Wait()

	// No panics means thread safety is working
}

func TestSessionStore_UpdateToken(t *testing.T) {
	store := NewSessionStore()
	sessionID := "session123"
	token1 := "token1"
	token2 := "token2"

	// Set initial token
	store.Set(sessionID, token1)

	// Get initial token
	retrieved, _ := store.Get(sessionID)
	if retrieved != token1 {
		t.Errorf("Expected token %s, got %s", token1, retrieved)
	}

	// Update token
	store.Set(sessionID, token2)

	// Get updated token
	retrieved, _ = store.Get(sessionID)
	if retrieved != token2 {
		t.Errorf("Expected token %s, got %s", token2, retrieved)
	}
}

// TestSessionStore_ConsumeIfMatch_Atomic exercises the cross-process
// single-use primitive added for M-01. ConsumeIfMatch MUST behave as one
// compare-and-delete: only one of N concurrent callers with the right
// expected value may observe consumed=true, the rest see consumed=false.
// Without this property, two replicas behind a shared store could each
// accept the same single-use token simultaneously.
func TestSessionStore_ConsumeIfMatch_Atomic(t *testing.T) {
	store := NewSessionStore()
	const id = "shared-session"
	const token = "single-use-token"

	if err := store.Set(id, token); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const goroutines = 64
	var (
		wg        sync.WaitGroup
		consumed  int64
		mismatch  int64
		mu        sync.Mutex
		errsFound []error
	)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			ok, err := store.ConsumeIfMatch(id, token)
			if err != nil {
				mu.Lock()
				errsFound = append(errsFound, err)
				mu.Unlock()
				return
			}
			if ok {
				mu.Lock()
				consumed++
				mu.Unlock()
				return
			}
			mu.Lock()
			mismatch++
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(errsFound) > 0 {
		t.Fatalf("ConsumeIfMatch returned errors: %v", errsFound)
	}
	if consumed != 1 {
		t.Errorf("expected exactly 1 successful consume across %d goroutines, got %d", goroutines, consumed)
	}
	if mismatch != goroutines-1 {
		t.Errorf("expected %d misses, got %d", goroutines-1, mismatch)
	}

	// Entry must be gone now.
	if store.Exists(id) {
		t.Error("entry must be deleted after successful ConsumeIfMatch")
	}
}

// TestSessionStore_ConsumeIfMatch_Mismatch verifies wrong-value callers do
// NOT delete the entry; the legitimate holder of the right token must
// still be able to consume it afterwards.
func TestSessionStore_ConsumeIfMatch_Mismatch(t *testing.T) {
	store := NewSessionStore()
	const id = "session"
	const token = "real-token"
	if err := store.Set(id, token); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ok, err := store.ConsumeIfMatch(id, "wrong-token")
	if err != nil {
		t.Fatalf("ConsumeIfMatch err: %v", err)
	}
	if ok {
		t.Fatal("ConsumeIfMatch returned true for non-matching token")
	}
	if !store.Exists(id) {
		t.Fatal("entry must NOT be deleted on mismatch")
	}

	// Right token still consumes.
	ok, err = store.ConsumeIfMatch(id, token)
	if err != nil {
		t.Fatalf("ConsumeIfMatch (correct) err: %v", err)
	}
	if !ok {
		t.Fatal("ConsumeIfMatch (correct token) returned false")
	}
}

// TestSessionStore_ConsumeIfMatch_Missing verifies missing/expired entries
// return consumed=false without error.
func TestSessionStore_ConsumeIfMatch_Missing(t *testing.T) {
	store := NewSessionStore()
	ok, err := store.ConsumeIfMatch("ghost", "anything")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ok {
		t.Fatal("ConsumeIfMatch on missing entry returned true")
	}

	// Expired entry behaves like missing.
	const id = "expired"
	if err := store.Set(id, "tok"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	store.mu.Lock()
	store.tokens[id].expiresAt = time.Now().Add(-time.Hour)
	store.mu.Unlock()

	ok, err = store.ConsumeIfMatch(id, "tok")
	if err != nil {
		t.Fatalf("unexpected err on expired: %v", err)
	}
	if ok {
		t.Fatal("ConsumeIfMatch on expired entry returned true")
	}
}

func BenchmarkSessionStore_Set(b *testing.B) {
	store := NewSessionStore()
	sessionID := "session123"
	token := "token456"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.Set(sessionID, token)
	}
}

func BenchmarkSessionStore_Get(b *testing.B) {
	store := NewSessionStore()
	sessionID := "session123"
	token := "token456"
	store.Set(sessionID, token)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.Get(sessionID)
	}
}

func BenchmarkSessionStore_ConcurrentOperations(b *testing.B) {
	store := NewSessionStore()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			sessionID := fmt.Sprintf("session%d", i%100)
			token := fmt.Sprintf("token%d", i)
			store.Set(sessionID, token)
			store.Get(sessionID)
			i++
		}
	})
}
