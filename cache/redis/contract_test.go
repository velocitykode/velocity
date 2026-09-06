package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/cache/cachetest"
	"github.com/velocitykode/velocity/cache/drivers"
	"github.com/velocitykode/velocity/cache/redis"
)

// TestRedisStore_Contract runs the cachetest.Store spec against the Redis
// store backed by miniredis. Uses the clock-aware factory so the TTL
// invariant can FastForward miniredis past the entry's expiry.
func TestRedisStore_Contract(t *testing.T) {
	// Share a single miniredis instance across the sub-tests via a
	// closure-captured pointer so Advance can call FastForward on it.
	// Each sub-test still gets a fresh prefix-namespaced store from
	// New (the same miniredis serves all sub-tests but each operates
	// under its own prefix, which is how the FileStore factory works
	// today too).
	var mrPtr *miniredis.Miniredis
	cachetest.RunStoreContractTestsWithClock(t, cachetest.StoreFactoryWithClock{
		New: func(t *testing.T) cache.Store {
			s, mr := newMiniredisStore(t)
			mrPtr = mr
			return s
		},
		Advance: func(d time.Duration) {
			if mrPtr != nil {
				mrPtr.FastForward(d)
			}
		},
	})
}

// TestRedisStore_ReplacerContract runs the CacheReplacer spec against the
// SET XX implementation.
func TestRedisStore_ReplacerContract(t *testing.T) {
	var mrPtr *miniredis.Miniredis
	cachetest.RunReplacerContractTests(t, func(t *testing.T) cachetest.ReplacerStore {
		s, mr := newMiniredisStore(t)
		mrPtr = mr
		return s
	}, func(d time.Duration) {
		if mrPtr != nil {
			mrPtr.FastForward(d)
		}
	})
}

// TestRedisStore_SetStoreContract runs the CacheSetStore spec against the
// SADD / SREM / SMEMBERS implementation.
func TestRedisStore_SetStoreContract(t *testing.T) {
	var mrPtr *miniredis.Miniredis
	cachetest.RunSetStoreContractTests(t, func(t *testing.T) cachetest.SetStore {
		s, mr := newMiniredisStore(t)
		mrPtr = mr
		return s
	}, func(d time.Duration) {
		if mrPtr != nil {
			mrPtr.FastForward(d)
		}
	})
}

// TestRedisStore_LockerContract runs the locker spec against RedisStore.
func TestRedisStore_LockerContract(t *testing.T) {
	cachetest.RunLockerContractTests(t, func(t *testing.T) drivers.Locker {
		s, _ := newMiniredisStore(t)
		return s
	})
}

// newMiniredisStore spins a per-test miniredis instance and wires a
// RedisStore against it.
func newMiniredisStore(t *testing.T) (*redis.RedisStore, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	store, err := redis.NewRedisStore(context.Background(), "contract", mr.Host(), mr.Server().Addr().Port, "", 0, false)
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	return store, mr
}
