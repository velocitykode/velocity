package velocity

import (
	"context"
	"github.com/velocitykode/velocity/contract"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
)

func TestCacheLoginThrottler_Reserve_CountsBeforeVerification(t *testing.T) {
	const cap = 5
	th := newTestDimensionedLoginThrottler(t, cap, 20, 50)
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	const key = auth.ThrottleKeyPairPrefix + "victim"

	for i := 1; i <= cap; i++ {
		if within, _ := th.Reserve(r, key); !within {
			t.Fatalf("reservation %d of %d denied", i, cap)
		}
	}
	if within, _ := th.Reserve(r, key); within {
		t.Fatalf("reservation %d allowed past cap %d", cap+1, cap)
	}
	if th.Allow(r, key) {
		t.Fatal("Allow after a full window = true, want false")
	}
	th.RecordSuccess(r, key)
	if within, _ := th.Reserve(r, key); !within {
		t.Fatal("Reserve after RecordSuccess = false, want true (window cleared)")
	}
}

// TestCacheLoginThrottler_Reserve_ConcurrentBelowCap is the reviewer's
// below-cap probe: with 19 of 20 attempts already counted, 64 concurrent
// reservations must yield exactly one within-cap admission.
func TestCacheLoginThrottler_Reserve_ConcurrentBelowCap(t *testing.T) {
	th := newTestDimensionedLoginThrottler(t, 5, 20, 50)
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	const key = auth.ThrottleKeyIdentifierPrefix + "victim"
	for i := 0; i < 19; i++ {
		th.RecordFailure(r, key)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	within := 0
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := th.Reserve(r, key); ok {
				mu.Lock()
				within++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if within != 1 {
		t.Fatalf("%d concurrent reservations admitted within cap, want exactly 1", within)
	}
}

// TestCacheLoginThrottler_Reserve_WindowReset covers the decay boundary:
// once the window expires, concurrent reservations start a fresh count
// that still admits no more than the cap. The window is long, and its end
// is the counter leaving the store, done here by forgetting it, so no
// wall-clock wait decides the result.
func TestCacheLoginThrottler_Reserve_WindowReset(t *testing.T) {
	store, err := newMemoryCacheManager().DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore: %v", err)
	}
	const cap = 3
	th := newCacheLoginThrottler(store, cap, 20, 50, time.Hour)
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	const key = auth.ThrottleKeyPairPrefix + "victim"
	for i := 0; i < cap; i++ {
		_, _ = th.Reserve(r, key)
	}
	if within, _ := th.Reserve(r, key); within {
		t.Fatal("reservation past cap allowed before the window expired")
	}
	// The window expires: its counter leaves the store.
	if _, ok := store.Get(th.cacheKey(key)); !ok {
		t.Fatal("the window's counter is not in the store")
	}
	if err := store.ForgetCtx(context.Background(), th.cacheKey(key)); err != nil {
		t.Fatalf("forget the window's counter: %v", err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	within := 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := th.Reserve(r, key); ok {
				mu.Lock()
				within++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if within != cap {
		t.Fatalf("%d reservations admitted after the window reset, want %d", within, cap)
	}
}

func TestCacheLoginThrottler_Reserve_NilSafe(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	var nilTh *cacheLoginThrottler
	if within, _ := nilTh.Reserve(r, "k"); !within {
		t.Fatal("nil throttler must reserve")
	}
	if within, _ := (&cacheLoginThrottler{}).Reserve(nil, "k"); !within {
		t.Fatal("storeless throttler must reserve")
	}
}

// expiringStore is a cache store whose next IncrementCtx finds the key
// expired: it forgets the key first, so the increment recreates it from
// nothing, with no expiration. It records the re-puts.
type expiringStore struct {
	contract.CacheStore
	expireNext bool
	puts       []time.Duration
}

func (s *expiringStore) IncrementCtx(ctx context.Context, key string, value int64) (int64, error) {
	if s.expireNext {
		s.expireNext = false
		_ = s.CacheStore.ForgetCtx(ctx, key)
	}
	return s.CacheStore.IncrementCtx(ctx, key, value)
}

func (s *expiringStore) PutCtx(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	s.puts = append(s.puts, ttl)
	return s.CacheStore.PutCtx(ctx, key, value, ttl)
}

// A window that expires between an attempt's add and its increment is
// recreated by the increment with no expiration; the attempt then re-puts
// its count of 1 under the decay TTL, so the bucket does not deny forever.
// The first attempt of a fresh window needs no re-put.
func TestCacheLoginThrottler_Reserve_ExpiryBetweenAddAndIncrement(t *testing.T) {
	base, err := newMemoryCacheManager().DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore: %v", err)
	}
	store := &expiringStore{CacheStore: base}
	th := newCacheLoginThrottler(store, 3, 20, 50, time.Hour)
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	const key = auth.ThrottleKeyPairPrefix + "victim"

	if within, _ := th.Reserve(r, key); !within {
		t.Fatal("first attempt refused")
	}
	if len(store.puts) != 0 {
		t.Fatalf("the first attempt re-put its count: %v", store.puts)
	}
	store.expireNext = true
	if within, _ := th.Reserve(r, key); !within {
		t.Fatal("attempt after the expiry refused")
	}
	if len(store.puts) != 1 || store.puts[0] != time.Hour {
		t.Fatalf("re-puts = %v, want one under the decay TTL", store.puts)
	}
	if v, ok := base.Get(th.cacheKey(key)); !ok || numericCacheValue(v) != 1 {
		t.Fatalf("count after the expiry = %v (present %v), want 1", v, ok)
	}
}
