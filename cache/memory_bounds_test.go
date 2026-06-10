package cache_test

import (
	"testing"

	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/cache/drivers"
)

// TestMemoryStoreConfigMaxEntriesPlumbed proves StoreConfig.MaxEntries
// reaches the memory driver through the registry factory, including the
// 0-means-default and negative-means-unlimited sentinels.
func TestMemoryStoreConfigMaxEntriesPlumbed(t *testing.T) {
	tests := []struct {
		name       string
		maxEntries int
		want       int
	}{
		{"zero applies default cap", 0, drivers.DefaultMaxEntries},
		{"positive cap honoured", 42, 42},
		{"negative means unlimited", -1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := cache.NewManager(&cache.Config{
				Default: "default",
				Stores: map[string]cache.StoreConfig{
					"default": {Driver: cache.DriverMemory, MaxEntries: tt.maxEntries},
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
			if got := mem.MaxEntries(); got != tt.want {
				t.Fatalf("MaxEntries() = %d, want %d", got, tt.want)
			}
		})
	}
}
