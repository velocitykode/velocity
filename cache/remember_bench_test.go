package cache

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// BenchmarkRemember_Hit measures a Remember that finds the key.
func BenchmarkRemember_Hit(b *testing.B) {
	m := benchManager()
	b.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	_ = m.Put("k", "v", time.Hour)
	cb := func() (interface{}, error) { return "v", nil }
	b.ReportAllocs()
	for b.Loop() {
		_, _ = m.RememberE("k", time.Hour, cb)
	}
}

// BenchmarkRemember_Miss measures a Remember that populates the key: the
// populate lock taken, the callback run, the value written, the lock
// dropped.
func BenchmarkRemember_Miss(b *testing.B) {
	m := benchManager()
	b.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	cb := func() (interface{}, error) { return "v", nil }
	keys := make([]string, 1<<16)
	for i := range keys {
		keys[i] = "k" + strconv.Itoa(i)
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		key := keys[i%len(keys)]
		i++
		_, _ = m.RememberE(key, time.Hour, cb)
		_ = m.Forget(key)
	}
}

// benchManager is a memory-store manager with no event dispatcher.
func benchManager() *Manager {
	return NewManager(&Config{Default: "memory", Stores: map[string]StoreConfig{"memory": {Driver: DriverMemory}}})
}
