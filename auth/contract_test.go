package auth_test

import (
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/authtest"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/contract"
)

// TestMemorySessionStore_Contract runs the authtest spec against the
// in-process ServerSessionStore.
func TestMemorySessionStore_Contract(t *testing.T) {
	authtest.RunServerSessionStoreContractTests(t, func(t *testing.T) auth.ServerSessionStore {
		return session.NewMemoryStore()
	})
}

// TestCacheSessionStore_Contract runs the same authtest spec against the
// cache-backed ServerSessionStore. The memory cache driver stands in for the
// Redis one a deployment configures: both reach the store through
// cache.CacheManager, so the driver under test is identical.
func TestCacheSessionStore_Contract(t *testing.T) {
	authtest.RunServerSessionStoreContractTests(t, func(t *testing.T) auth.ServerSessionStore {
		cm := cache.NewManager(&cache.Config{
			Default: "default",
			Stores: map[string]cache.StoreConfig{
				"default": {Driver: "memory"},
			},
		})
		if cm == nil {
			t.Fatal("cache.NewManager returned nil")
		}
		store, err := session.NewCacheStore(cm)
		if err != nil {
			t.Fatalf("NewCacheStore: %v", err)
		}
		return store
	})
}

// TestNoopLoginThrottler_Contract runs the authtest spec against the
// default no-op throttler.
func TestNoopLoginThrottler_Contract(t *testing.T) {
	authtest.RunLoginThrottlerContractTests(t, func(t *testing.T) contract.LoginThrottler {
		return auth.NoopLoginThrottler{}
	})
}
