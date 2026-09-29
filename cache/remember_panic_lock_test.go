package cache

import (
	"context"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// A Remember callback is user code run while the caller holds the key's
// populate lock. One that panics releases the lock before the panic
// reaches the caller, so the next caller elects itself at once instead of
// every caller waiting out the lock's TTL; the panic still reaches the
// caller, and nothing is written to the key.
func TestRemember_PanickingCallbackReleasesThePopulateLock(t *testing.T) {
	variants := []struct {
		name     string
		remember func(m *Manager, key string, cb func() (interface{}, error)) (interface{}, error)
	}{
		{"RememberE", func(m *Manager, key string, cb func() (interface{}, error)) (interface{}, error) {
			return m.RememberE(key, time.Minute, cb)
		}},
		{"RememberForeverE", func(m *Manager, key string, cb func() (interface{}, error)) (interface{}, error) {
			return m.RememberForeverE(key, cb)
		}},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			m := newTestManager(nil)
			t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
			code := hostile.New(t, hostile.Panic, nil)
			const key = "remember-panic"

			if p := hostile.Within(t, hostile.Deadline, func() {
				_, _ = v.remember(m, key, func() (interface{}, error) {
					code.Run()
					return "unreachable", nil
				})
			}); p != hostile.PanicValue {
				t.Fatalf("Remember panic = %v, want the callback's panic", p)
			}

			store, err := m.DefaultStore()
			if err != nil {
				t.Fatalf("DefaultStore: %v", err)
			}
			if _, held := store.Get(rememberLockKey(key)); held {
				t.Error("the populate lock is still held after the callback panicked")
			}
			if _, found := store.Get(key); found {
				t.Error("a value was written for the key after the callback panicked")
			}
			calls := 0
			got, err := v.remember(m, key, func() (interface{}, error) {
				calls++
				return "fresh", nil
			})
			if err != nil || got != "fresh" || calls != 1 {
				t.Errorf("next Remember = %v, %v with %d calls; want fresh, nil, 1", got, err, calls)
			}
			if _, held := store.Get(rememberLockKey(key)); held {
				t.Error("the populate lock is still held after the next Remember")
			}
		})
	}
}
