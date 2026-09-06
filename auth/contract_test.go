package auth_test

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/authtest"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/cache/drivers"
	cacheredis "github.com/velocitykode/velocity/cache/redis"
	"github.com/velocitykode/velocity/contract"
)

// TestMemorySessionStore_Contract runs the authtest spec against the
// in-process ServerSessionStore.
func TestMemorySessionStore_Contract(t *testing.T) {
	authtest.RunServerSessionStoreContractTests(t, func(t *testing.T) auth.ServerSessionStore {
		return session.NewMemoryStore()
	})
}

// TestCacheSessionStore_MemoryContract runs the authtest spec against the
// cache-backed ServerSessionStore over the memory cache driver.
func TestCacheSessionStore_MemoryContract(t *testing.T) {
	authtest.RunServerSessionStoreContractTests(t, func(t *testing.T) auth.ServerSessionStore {
		backend := drivers.NewMemoryStore("sessions")
		t.Cleanup(func() { _ = backend.Shutdown(context.Background()) })
		store, err := session.NewCacheStore(backend)
		if err != nil {
			t.Fatalf("NewCacheStore: %v", err)
		}
		return store
	})
}

// TestCacheSessionStore_RedisContract runs the authtest spec against the
// cache-backed ServerSessionStore over the redis cache driver (miniredis).
func TestCacheSessionStore_RedisContract(t *testing.T) {
	authtest.RunServerSessionStoreContractTests(t, func(t *testing.T) auth.ServerSessionStore {
		mr := miniredis.RunT(t)
		backend, err := cacheredis.NewRedisStore(context.Background(), "sessions", mr.Host(), mr.Server().Addr().Port, "", 0, false)
		if err != nil {
			t.Fatalf("NewRedisStore: %v", err)
		}
		t.Cleanup(func() { _ = backend.Shutdown(context.Background()) })
		store, err := session.NewCacheStore(backend)
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
