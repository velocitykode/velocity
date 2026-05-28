package cache_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/cache/cachetest"
	"github.com/velocitykode/velocity/cache/drivers"
)

// TestMemoryStore_Contract runs the cachetest.Store spec against the
// in-process memory store. The MemoryStore implements [cache.Store].
func TestMemoryStore_Contract(t *testing.T) {
	cachetest.RunStoreContractTests(t, func(t *testing.T) cache.Store {
		s := drivers.NewMemoryStore("contract")
		t.Cleanup(func() { _ = s.Shutdown(t.Context()) })
		return s
	})
}

// TestMemoryStore_LockerContract runs the locker spec against the memory
// store, which implements [drivers.Locker].
func TestMemoryStore_LockerContract(t *testing.T) {
	cachetest.RunLockerContractTests(t, func(t *testing.T) drivers.Locker {
		s := drivers.NewMemoryStore("contract-lock")
		t.Cleanup(func() { _ = s.Shutdown(t.Context()) })
		return s
	})
}

// TestFileStore_Contract runs the cachetest.Store spec against the
// filesystem-backed store rooted at t.TempDir.
func TestFileStore_Contract(t *testing.T) {
	cachetest.RunStoreContractTests(t, func(t *testing.T) cache.Store {
		s, err := drivers.NewFileStore("contract", t.TempDir())
		if err != nil {
			t.Fatalf("NewFileStore: %v", err)
		}
		t.Cleanup(func() { _ = s.Shutdown(t.Context()) })
		return s
	})
}

// TestFileStore_LockerContract runs the locker spec against FileStore.
// On platforms without flock(2) (Windows), FileStore.Lock returns nil and
// the contract cannot be exercised; we self-skip via a probe.
func TestFileStore_LockerContract(t *testing.T) {
	// Probe once before the factory loop: if Lock returns nil here, the
	// platform does not support file locks and every t.Run would fail.
	probe := mustFileStore(t, t.TempDir())
	if l := probe.Lock("probe", probeTTL); l == nil {
		_ = probe.Shutdown(context.Background())
		t.Skip("FileStore.Lock not supported on this platform (flock unavailable)")
	}
	_ = probe.Shutdown(context.Background())

	cachetest.RunLockerContractTests(t, func(t *testing.T) drivers.Locker {
		s := mustFileStore(t, t.TempDir())
		t.Cleanup(func() { _ = s.Shutdown(t.Context()) })
		return s
	})
}

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

// TestRedisStore_LockerContract runs the locker spec against RedisStore.
func TestRedisStore_LockerContract(t *testing.T) {
	cachetest.RunLockerContractTests(t, func(t *testing.T) drivers.Locker {
		s, _ := newMiniredisStore(t)
		return s
	})
}

// probeTTL is the duration used by capability probes; long enough to
// outlive the probe call, short enough that a forgotten probe times out
// before the run completes.
const probeTTL = 60 * time.Second

func mustFileStore(t *testing.T, root string) *drivers.FileStore {
	t.Helper()
	s, err := drivers.NewFileStore("contract", root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return s
}

// newMiniredisStore spins a per-test miniredis instance and wires a
// RedisStore against it.
func newMiniredisStore(t *testing.T) (*drivers.RedisStore, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	store, err := drivers.NewRedisStore(context.Background(), "contract", mr.Host(), mr.Server().Addr().Port, "", 0, false)
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	return store, mr
}
