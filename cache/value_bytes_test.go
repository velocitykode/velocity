package cache_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/cache/drivers"
)

// TestStoreConfigMaxValueBytesPlumbed proves StoreConfig.MaxValueBytes
// reaches both the memory and file drivers through the registry factories,
// and that the resulting stores enforce it via cache.ErrValueTooLarge.
func TestStoreConfigMaxValueBytesPlumbed(t *testing.T) {
	big := strings.Repeat("x", 1024)

	t.Run("memory", func(t *testing.T) {
		m := cache.NewManager(&cache.Config{
			Default: "default",
			Stores: map[string]cache.StoreConfig{
				"default": {Driver: cache.DriverMemory, MaxValueBytes: 64},
			},
		})
		store, err := m.Store("default")
		if err != nil {
			t.Fatalf("Store: %v", err)
		}
		mem, ok := store.(*drivers.MemoryStore)
		if !ok {
			t.Fatalf("store is %T, want *drivers.MemoryStore", store)
		}
		if got := mem.MaxValueBytes(); got != 64 {
			t.Fatalf("MaxValueBytes() = %d, want 64", got)
		}
		if err := store.Put("big", big, time.Hour); !errors.Is(err, cache.ErrValueTooLarge) {
			t.Errorf("Put oversize: err = %v, want cache.ErrValueTooLarge", err)
		}
	})

	t.Run("file", func(t *testing.T) {
		m := cache.NewManager(&cache.Config{
			Default: "default",
			Stores: map[string]cache.StoreConfig{
				"default": {Driver: cache.DriverFile, Path: t.TempDir(), MaxValueBytes: 64},
			},
		})
		store, err := m.Store("default")
		if err != nil {
			t.Fatalf("Store: %v", err)
		}
		fs, ok := store.(*drivers.FileStore)
		if !ok {
			t.Fatalf("store is %T, want *drivers.FileStore", store)
		}
		if got := fs.MaxValueBytes(); got != 64 {
			t.Fatalf("MaxValueBytes() = %d, want 64", got)
		}
		if err := store.Put("big", big, time.Hour); !errors.Is(err, cache.ErrValueTooLarge) {
			t.Errorf("Put oversize: err = %v, want cache.ErrValueTooLarge", err)
		}
	})

	t.Run("zero means unlimited", func(t *testing.T) {
		m := cache.NewManager(&cache.Config{
			Default: "default",
			Stores: map[string]cache.StoreConfig{
				"default": {Driver: cache.DriverMemory},
			},
		})
		store, err := m.Store("default")
		if err != nil {
			t.Fatalf("Store: %v", err)
		}
		if err := store.Put("big", big, time.Hour); err != nil {
			t.Errorf("Put on default (uncapped) store: %v", err)
		}
	})
}
