package cache_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/velocitykode/velocity/cache"
)

// errBoom is a sentinel error used by RememberE callbacks under test.
var errBoom = errors.New("upstream boom")

// managerFactory builds a fresh *cache.Manager backed by a specific driver.
// Returns the manager plus a cleanup func that the caller must defer.
type managerFactory struct {
	name  string
	build func(t *testing.T) (*cache.Manager, func())
}

// allDriverFactories enumerates every cache driver the manager supports so
// behavior tests can iterate the full driver matrix in one place.
func allDriverFactories(t *testing.T) []managerFactory {
	t.Helper()
	return []managerFactory{
		{
			name: "memory",
			build: func(t *testing.T) (*cache.Manager, func()) {
				m := cache.NewManager(&cache.Config{
					Default: "memory",
					Stores: map[string]cache.StoreConfig{
						"memory": {Driver: cache.DriverMemory},
					},
				})
				return m, func() { _ = m.Shutdown(context.Background()) }
			},
		},
		{
			name: "file",
			build: func(t *testing.T) (*cache.Manager, func()) {
				dir, err := os.MkdirTemp("", "velocity-cache-remember-*")
				if err != nil {
					t.Fatalf("mkdir tmp: %v", err)
				}
				m := cache.NewManager(&cache.Config{
					Default: "file",
					Stores: map[string]cache.StoreConfig{
						"file": {Driver: cache.DriverFile, Path: dir},
					},
				})
				return m, func() {
					_ = m.Shutdown(context.Background())
					_ = os.RemoveAll(dir)
				}
			},
		},
		{
			name: "redis",
			build: func(t *testing.T) (*cache.Manager, func()) {
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
				return m, func() {
					_ = m.Shutdown(context.Background())
					mr.Close()
				}
			},
		},
	}
}

// TestRememberE covers the behavior contract of the error-aware Remember and
// RememberForever helpers across every driver: cache hit short-circuits,
// happy path computes + caches, error path skips Put, second attempt after
// error re-runs the callback (slot was not poisoned).
func TestRememberE(t *testing.T) {
	for _, f := range allDriverFactories(t) {
		f := f
		t.Run(f.name, func(t *testing.T) {
			t.Run("HappyPath_CachesValue", func(t *testing.T) {
				m, done := f.build(t)
				defer done()

				var calls int32
				val, err := m.RememberE("k", time.Hour, func() (interface{}, error) {
					atomic.AddInt32(&calls, 1)
					return "computed", nil
				})
				if err != nil {
					t.Fatalf("RememberE: %v", err)
				}
				if val != "computed" {
					t.Fatalf("got %v, want computed", val)
				}

				// Second call returns the cached value, callback NOT re-run.
				val2, err := m.RememberE("k", time.Hour, func() (interface{}, error) {
					atomic.AddInt32(&calls, 1)
					return "should-not-run", nil
				})
				if err != nil {
					t.Fatalf("RememberE 2nd: %v", err)
				}
				if val2 != "computed" {
					t.Fatalf("got %v, want cached computed", val2)
				}
				if got := atomic.LoadInt32(&calls); got != 1 {
					t.Fatalf("callback ran %d times, want 1", got)
				}
			})

			t.Run("ErrorPath_DoesNotCache", func(t *testing.T) {
				m, done := f.build(t)
				defer done()

				_, err := m.RememberE("err-key", time.Hour, func() (interface{}, error) {
					return "ignored", errBoom
				})
				if !errors.Is(err, errBoom) {
					t.Fatalf("got err %v, want errBoom", err)
				}

				// The framework MUST NOT have written anything for the slot.
				if _, found := m.Get("err-key"); found {
					t.Fatal("err-key was poisoned: cache contains a value after callback error")
				}
				if m.Has("err-key") {
					t.Fatal("Has(err-key) reports true after callback error")
				}
			})

			t.Run("ErrorThenSuccess_RetriesUpstream", func(t *testing.T) {
				m, done := f.build(t)
				defer done()

				var calls int32
				cb := func() (interface{}, error) {
					n := atomic.AddInt32(&calls, 1)
					if n == 1 {
						return nil, errBoom
					}
					return "succeeded", nil
				}

				if _, err := m.RememberE("retry", time.Hour, cb); !errors.Is(err, errBoom) {
					t.Fatalf("first call err = %v, want errBoom", err)
				}
				val, err := m.RememberE("retry", time.Hour, cb)
				if err != nil {
					t.Fatalf("second call err = %v", err)
				}
				if val != "succeeded" {
					t.Fatalf("got %v, want succeeded", val)
				}
				if got := atomic.LoadInt32(&calls); got != 2 {
					t.Fatalf("callback ran %d times, want 2", got)
				}
			})

			t.Run("CacheHit_BypassesCallback", func(t *testing.T) {
				m, done := f.build(t)
				defer done()

				if err := m.Put("preloaded", "stored", time.Hour); err != nil {
					t.Fatalf("Put: %v", err)
				}
				var calls int32
				val, err := m.RememberE("preloaded", time.Hour, func() (interface{}, error) {
					atomic.AddInt32(&calls, 1)
					return "should-not-run", nil
				})
				if err != nil {
					t.Fatalf("RememberE: %v", err)
				}
				if val != "stored" {
					t.Fatalf("got %v, want stored", val)
				}
				if got := atomic.LoadInt32(&calls); got != 0 {
					t.Fatalf("callback ran %d times, want 0", got)
				}
			})

			t.Run("RememberForeverE_ErrorPath", func(t *testing.T) {
				m, done := f.build(t)
				defer done()

				if _, err := m.RememberForeverE("perm-err", func() (interface{}, error) {
					return nil, errBoom
				}); !errors.Is(err, errBoom) {
					t.Fatalf("err = %v, want errBoom", err)
				}
				if m.Has("perm-err") {
					t.Fatal("perm-err was cached despite callback error")
				}
			})

			t.Run("RememberForeverE_HappyPath", func(t *testing.T) {
				m, done := f.build(t)
				defer done()

				val, err := m.RememberForeverE("perm-ok", func() (interface{}, error) {
					return "kept", nil
				})
				if err != nil {
					t.Fatalf("RememberForeverE: %v", err)
				}
				if val != "kept" {
					t.Fatalf("got %v, want kept", val)
				}
				if !m.Has("perm-ok") {
					t.Fatal("perm-ok not cached")
				}
			})

			t.Run("EmptyKey_HappyPath", func(t *testing.T) {
				// Empty keys are not rejected by the Manager today; verify
				// that RememberE does not change that behavior on its own.
				m, done := f.build(t)
				defer done()

				val, err := m.RememberE("", time.Hour, func() (interface{}, error) {
					return "empty-ok", nil
				})
				if err != nil {
					t.Fatalf("RememberE empty: %v", err)
				}
				if val != "empty-ok" {
					t.Fatalf("got %v, want empty-ok", val)
				}
			})

			t.Run("NilValue_FromCallback", func(t *testing.T) {
				// Some upstream APIs return (nil, nil). Verify we cache the
				// nil and treat the next read as a hit (matches existing
				// non-error Remember semantics: a successful nil is still a
				// computed value).
				m, done := f.build(t)
				defer done()

				var calls int32
				val, err := m.RememberE("nil-ok", time.Hour, func() (interface{}, error) {
					atomic.AddInt32(&calls, 1)
					return nil, nil
				})
				if err != nil {
					t.Fatalf("RememberE: %v", err)
				}
				if val != nil {
					t.Fatalf("got %v, want nil", val)
				}
				// Memory and file stores cache the nil and report a hit; redis
				// JSON-marshals nil as `null`. Either way the callback must
				// not run a second time.
				_, _ = m.RememberE("nil-ok", time.Hour, func() (interface{}, error) {
					atomic.AddInt32(&calls, 1)
					return "should-not-run", nil
				})
				if got := atomic.LoadInt32(&calls); got != 1 {
					t.Fatalf("callback ran %d times, want 1 (nil result must be cached)", got)
				}
			})
		})
	}
}

// TestRememberE_BackwardCompat ensures the legacy Remember (no error return)
// surface keeps working unchanged, since adopters rely on it.
func TestRememberE_BackwardCompat(t *testing.T) {
	m, done := allDriverFactories(t)[0].build(t)
	defer done()

	var calls int32
	val, err := m.Remember("legacy", time.Hour, func() interface{} {
		atomic.AddInt32(&calls, 1)
		return "legacy-val"
	})
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if val != "legacy-val" {
		t.Fatalf("got %v, want legacy-val", val)
	}
	val2, _ := m.Remember("legacy", time.Hour, func() interface{} {
		atomic.AddInt32(&calls, 1)
		return "second"
	})
	if val2 != "legacy-val" {
		t.Fatalf("got %v, want cached legacy-val", val2)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("legacy Remember ran callback %d times, want 1", got)
	}
}

// TestRememberE_Concurrent verifies thread-safety of RememberE. Multiple
// goroutines compete to populate the same key. After the storm, the value
// must be present and the callback may have run multiple times (Manager
// does not coordinate compute, by design: that is the lock package's job),
// but no error must surface to any caller, and no race detector report
// must fire (`go test -race`).
func TestRememberE_Concurrent(t *testing.T) {
	for _, f := range allDriverFactories(t) {
		f := f
		t.Run(f.name, func(t *testing.T) {
			m, done := f.build(t)
			defer done()

			var calls int32
			var wg sync.WaitGroup
			const goroutines = 32
			start := make(chan struct{})

			for i := 0; i < goroutines; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					_, err := m.RememberE("hot", time.Hour, func() (interface{}, error) {
						atomic.AddInt32(&calls, 1)
						return fmt.Sprintf("v-%d", i), nil
					})
					if err != nil {
						t.Errorf("goroutine %d: %v", i, err)
					}
				}(i)
			}
			close(start)
			wg.Wait()

			if !m.Has("hot") {
				t.Fatal("hot key missing after concurrent populate")
			}
			if got := atomic.LoadInt32(&calls); got < 1 {
				t.Fatalf("callback ran %d times, want >=1", got)
			}
		})
	}
}

// TestRememberT verifies the typed-generic shim returns T directly without
// any cast at the call site. Covers happy path, error path, and the
// type-mismatch corruption signal.
func TestRememberT(t *testing.T) {
	type Region struct {
		Slug string
		ID   int
	}

	m, done := allDriverFactories(t)[0].build(t)
	defer done()

	t.Run("HappyPath_ReturnsTypedValue", func(t *testing.T) {
		want := Region{Slug: "us-east", ID: 1}
		got, err := cache.RememberT[Region](m, "region:us-east", time.Hour, func() (Region, error) {
			return want, nil
		})
		if err != nil {
			t.Fatalf("RememberT: %v", err)
		}
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("ErrorPath_ReturnsZero", func(t *testing.T) {
		got, err := cache.RememberT[Region](m, "region:err", time.Hour, func() (Region, error) {
			return Region{}, errBoom
		})
		if !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want errBoom", err)
		}
		if got != (Region{}) {
			t.Fatalf("got %+v, want zero", got)
		}
		if m.Has("region:err") {
			t.Fatal("region:err was cached on error")
		}
	})

	t.Run("Primitives_String", func(t *testing.T) {
		got, err := cache.RememberT[string](m, "str-key", time.Hour, func() (string, error) {
			return "hello", nil
		})
		if err != nil {
			t.Fatalf("RememberT[string]: %v", err)
		}
		if got != "hello" {
			t.Fatalf("got %q, want hello", got)
		}
	})

	t.Run("ZeroValueLegitimate", func(t *testing.T) {
		// Callback returns zero T legitimately. RememberT must surface that
		// without confusing it with a type-mismatch.
		got, err := cache.RememberT[int](m, "zero-int", time.Hour, func() (int, error) {
			return 0, nil
		})
		if err != nil {
			t.Fatalf("RememberT[int]: %v", err)
		}
		if got != 0 {
			t.Fatalf("got %d, want 0", got)
		}
	})

	t.Run("TypeMismatch_ReturnsError", func(t *testing.T) {
		// Pre-seed a wrong type at the key, then ask for Region. The shim
		// must not silently coerce; it returns zero T and an error so the
		// caller can detect cache-corruption / version-skew.
		if err := m.Put("typed-mismatch", "i-am-a-string", time.Hour); err != nil {
			t.Fatalf("Put: %v", err)
		}
		got, err := cache.RememberT[Region](m, "typed-mismatch", time.Hour, func() (Region, error) {
			return Region{Slug: "should-not-run"}, nil
		})
		if err == nil {
			t.Fatal("RememberT type-mismatch did not return an error")
		}
		if got != (Region{}) {
			t.Fatalf("got %+v on type mismatch, want zero", got)
		}
	})
}
