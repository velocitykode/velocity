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
