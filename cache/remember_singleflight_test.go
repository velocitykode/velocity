package cache_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/cache"
)

// TestRemember_SingleFlight asserts that when 100 goroutines concurrently
// call Remember for the same key on a cold cache, the populate callback
// executes a small constant number of times (target: exactly once on the
// happy path). Previously the unguarded Get-then-callback-then-Put path
// allowed every concurrent caller to run the callback, producing 100
// upstream loads on a cache miss -- the textbook thundering herd.
func TestRemember_SingleFlight(t *testing.T) {
	m := cache.NewManager(&cache.Config{
		Default: "memory",
		Prefix:  "",
		Stores: map[string]cache.StoreConfig{
			"memory": {Driver: cache.DriverMemory},
		},
	})
	defer func() { _ = m.Shutdown(context.Background()) }()

	const goroutines = 100
	var callbackCount int32
	start := make(chan struct{})
	results := make(chan interface{}, goroutines)
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			val, err := m.Remember("herd-key", time.Hour, func() interface{} {
				atomic.AddInt32(&callbackCount, 1)
				// Simulate a slow populate so concurrent callers
				// pile up on the Add gate rather than racing past it.
				time.Sleep(50 * time.Millisecond)
				return "computed"
			})
			results <- val
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("Remember error: %v", err)
		}
	}
	for v := range results {
		if v != "computed" {
			t.Errorf("Remember returned %v; want computed", v)
		}
	}

	// The exact bound depends on race timing on a loaded CI box, but the
	// previous bug ran the callback ~goroutines times. A constant-bounded
	// count (typically 1, occasionally a few if a goroutine starts late
	// and the sentinel TTL elapses) proves the single-flight gate works.
	got := atomic.LoadInt32(&callbackCount)
	if got == 0 {
		t.Fatal("callback never ran; Remember must populate on miss")
	}
	if got > 5 {
		t.Fatalf("callback ran %d times under contention; single-flight gate failed (must be <= 5)", got)
	}
	t.Logf("callback ran %d times across %d concurrent callers", got, goroutines)
}

// TestRemember_SingleFlight_CallbackErrorDoesNotPin verifies that a
// callback returning an error releases the populater slot so the next
// caller can re-elect and try again, rather than every subsequent
// caller polling the sentinel until it expires.
func TestRemember_SingleFlight_CallbackErrorDoesNotPin(t *testing.T) {
	m := cache.NewManager(&cache.Config{
		Default: "memory",
		Stores: map[string]cache.StoreConfig{
			"memory": {Driver: cache.DriverMemory},
		},
	})
	defer func() { _ = m.Shutdown(context.Background()) }()

	// First call fails -- this must Forget the sentinel.
	_, err := m.RememberE("err-key", time.Hour, func() (interface{}, error) {
		return nil, context.DeadlineExceeded
	})
	if err == nil {
		t.Fatal("RememberE must surface callback error")
	}

	// Second call should be able to populate fresh, not poll the sentinel.
	started := time.Now()
	val, err := m.RememberE("err-key", time.Hour, func() (interface{}, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("second RememberE: %v", err)
	}
	if val != "ok" {
		t.Errorf("second RememberE = %v; want ok", val)
	}
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Errorf("second RememberE took %v; callback-failure sentinel was not cleaned up", elapsed)
	}
}

// TestRemember_SingleFlight_Redis covers the Redis driver path via
// miniredis so the SETNX-based Add primitive is exercised end-to-end
// through Manager.Remember.
func TestRemember_SingleFlight_Redis(t *testing.T) {
	m, mr := newRedisTestManager(t)
	defer mr.Close()
	defer func() { _ = m.Shutdown(context.Background()) }()

	const goroutines = 50
	var callbackCount int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			_, err := m.Remember("redis-herd", time.Hour, func() interface{} {
				atomic.AddInt32(&callbackCount, 1)
				time.Sleep(30 * time.Millisecond)
				return "computed"
			})
			if err != nil {
				t.Errorf("Remember: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	got := atomic.LoadInt32(&callbackCount)
	if got == 0 {
		t.Fatal("callback never ran")
	}
	if got > 5 {
		t.Fatalf("callback ran %d times under contention; want <= 5", got)
	}
}
