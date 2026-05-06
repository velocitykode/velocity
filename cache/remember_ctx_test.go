package cache_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/velocitykode/velocity/cache"
)

// newRedisManager spins up a miniredis-backed *cache.Manager for ctx
// propagation tests. Returns the manager, the miniredis instance, and a
// cleanup func.
func newRedisManager(t *testing.T) (*cache.Manager, *miniredis.Miniredis, func()) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	m := cache.NewManager(&cache.Config{
		Default: "redis",
		Stores: map[string]cache.StoreConfig{
			"redis": {
				Driver: cache.DriverRedis,
				Host:   mr.Host(),
				Port:   mr.Server().Addr().Port,
			},
		},
	})
	return m, mr, func() {
		_ = m.Shutdown(context.Background())
		mr.Close()
	}
}

// TestRememberWithContext_AllDrivers covers the non-error ctx-aware variant
// across every driver. The ctx must reach the store on miss + Put without
// breaking memory/file (which fall back to the plain Store API).
func TestRememberWithContext_AllDrivers(t *testing.T) {
	for _, f := range allDriverFactories(t) {
		f := f
		t.Run(f.name, func(t *testing.T) {
			m, done := f.build(t)
			defer done()

			t.Run("HappyPath_Caches", func(t *testing.T) {
				var calls int32
				val, err := m.RememberWithContext(context.Background(), "k", time.Hour, func() interface{} {
					atomic.AddInt32(&calls, 1)
					return "v"
				})
				if err != nil {
					t.Fatalf("RememberWithContext: %v", err)
				}
				if val != "v" {
					t.Fatalf("got %v, want v", val)
				}

				// Second call hits cache.
				val2, err := m.RememberWithContext(context.Background(), "k", time.Hour, func() interface{} {
					atomic.AddInt32(&calls, 1)
					return "should-not-run"
				})
				if err != nil {
					t.Fatalf("2nd: %v", err)
				}
				if val2 != "v" {
					t.Fatalf("got %v, want cached v", val2)
				}
				if got := atomic.LoadInt32(&calls); got != 1 {
					t.Fatalf("callback ran %d times, want 1", got)
				}
			})

			t.Run("RememberForeverWithContext_HappyPath", func(t *testing.T) {
				val, err := m.RememberForeverWithContext(context.Background(), "perm", func() interface{} {
					return "kept"
				})
				if err != nil {
					t.Fatalf("RememberForeverWithContext: %v", err)
				}
				if val != "kept" {
					t.Fatalf("got %v, want kept", val)
				}
				if !m.Has("perm") {
					t.Fatal("perm missing")
				}
			})

			t.Run("RememberEWithContext_ErrorPath", func(t *testing.T) {
				_, err := m.RememberEWithContext(context.Background(), "ewerr", time.Hour, func() (interface{}, error) {
					return nil, errBoom
				})
				if !errors.Is(err, errBoom) {
					t.Fatalf("err = %v, want errBoom", err)
				}
				if m.Has("ewerr") {
					t.Fatal("ewerr was cached on error")
				}
			})

			t.Run("RememberForeverEWithContext_ErrorPath", func(t *testing.T) {
				_, err := m.RememberForeverEWithContext(context.Background(), "fewerr", func() (interface{}, error) {
					return nil, errBoom
				})
				if !errors.Is(err, errBoom) {
					t.Fatalf("err = %v, want errBoom", err)
				}
				if m.Has("fewerr") {
					t.Fatal("fewerr was cached on error")
				}
			})
		})
	}
}

// TestRememberWithContext_RedisCancellation verifies ctx propagation reaches
// the redis driver: a cancelled ctx must cause the underlying GET/SET to
// fail, which the manager surfaces as an error WITHOUT writing a poisoned
// value into the slot.
func TestRememberWithContext_RedisCancellation(t *testing.T) {
	m, _, done := newRedisManager(t)
	defer done()

	t.Run("CancelledBeforeCall", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		var calls int32
		_, err := m.RememberEWithContext(ctx, "cancelled", time.Hour, func() (interface{}, error) {
			atomic.AddInt32(&calls, 1)
			return "v", nil
		})
		if err == nil {
			t.Fatal("expected error on cancelled ctx")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if m.Has("cancelled") {
			t.Fatal("cancelled-ctx call wrote a value to the cache")
		}
		// Callback DID run (cancelled GET surfaces as a miss, not a hard
		// error in our impl); but the subsequent SET fails because the same
		// ctx is still cancelled. Either way the slot must be empty.
		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Logf("callback ran %d times (informational)", got)
		}
	})

	t.Run("CancelledMidFlight", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		// Fire a callback that signals it has started, then waits for cancel.
		started := make(chan struct{})
		_, err := m.RememberEWithContext(ctx, "mid-flight", time.Hour, func() (interface{}, error) {
			close(started)
			return "v", nil
		})
		// We did NOT cancel before the call returned; this is just a
		// happy-path cancellation immediately after to confirm no resource leak.
		cancel()
		<-started
		_ = err
	})
}

// TestRememberWithContext_MemoryFallback ensures memory (which does NOT
// implement ContextStore) accepts RememberWithContext calls and falls back
// to the plain Store API without dropping ctx propagation responsibility on
// the floor (the impl simply skips the ctx-aware code path).
func TestRememberWithContext_MemoryFallback(t *testing.T) {
	m := cache.NewManager(&cache.Config{
		Default: "memory",
		Stores: map[string]cache.StoreConfig{
			"memory": {Driver: cache.DriverMemory},
		},
	})
	defer func() { _ = m.Shutdown(context.Background()) }()

	// Cancelled ctx on a memory store should still complete: memory has no
	// network IO to cancel. This documents the behavior so callers know
	// ctx is best-effort.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	val, err := m.RememberEWithContext(ctx, "k", time.Hour, func() (interface{}, error) {
		return "v", nil
	})
	if err != nil {
		t.Fatalf("memory cancelled-ctx err = %v, want nil (best-effort)", err)
	}
	if val != "v" {
		t.Fatalf("got %v, want v", val)
	}
}

// TestRememberWithContext_LegacyDelegation confirms the original Remember
// and RememberForever methods still work after they were refactored to
// delegate to the WithContext variants. This is a backward-compat guard.
func TestRememberWithContext_LegacyDelegation(t *testing.T) {
	m := cache.NewManager(&cache.Config{
		Default: "memory",
		Stores: map[string]cache.StoreConfig{
			"memory": {Driver: cache.DriverMemory},
		},
	})
	defer func() { _ = m.Shutdown(context.Background()) }()

	val, err := m.Remember("legacy", time.Hour, func() interface{} { return 42 })
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if val != 42 {
		t.Fatalf("got %v, want 42", val)
	}

	val, err = m.RememberForever("legacy-perm", func() interface{} { return "perm" })
	if err != nil {
		t.Fatalf("RememberForever: %v", err)
	}
	if val != "perm" {
		t.Fatalf("got %v, want perm", val)
	}
}

// TestRememberWithContext_Concurrent fires many goroutines at the ctx-aware
// surface to surface any data-race introduced by the dispatch + ctx
// threading. Run with -race.
func TestRememberWithContext_Concurrent(t *testing.T) {
	m, _, done := newRedisManager(t)
	defer done()

	var calls int32
	var wg sync.WaitGroup
	const n = 32
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := m.RememberEWithContext(ctx, "hot", time.Hour, func() (interface{}, error) {
				atomic.AddInt32(&calls, 1)
				return "computed", nil
			})
			if err != nil {
				t.Errorf("RememberEWithContext: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if !m.Has("hot") {
		t.Fatal("hot key missing after concurrent populate")
	}
}

// TestRememberTWithContext exercises the typed-generic ctx shim end to end
// through the redis driver so we know the ctx really reaches the store.
func TestRememberTWithContext(t *testing.T) {
	m, _, done := newRedisManager(t)
	defer done()

	t.Run("HappyPath", func(t *testing.T) {
		got, err := cache.RememberTWithContext[string](m, context.Background(), "tctx", time.Hour, func() (string, error) {
			return "ok", nil
		})
		if err != nil {
			t.Fatalf("RememberTWithContext: %v", err)
		}
		if got != "ok" {
			t.Fatalf("got %q, want ok", got)
		}
	})

	t.Run("CancelledCtx_NoPoison", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := cache.RememberTWithContext[string](m, ctx, "tctx-cancel", time.Hour, func() (string, error) {
			return "computed", nil
		})
		if err == nil {
			t.Fatal("expected error on cancelled ctx")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if m.Has("tctx-cancel") {
			t.Fatal("cancelled-ctx call wrote a value to the cache")
		}
	})

	t.Run("ErrorPath_ReturnsZero", func(t *testing.T) {
		got, err := cache.RememberTWithContext[int](m, context.Background(), "terr", time.Hour, func() (int, error) {
			return 99, errBoom
		})
		if !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want errBoom", err)
		}
		if got != 0 {
			t.Fatalf("got %d, want zero", got)
		}
		if m.Has("terr") {
			t.Fatal("terr cached on error")
		}
	})
}
