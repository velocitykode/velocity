package cache

import (
	"context"
	"errors"
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

func (c *testEventCollector) countEvents(predicate func(interface{}) bool) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.events {
		if predicate(e) {
			n++
		}
	}
	return n
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
		{&CacheOperationFailed{}, "cache.operation.failed"},
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

// failingStore is a cache.Store whose mutating operations all return err. It
// lets the CacheOperationFailed tests exercise the Manager's error paths
// without a real backend.
type failingStore struct{ err error }

func (s *failingStore) GetPrefix() string                                   { return "" }
func (s *failingStore) GetCtx(context.Context, string) (interface{}, bool)  { return nil, false }
func (s *failingStore) Get(string) (interface{}, bool)                      { return nil, false }
func (s *failingStore) GetStringCtx(context.Context, string) (string, bool) { return "", false }
func (s *failingStore) GetString(string) (string, bool)                     { return "", false }
func (s *failingStore) PutCtx(context.Context, string, interface{}, time.Duration) error {
	return s.err
}
func (s *failingStore) Put(string, interface{}, time.Duration) error { return s.err }
func (s *failingStore) AddCtx(context.Context, string, interface{}, time.Duration) (bool, error) {
	return false, s.err
}
func (s *failingStore) Add(string, interface{}, time.Duration) (bool, error)  { return false, s.err }
func (s *failingStore) ForeverCtx(context.Context, string, interface{}) error { return s.err }
func (s *failingStore) Forever(string, interface{}) error                     { return s.err }
func (s *failingStore) ForgetCtx(context.Context, string) error               { return s.err }
func (s *failingStore) Forget(string) error                                   { return s.err }
func (s *failingStore) FlushCtx(context.Context) error                        { return s.err }
func (s *failingStore) Flush() error                                          { return s.err }
func (s *failingStore) IncrementCtx(context.Context, string, int64) (int64, error) {
	return 0, s.err
}
func (s *failingStore) Increment(string, int64) (int64, error) { return 0, s.err }
func (s *failingStore) DecrementCtx(context.Context, string, int64) (int64, error) {
	return 0, s.err
}
func (s *failingStore) Decrement(string, int64) (int64, error) { return 0, s.err }
func (s *failingStore) Remember(string, time.Duration, func() interface{}) (interface{}, error) {
	return nil, s.err
}
func (s *failingStore) RememberForever(string, func() interface{}) (interface{}, error) {
	return nil, s.err
}
func (s *failingStore) ManyCtx(context.Context, []string) map[string]interface{} { return nil }
func (s *failingStore) Many([]string) map[string]interface{}                     { return nil }
func (s *failingStore) PutManyCtx(context.Context, map[string]interface{}, time.Duration) error {
	return s.err
}
func (s *failingStore) PutMany(map[string]interface{}, time.Duration) error { return s.err }
func (s *failingStore) HasCtx(context.Context, string) bool                 { return false }
func (s *failingStore) Has(string) bool                                     { return false }

// newFailingManager wires a Manager whose default store always errors. The
// store is injected directly into the manager's store map so no driver
// registration is required.
func newFailingManager(collector *testEventCollector, err error) *Manager {
	config := &Config{
		Default: "failing",
		Stores:  map[string]StoreConfig{"failing": {Driver: "failing"}},
	}
	manager := NewManager(config)
	manager.SetEventDispatcher(collector.dispatch)
	manager.stores["failing"] = &failingStore{err: err}
	return manager
}

// lockWinStore wins the populater lock (AddCtx succeeds) and never reports a
// cached value, but every value write fails. It exercises the Remember*E
// write-failure branches that failingStore cannot reach (AddCtx always errors
// there, so the write is never attempted).
type lockWinStore struct {
	failingStore
}

func (s *lockWinStore) AddCtx(context.Context, string, interface{}, time.Duration) (bool, error) {
	return true, nil
}

func newLockWinManager(collector *testEventCollector, err error) *Manager {
	config := &Config{
		Default: "lockwin",
		Stores:  map[string]StoreConfig{"lockwin": {Driver: "lockwin"}},
	}
	manager := NewManager(config)
	manager.SetEventDispatcher(collector.dispatch)
	manager.stores["lockwin"] = &lockWinStore{failingStore{err: err}}
	return manager
}

func TestDispatchCacheOperationFailedRememberWrite(t *testing.T) {
	wantErr := errors.New("write boom")
	cases := []struct {
		name   string
		key    string
		invoke func(m *Manager)
	}{
		{"remember_write", "rk", func(m *Manager) {
			_, _ = m.RememberE("rk", time.Minute, func() (interface{}, error) { return "v", nil })
		}},
		{"rememberforever_write", "rf", func(m *Manager) {
			_, _ = m.RememberForeverE("rf", func() (interface{}, error) { return "v", nil })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collector := newTestEventCollector()
			manager := newLockWinManager(collector, wantErr)

			tc.invoke(manager)

			event := collector.findEvent(func(e interface{}) bool {
				cf, ok := e.(*CacheOperationFailed)
				return ok &&
					cf.Op == "put" &&
					cf.Key == tc.key &&
					cf.Store == "lockwin" &&
					cf.Error == wantErr.Error()
			})
			if event == nil {
				t.Fatalf("CacheOperationFailed not dispatched on write failure (key=%q)", tc.key)
			}
		})
	}
}

func TestDispatchCacheOperationFailed(t *testing.T) {
	wantErr := errors.New("store boom")
	cases := []struct {
		name   string
		op     string
		key    string
		invoke func(m *Manager)
	}{
		{"put", "put", "k1", func(m *Manager) { _ = m.Put("k1", "v", time.Minute) }},
		{"add", "add", "k2", func(m *Manager) { _, _ = m.Add("k2", "v", time.Minute) }},
		{"forever", "put", "k3", func(m *Manager) { _ = m.Forever("k3", "v") }},
		{"forget", "forget", "k4", func(m *Manager) { _ = m.Forget("k4") }},
		{"flush", "flush", "", func(m *Manager) { _ = m.Flush() }},
		{"increment", "increment", "k5", func(m *Manager) { _, _ = m.Increment("k5", 1) }},
		{"decrement", "decrement", "k6", func(m *Manager) { _, _ = m.Decrement("k6", 1) }},
		{"putmany", "put_many", "", func(m *Manager) { _ = m.PutMany(map[string]interface{}{"k7": "v"}, time.Minute) }},
		{"remember_lock", "add", rememberLockKey("k8"), func(m *Manager) {
			_, _ = m.RememberE("k8", time.Minute, func() (interface{}, error) { return "v", nil })
		}},
		{"rememberforever_lock", "add", rememberLockKey("k9"), func(m *Manager) {
			_, _ = m.RememberForeverE("k9", func() (interface{}, error) { return "v", nil })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collector := newTestEventCollector()
			manager := newFailingManager(collector, wantErr)

			tc.invoke(manager)

			matches := func(e interface{}) bool {
				cf, ok := e.(*CacheOperationFailed)
				return ok &&
					cf.Op == tc.op &&
					cf.Key == tc.key &&
					cf.Store == "failing" &&
					cf.Error == wantErr.Error()
			}
			if got := collector.countEvents(matches); got != 1 {
				t.Fatalf("CacheOperationFailed dispatched %d times, want exactly 1 (op=%s key=%q)", got, tc.op, tc.key)
			}
			isFailure := func(e interface{}) bool {
				_, ok := e.(*CacheOperationFailed)
				return ok
			}
			if got := collector.countEvents(isFailure); got != 1 {
				t.Fatalf("recorded %d CacheOperationFailed events, want exactly 1 (op=%s key=%q)", got, tc.op, tc.key)
			}
		})
	}
}
