package velocity

import (
	"context"
	"reflect"
	"testing"

	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/cache/drivers"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/scheduler"
)

// lockLessStore is a cache store without the Lock primitive.
type lockLessStore struct{ cache.Store }

// The warnings installSchedulerLocker writes when it falls back to the
// in-process Locker go through the app's logger, which is user code: one
// that panics must not escape into bootstrap or leave the fallback
// uninstalled, and the warning reaches the fallback logger instead.
func TestInstallSchedulerLocker_PanickingLoggerIsContained(t *testing.T) {
	override := func(t *testing.T, store func(cfg cache.StoreConfig) cache.Store) *cache.Manager {
		t.Helper()
		name := "test-locker-logger-" + t.Name()
		prev := cache.Drivers().Override(name, func(_ context.Context, cfg cache.StoreConfig) (cache.Store, error) {
			return store(cfg), nil
		})
		t.Cleanup(func() { cache.Drivers().Override(name, prev) })
		cm := cache.NewManager(&cache.Config{
			Default: "default",
			Stores:  map[string]cache.StoreConfig{"default": {Driver: name}},
		})
		t.Cleanup(func() { _ = cm.Shutdown(context.Background()) })
		return cm
	}
	entries := []struct {
		name string
		cm   func(t *testing.T) *cache.Manager
		msg  string
	}{
		{"store unavailable", newDatabaseBackedCache,
			"velocity/scheduler: cache default store unavailable"},
		{"store without locks", func(t *testing.T) *cache.Manager {
			return override(t, func(cfg cache.StoreConfig) cache.Store {
				return lockLessStore{drivers.NewMemoryStore(cfg.Prefix)}
			})
		}, "velocity/scheduler: cache driver does not support distributed locks"},
		{"nil lock probe", func(t *testing.T) *cache.Manager {
			return override(t, func(cfg cache.StoreConfig) cache.Store {
				return &nilLockingStore{Store: drivers.NewMemoryStore(cfg.Prefix)}
			})
		}, "velocity/scheduler: cache driver returned nil Lock during capability probe"},
	}
	for _, e := range entries {
		t.Run(e.name, func(t *testing.T) {
			out := fallbacklogtest.Capture(t)
			code := hostile.New(t, hostile.Panic, nil)
			cm := e.cm(t)
			sched := scheduler.New()
			if p := hostile.Within(t, hostile.Deadline, func() {
				installSchedulerLocker(sched, cm, "redis", hostile.NewLogger(code, hostile.Warn))
			}); p != nil {
				t.Fatalf("installSchedulerLocker panicked: %v", p)
			}
			if got := reflect.TypeOf(sched.Locker()).String(); got != "*scheduler.InMemoryLocker" {
				t.Errorf("locker = %s, want the in-process fallback", got)
			}
			if n := out.Count("WARN", e.msg); n != 1 {
				t.Errorf("fallback WARN lines = %d, want 1:\n%s", n, out.String())
			}
		})
	}
}
