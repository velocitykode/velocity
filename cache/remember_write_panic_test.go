package cache

import (
	"context"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// panicMarshaler panics when the store serializes it.
type panicMarshaler struct{}

func (panicMarshaler) MarshalJSON() ([]byte, error) { panic(hostile.PanicValue) }

// A Remember whose write panics (a value's MarshalJSON, run while the
// store persists the value) releases the populate lock before the panic
// reaches the caller, as a panicking callback does, so the next caller
// elects itself at once instead of waiting out the lock's TTL.
func TestRemember_PanickingWriteReleasesThePopulateLock(t *testing.T) {
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
			m := NewManager(&Config{Default: "file", Stores: map[string]StoreConfig{
				"file": {Driver: "file", Path: t.TempDir()},
			}})
			t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
			const key = "remember-write-panic"

			if p := hostile.Within(t, hostile.Deadline, func() {
				_, _ = v.remember(m, key, func() (interface{}, error) { return panicMarshaler{}, nil })
			}); p != hostile.PanicValue {
				t.Fatalf("Remember panic = %v, want the write's panic", p)
			}

			store, err := m.DefaultStore()
			if err != nil {
				t.Fatalf("DefaultStore: %v", err)
			}
			if _, held := store.Get(rememberLockKey(key)); held {
				t.Error("the populate lock is still held after the write panicked")
			}
			calls := 0
			got, err := v.remember(m, key, func() (interface{}, error) {
				calls++
				return "fresh", nil
			})
			if err != nil || got != "fresh" || calls != 1 {
				t.Errorf("next Remember = %v, %v with %d calls; want fresh, nil, 1", got, err, calls)
			}
		})
	}
}
