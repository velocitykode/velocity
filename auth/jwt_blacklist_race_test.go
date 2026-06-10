package auth

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func newBlacklistRaceManager(t *testing.T) *JWTManager {
	t.Helper()
	m, err := NewJWTManager(JWTConfig{
		Secret:           "test-secret-key-for-jwt-signing-minimum-length",
		Algorithm:        "HS256",
		TTL:              60,
		RefreshTTL:       1440,
		BlacklistEnabled: true,
		BlacklistStore:   NewInMemoryBlacklistStore(),
	})
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}
	return m
}

// SetBlacklistStore previously wrote j.blacklistStore with no
// synchronization while validation read it concurrently, a torn
// interface read under the race detector. Run with -race.
func TestJWTManager_SetBlacklistStore_ConcurrentWithValidation(t *testing.T) {
	m := newBlacklistRaceManager(t)

	token, err := m.GenerateToken(&jwtRefreshTestUser{id: "user123"})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writer: keep swapping the store (including the nil -> in-memory
	// revert path).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			m.SetBlacklistStore(NewInMemoryBlacklistStore())
			m.SetBlacklistStore(nil)
		}
		close(stop)
	}()

	// Readers: token validation consults the blacklist on every call;
	// RevokeToken and CleanupBlacklist hit the other two read sites.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; ; j++ {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := m.ValidateAccessToken(token); err != nil {
					t.Errorf("ValidateAccessToken: %v", err)
					return
				}
				m.RevokeToken(fmt.Sprintf("jti-%d-%d", n, j), time.Now().Add(time.Minute))
				m.CleanupBlacklist()
			}
		}(i)
	}

	wg.Wait()
}

// SetBlacklistStore(nil) must fall back to an in-process store, not
// leave a nil interface for the next RevokeToken to deref.
func TestJWTManager_SetBlacklistStore_NilRevertsToInMemory(t *testing.T) {
	m := newBlacklistRaceManager(t)
	m.SetBlacklistStore(nil)
	m.RevokeToken("some-jti", time.Now().Add(time.Minute))
	if !m.IsBlacklisted("some-jti") {
		t.Error("revoked JTI not blacklisted after nil-store revert")
	}
}
