package cache

import (
	"context"
	"sync"
	"testing"
	"time"
)

type testCtxKey string

const testRequestIDKey testCtxKey = "request_id"

// testEventCollector collects dispatched events for testing
type testEventCollector struct {
	mu     sync.Mutex
	events []interface{}
}

func newTestEventCollector() *testEventCollector {
	return &testEventCollector{
		events: make([]interface{}, 0),
	}
}

func (c *testEventCollector) dispatch(_ context.Context, event interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	return nil
}

func (c *testEventCollector) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = make([]interface{}, 0)
}

func (c *testEventCollector) findEvent(predicate func(interface{}) bool) interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if predicate(e) {
			return e
		}
	}
	return nil
}

// newTestManager creates a memory-backed Manager with the given collector wired up.
func newTestManager(collector *testEventCollector) *Manager {
	config := &Config{
		Default: "memory",
		Stores: map[string]StoreConfig{
			"memory": {Driver: DriverMemory},
		},
	}
	manager := NewManager(config)
	manager.SetEventDispatcher(collector.dispatch)
	return manager
}

func TestCacheEventNames(t *testing.T) {
	tests := []struct {
		event    interface{ Name() string }
		expected string
	}{
		{&CacheHit{}, "cache.hit"},
		{&CacheMiss{}, "cache.miss"},
		{&CacheWritten{}, "cache.written"},
		{&CacheForgotten{}, "cache.forgotten"},
	}

	for _, tt := range tests {
		if got := tt.event.Name(); got != tt.expected {
			t.Errorf("Event name = %v, want %v", got, tt.expected)
		}
	}
}

func TestDispatchCacheHit(t *testing.T) {
	collector := newTestEventCollector()
	manager := newTestManager(collector)

	// Put a value first so Get triggers a CacheHit
	_ = manager.Put("user:1", "value", time.Minute)
	collector.clear()

	ctx := context.WithValue(context.Background(), testRequestIDKey, "test-123")
	_, _ = manager.GetWithContext(ctx, "user:1")

	event := collector.findEvent(func(e interface{}) bool {
		if ch, ok := e.(*CacheHit); ok {
			return ch.Key == "user:1" &&
				ch.Store == "memory" &&
				ch.Context.Value(testRequestIDKey) == "test-123"
		}
		return false
	})
	if event == nil {
		t.Error("CacheHit not dispatched correctly")
	}
}

func TestDispatchCacheMiss(t *testing.T) {
	collector := newTestEventCollector()
	manager := newTestManager(collector)

	_, _ = manager.Get("user:999")

	event := collector.findEvent(func(e interface{}) bool {
		if cm, ok := e.(*CacheMiss); ok {
			return cm.Key == "user:999" && cm.Store == "memory"
		}
		return false
	})
	if event == nil {
		t.Error("CacheMiss not dispatched correctly")
	}
}

func TestDispatchCacheWritten(t *testing.T) {
	collector := newTestEventCollector()
	manager := newTestManager(collector)

	ttl := 5 * time.Minute
	_ = manager.Put("session:abc", "value", ttl)

	event := collector.findEvent(func(e interface{}) bool {
		if cw, ok := e.(*CacheWritten); ok {
			return cw.Key == "session:abc" &&
				cw.Store == "memory" &&
				cw.TTL == ttl
		}
		return false
	})
	if event == nil {
		t.Error("CacheWritten not dispatched correctly")
	}
}

func TestDispatchCacheWrittenForever(t *testing.T) {
	collector := newTestEventCollector()
	manager := newTestManager(collector)

	_ = manager.Forever("config:app", "value")

	event := collector.findEvent(func(e interface{}) bool {
		if cw, ok := e.(*CacheWritten); ok {
			return cw.Key == "config:app" && cw.TTL == 0
		}
		return false
	})
	if event == nil {
		t.Error("CacheWritten (forever) not dispatched correctly")
	}
}

func TestDispatchCacheForgotten(t *testing.T) {
	collector := newTestEventCollector()
	manager := newTestManager(collector)

	// Put a value first, then forget it
	_ = manager.Put("temp:data", "value", time.Minute)
	collector.clear()

	_ = manager.Forget("temp:data")

	event := collector.findEvent(func(e interface{}) bool {
		if cf, ok := e.(*CacheForgotten); ok {
			return cf.Key == "temp:data" && cf.Store == "memory"
		}
		return false
	})
	if event == nil {
		t.Error("CacheForgotten not dispatched correctly")
	}
}

func TestManagerGetDispatchesEvents(t *testing.T) {
	collector := newTestEventCollector()
	manager := newTestManager(collector)

	// Put a value first (this will also dispatch an event)
	_ = manager.Put("test:key", "value", time.Minute)
	collector.clear()

	// Get existing key - should dispatch CacheHit
	_, found := manager.Get("test:key")
	if !found {
		t.Error("Expected to find the key")
	}

	event := collector.findEvent(func(e interface{}) bool {
		if ch, ok := e.(*CacheHit); ok {
			return ch.Key == "test:key" && ch.Store == "memory"
		}
		return false
	})
	if event == nil {
		t.Error("CacheHit not dispatched on Get")
	}
}

func TestManagerGetMissDispatchesEvents(t *testing.T) {
	collector := newTestEventCollector()
	manager := newTestManager(collector)

	// Get non-existent key - should dispatch CacheMiss
	_, found := manager.Get("nonexistent:key")
	if found {
		t.Error("Did not expect to find the key")
	}

	event := collector.findEvent(func(e interface{}) bool {
		if cm, ok := e.(*CacheMiss); ok {
			return cm.Key == "nonexistent:key" && cm.Store == "memory"
		}
		return false
	})
	if event == nil {
		t.Error("CacheMiss not dispatched on Get")
	}
}

func TestManagerPutDispatchesEvents(t *testing.T) {
	collector := newTestEventCollector()
	manager := newTestManager(collector)

	ttl := 5 * time.Minute
	err := manager.Put("new:key", "value", ttl)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	event := collector.findEvent(func(e interface{}) bool {
		if cw, ok := e.(*CacheWritten); ok {
			return cw.Key == "new:key" && cw.Store == "memory" && cw.TTL == ttl
		}
		return false
	})
	if event == nil {
		t.Error("CacheWritten not dispatched on Put")
	}
}

func TestManagerForeverDispatchesEvents(t *testing.T) {
	collector := newTestEventCollector()
	manager := newTestManager(collector)

	err := manager.Forever("permanent:key", "value")
	if err != nil {
		t.Fatalf("Forever failed: %v", err)
	}

	event := collector.findEvent(func(e interface{}) bool {
		if cw, ok := e.(*CacheWritten); ok {
			return cw.Key == "permanent:key" && cw.Store == "memory" && cw.TTL == 0
		}
		return false
	})
	if event == nil {
		t.Error("CacheWritten not dispatched on Forever")
	}
}

func TestManagerForgetDispatchesEvents(t *testing.T) {
	collector := newTestEventCollector()
	manager := newTestManager(collector)

	// Put and then forget
	_ = manager.Put("temp:key", "value", time.Minute)
	collector.clear()

	err := manager.Forget("temp:key")
	if err != nil {
		t.Fatalf("Forget failed: %v", err)
	}

	event := collector.findEvent(func(e interface{}) bool {
		if cf, ok := e.(*CacheForgotten); ok {
			return cf.Key == "temp:key" && cf.Store == "memory"
		}
		return false
	})
	if event == nil {
		t.Error("CacheForgotten not dispatched on Forget")
	}
}

func TestManagerWithContextMethods(t *testing.T) {
	collector := newTestEventCollector()
	manager := newTestManager(collector)

	ctx := context.WithValue(context.Background(), testRequestIDKey, "req-456")

	// Test PutWithContext
	err := manager.PutWithContext(ctx, "ctx:key", "value", time.Minute)
	if err != nil {
		t.Fatalf("PutWithContext failed: %v", err)
	}

	event := collector.findEvent(func(e interface{}) bool {
		if cw, ok := e.(*CacheWritten); ok {
			return cw.Context.Value(testRequestIDKey) == "req-456"
		}
		return false
	})
	if event == nil {
		t.Error("Context not passed in PutWithContext")
	}

	collector.clear()

	// Test GetWithContext
	_, _ = manager.GetWithContext(ctx, "ctx:key")
	event = collector.findEvent(func(e interface{}) bool {
		if ch, ok := e.(*CacheHit); ok {
			return ch.Context.Value(testRequestIDKey) == "req-456"
		}
		return false
	})
	if event == nil {
		t.Error("Context not passed in GetWithContext")
	}

	collector.clear()

	// Test ForgetWithContext
	_ = manager.ForgetWithContext(ctx, "ctx:key")
	event = collector.findEvent(func(e interface{}) bool {
		if cf, ok := e.(*CacheForgotten); ok {
			return cf.Context.Value(testRequestIDKey) == "req-456"
		}
		return false
	})
	if event == nil {
		t.Error("Context not passed in ForgetWithContext")
	}
}
