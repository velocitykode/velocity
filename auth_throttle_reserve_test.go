package velocity

import (
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
// that still admits no more than the cap.
func TestCacheLoginThrottler_Reserve_WindowReset(t *testing.T) {
	store, err := newMemoryCacheManager().DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore: %v", err)
	}
	const cap = 3
	th := newCacheLoginThrottler(store, cap, 20, 50, 50*time.Millisecond)
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	const key = auth.ThrottleKeyPairPrefix + "victim"
	for i := 0; i < cap; i++ {
		_, _ = th.Reserve(r, key)
	}
	if within, _ := th.Reserve(r, key); within {
		t.Fatal("reservation past cap allowed before the window expired")
	}
	time.Sleep(80 * time.Millisecond)

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
