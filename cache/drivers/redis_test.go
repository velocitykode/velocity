package drivers

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// newTestRedisStore creates a RedisStore connected to a miniredis instance
func newTestRedisStore(t *testing.T, prefix string) (*RedisStore, *miniredis.Miniredis) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}

	store, err := NewRedisStore(context.Background(), prefix, mr.Host(), mr.Server().Addr().Port, "", 0, false)
	if err != nil {
		mr.Close()
		t.Fatalf("NewRedisStore() error = %v", err)
	}

	return store, mr
}

func TestNewRedisStore(t *testing.T) {
	tests := []struct {
		name    string
		prefix  string
		wantErr bool
	}{
		{"creates store with prefix", "app", false},
		{"creates store with empty prefix", "", false},
		{"creates store with long prefix", "my-application-cache", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mr, err := miniredis.Run()
			if err != nil {
				t.Fatalf("failed to start miniredis: %v", err)
			}
			defer mr.Close()

			store, err := NewRedisStore(context.Background(), tt.prefix, mr.Host(), mr.Server().Addr().Port, "", 0, false)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewRedisStore() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if store != nil {
				defer func() { _ = store.Shutdown(context.Background()) }()
				if got := store.GetPrefix(); got != tt.prefix {
					t.Errorf("GetPrefix() = %v, want %v", got, tt.prefix)
				}
			}
		})
	}
}

func TestNewRedisStore_ConnectionFailure(t *testing.T) {
	t.Run("returns error when connection fails", func(t *testing.T) {
		_, err := NewRedisStore(context.Background(), "test", "invalid-host", 9999, "", 0, false)
		if err == nil {
			t.Error("NewRedisStore() expected error for invalid connection")
		}
	})
}

func TestRedisStore_Shutdown_NilContext(t *testing.T) {
	store, mr := newTestRedisStore(t, "")
	defer mr.Close()

	//lint:ignore SA1012 deliberately exercising the nil-ctx defensive guard
	if err := store.Shutdown(nil); err != nil {
		t.Fatalf("Shutdown(nil) returned error: %v", err)
	}
}

func TestRedisStore_Get(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(s *RedisStore)
		key       string
		wantValue interface{}
		wantFound bool
	}{
		{
			name: "returns value when key exists",
			setup: func(s *RedisStore) {
				s.Put("key1", "value1", time.Hour)
			},
			key:       "key1",
			wantValue: "value1",
			wantFound: true,
		},
		{
			name:      "returns false when key does not exist",
			setup:     func(s *RedisStore) {},
			key:       "nonexistent",
			wantValue: nil,
			wantFound: false,
		},
		{
			name: "returns int value",
			setup: func(s *RedisStore) {
				s.Put("intkey", 42, time.Hour)
			},
			key:       "intkey",
			wantValue: float64(42), // JSON unmarshals numbers as float64
			wantFound: true,
		},
		{
			name: "returns float value",
			setup: func(s *RedisStore) {
				s.Put("floatkey", 3.14, time.Hour)
			},
			key:       "floatkey",
			wantValue: 3.14,
			wantFound: true,
		},
		{
			name: "returns bool value",
			setup: func(s *RedisStore) {
				s.Put("boolkey", true, time.Hour)
			},
			key:       "boolkey",
			wantValue: true,
			wantFound: true,
		},
		{
			name: "returns slice value",
			setup: func(s *RedisStore) {
				s.Put("slicekey", []string{"a", "b", "c"}, time.Hour)
			},
			key:       "slicekey",
			wantValue: []interface{}{"a", "b", "c"}, // JSON unmarshals as []interface{}
			wantFound: true,
		},
		{
			name: "returns map value",
			setup: func(s *RedisStore) {
				s.Put("mapkey", map[string]string{"foo": "bar"}, time.Hour)
			},
			key:       "mapkey",
			wantValue: map[string]interface{}{"foo": "bar"}, // JSON unmarshals as map[string]interface{}
			wantFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mr := newTestRedisStore(t, "")
			defer mr.Close()
			defer func() { _ = store.Shutdown(context.Background()) }()

			tt.setup(store)

			got, found := store.Get(tt.key)
			if found != tt.wantFound {
				t.Errorf("Get() found = %v, want %v", found, tt.wantFound)
			}
			if !reflect.DeepEqual(got, tt.wantValue) {
				t.Errorf("Get() value = %v (%T), want %v (%T)", got, got, tt.wantValue, tt.wantValue)
			}
		})
	}
}

func TestRedisStore_Get_Expiration(t *testing.T) {
	t.Run("returns false when key is expired", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "")
		defer mr.Close()
		defer func() { _ = store.Shutdown(context.Background()) }()

		store.Put("expired", "value", 100*time.Millisecond)

		// Fast forward time in miniredis
		mr.FastForward(200 * time.Millisecond)

		_, found := store.Get("expired")
		if found {
			t.Error("Get() should return false for expired key")
		}
	})
}

func TestRedisStore_GetString(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(s *RedisStore, mr *miniredis.Miniredis)
		key       string
		wantValue string
		wantFound bool
	}{
		{
			name: "returns string value when key exists",
			setup: func(s *RedisStore, mr *miniredis.Miniredis) {
				// Use raw string set for GetString (not JSON)
				mr.Set(s.prefixedKey("key1"), "hello")
			},
			key:       "key1",
			wantValue: "hello",
			wantFound: true,
		},
		{
			name:      "returns false when key does not exist",
			setup:     func(s *RedisStore, mr *miniredis.Miniredis) {},
			key:       "nonexistent",
			wantValue: "",
			wantFound: false,
		},
		{
			name: "returns JSON string from Put",
			setup: func(s *RedisStore, mr *miniredis.Miniredis) {
				s.Put("key1", "world", time.Hour)
			},
			key:       "key1",
			wantValue: `"world"`, // JSON encoded string
			wantFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mr := newTestRedisStore(t, "")
			defer mr.Close()
			defer func() { _ = store.Shutdown(context.Background()) }()

			tt.setup(store, mr)

			got, found := store.GetString(tt.key)
			if found != tt.wantFound {
				t.Errorf("GetString() found = %v, want %v", found, tt.wantFound)
			}
			if got != tt.wantValue {
				t.Errorf("GetString() value = %v, want %v", got, tt.wantValue)
			}
		})
	}
}

func TestRedisStore_Put(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   interface{}
		ttl     time.Duration
		wantErr bool
	}{
		{"stores string value", "key1", "value1", time.Hour, false},
		{"stores int value", "key2", 42, time.Hour, false},
		{"stores float value", "key3", 3.14, time.Hour, false},
		{"stores bool value", "key4", true, time.Hour, false},
		{"stores slice value", "key5", []string{"a", "b"}, time.Hour, false},
		{"stores map value", "key6", map[string]int{"count": 10}, time.Hour, false},
		{"stores with short TTL", "key7", "short", 100 * time.Millisecond, false},
		{"stores nil value", "key8", nil, time.Hour, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mr := newTestRedisStore(t, "")
			defer mr.Close()
			defer func() { _ = store.Shutdown(context.Background()) }()

			err := store.Put(tt.key, tt.value, tt.ttl)
			if (err != nil) != tt.wantErr {
				t.Errorf("Put() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if !store.Has(tt.key) {
					t.Error("Put() value not found after store")
				}
			}
		})
	}
}

func TestRedisStore_Put_MarshalFailure(t *testing.T) {
	t.Run("returns error when value cannot be marshaled", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "")
		defer mr.Close()
		defer func() { _ = store.Shutdown(context.Background()) }()

		// Channels cannot be marshaled to JSON
		ch := make(chan int)
		err := store.Put("key", ch, time.Hour)
		if err == nil {
			t.Error("Put() expected error for unmarshalable value")
		}
	})
}

func TestRedisStore_Put_Overwrites(t *testing.T) {
	t.Run("overwrites existing value", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "")
		defer mr.Close()
		defer func() { _ = store.Shutdown(context.Background()) }()

		store.Put("key", "original", time.Hour)
		store.Put("key", "updated", time.Hour)

		got, found := store.Get("key")
		if !found {
			t.Fatal("Get() value not found")
		}
		if got != "updated" {
			t.Errorf("Put() did not overwrite, got = %v, want updated", got)
		}
	})
}

func TestRedisStore_Forever(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   interface{}
		wantErr bool
	}{
		{"stores value forever", "key1", "value1", false},
		{"stores int value forever", "key2", 42, false},
		{"stores map value forever", "key3", map[string]string{"a": "b"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mr := newTestRedisStore(t, "")
			defer mr.Close()
			defer func() { _ = store.Shutdown(context.Background()) }()

			err := store.Forever(tt.key, tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("Forever() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !store.Has(tt.key) {
				t.Error("Forever() value not found after store")
			}
		})
	}
}

func TestRedisStore_Forever_DoesNotExpire(t *testing.T) {
	t.Run("value stored forever does not expire", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "")
		defer mr.Close()
		defer func() { _ = store.Shutdown(context.Background()) }()

		store.Forever("key", "value")

		// Fast forward time significantly
		mr.FastForward(24 * time.Hour)

		val, found := store.Get("key")
		if !found {
			t.Error("Forever() value should not expire")
		}
		if val != "value" {
			t.Errorf("Forever() value = %v, want value", val)
		}
	})
}

func TestRedisStore_Forever_MarshalFailure(t *testing.T) {
	t.Run("returns error when value cannot be marshaled", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "")
		defer mr.Close()
		defer func() { _ = store.Shutdown(context.Background()) }()

		ch := make(chan int)
		err := store.Forever("key", ch)
		if err == nil {
			t.Error("Forever() expected error for unmarshalable value")
		}
	})
}

func TestRedisStore_Forget(t *testing.T) {
	tests := []struct {
		name  string
		setup func(s *RedisStore)
		key   string
	}{
		{
			name: "removes existing key",
			setup: func(s *RedisStore) {
				s.Put("key1", "value1", time.Hour)
			},
			key: "key1",
		},
		{
			name:  "does not error when key does not exist",
			setup: func(s *RedisStore) {},
			key:   "nonexistent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mr := newTestRedisStore(t, "")
			defer mr.Close()
			defer func() { _ = store.Shutdown(context.Background()) }()

			tt.setup(store)

			err := store.Forget(tt.key)
			if err != nil {
				t.Errorf("Forget() error = %v", err)
			}

			if store.Has(tt.key) {
				t.Error("Forget() key still exists after removal")
			}
		})
	}
}

func TestRedisStore_Forget_WithPrefix(t *testing.T) {
	t.Run("removes correct key when prefix is set", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "myprefix")
		defer mr.Close()
		defer func() { _ = store.Shutdown(context.Background()) }()

		store.Put("key1", "value1", time.Hour)
		store.Put("key2", "value2", time.Hour)

		err := store.Forget("key1")
		if err != nil {
			t.Errorf("Forget() error = %v", err)
		}

		if store.Has("key1") {
			t.Error("Forget() key1 still exists")
		}
		if !store.Has("key2") {
			t.Error("Forget() key2 should still exist")
		}
	})
}

func TestRedisStore_Flush(t *testing.T) {
	t.Run("removes all values", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "")
		defer mr.Close()
		defer func() { _ = store.Shutdown(context.Background()) }()

		store.Put("key1", "value1", time.Hour)
		store.Put("key2", "value2", time.Hour)
		store.Forever("key3", "value3")

		err := store.Flush()
		if err != nil {
			t.Errorf("Flush() error = %v", err)
		}

		if store.Has("key1") || store.Has("key2") || store.Has("key3") {
			t.Error("Flush() keys still exist after flush")
		}
	})
}

func TestRedisStore_Has(t *testing.T) {
	tests := []struct {
		name  string
		setup func(s *RedisStore, mr *miniredis.Miniredis)
		key   string
		want  bool
	}{
		{
			name: "returns true when key exists",
			setup: func(s *RedisStore, mr *miniredis.Miniredis) {
				s.Put("key1", "value1", time.Hour)
			},
			key:  "key1",
			want: true,
		},
		{
			name:  "returns false when key does not exist",
			setup: func(s *RedisStore, mr *miniredis.Miniredis) {},
			key:   "nonexistent",
			want:  false,
		},
		{
			name: "returns false when key is expired",
			setup: func(s *RedisStore, mr *miniredis.Miniredis) {
				s.Put("expired", "value", 100*time.Millisecond)
				mr.FastForward(200 * time.Millisecond)
			},
			key:  "expired",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mr := newTestRedisStore(t, "")
			defer mr.Close()
			defer func() { _ = store.Shutdown(context.Background()) }()

			tt.setup(store, mr)

			if got := store.Has(tt.key); got != tt.want {
				t.Errorf("Has() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRedisStore_Increment(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(s *RedisStore, mr *miniredis.Miniredis)
		key     string
		value   int64
		want    int64
		wantErr bool
	}{
		{
			name: "increments existing value",
			setup: func(s *RedisStore, mr *miniredis.Miniredis) {
				mr.Set(s.prefixedKey("counter"), "10")
			},
			key:   "counter",
			value: 5,
			want:  15,
		},
		{
			name:  "creates value when key does not exist",
			setup: func(s *RedisStore, mr *miniredis.Miniredis) {},
			key:   "counter",
			value: 5,
			want:  5,
		},
		{
			name: "handles large increments",
			setup: func(s *RedisStore, mr *miniredis.Miniredis) {
				mr.Set(s.prefixedKey("counter"), "1000000")
			},
			key:   "counter",
			value: 999999,
			want:  1999999,
		},
		{
			name: "handles negative starting value",
			setup: func(s *RedisStore, mr *miniredis.Miniredis) {
				mr.Set(s.prefixedKey("counter"), "-10")
			},
			key:   "counter",
			value: 5,
			want:  -5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mr := newTestRedisStore(t, "")
			defer mr.Close()
			defer func() { _ = store.Shutdown(context.Background()) }()

			tt.setup(store, mr)

			got, err := store.Increment(tt.key, tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("Increment() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("Increment() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRedisStore_Decrement(t *testing.T) {
	tests := []struct {
		name  string
		setup func(s *RedisStore, mr *miniredis.Miniredis)
		key   string
		value int64
		want  int64
	}{
		{
			name: "decrements existing value",
			setup: func(s *RedisStore, mr *miniredis.Miniredis) {
				mr.Set(s.prefixedKey("counter"), "10")
			},
			key:   "counter",
			value: 3,
			want:  7,
		},
		{
			name:  "creates negative value when key does not exist",
			setup: func(s *RedisStore, mr *miniredis.Miniredis) {},
			key:   "counter",
			value: 5,
			want:  -5,
		},
		{
			name: "decrements to negative",
			setup: func(s *RedisStore, mr *miniredis.Miniredis) {
				mr.Set(s.prefixedKey("counter"), "3")
			},
			key:   "counter",
			value: 10,
			want:  -7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mr := newTestRedisStore(t, "")
			defer mr.Close()
			defer func() { _ = store.Shutdown(context.Background()) }()

			tt.setup(store, mr)

			got, err := store.Decrement(tt.key, tt.value)
			if err != nil {
				t.Errorf("Decrement() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("Decrement() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRedisStore_Remember(t *testing.T) {
	tests := []struct {
		name         string
		setup        func(s *RedisStore)
		key          string
		callback     func() interface{}
		want         interface{}
		callbackRuns bool
	}{
		{
			name: "returns cached value without calling callback",
			setup: func(s *RedisStore) {
				s.Put("key1", "cached", time.Hour)
			},
			key:          "key1",
			callback:     func() interface{} { return "computed" },
			want:         "cached",
			callbackRuns: false,
		},
		{
			name:         "computes and stores value when key does not exist",
			setup:        func(s *RedisStore) {},
			key:          "key1",
			callback:     func() interface{} { return "computed" },
			want:         "computed",
			callbackRuns: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mr := newTestRedisStore(t, "")
			defer mr.Close()
			defer func() { _ = store.Shutdown(context.Background()) }()

			tt.setup(store)

			callbackCalled := false
			callback := func() interface{} {
				callbackCalled = true
				return tt.callback()
			}

			got, err := store.Remember(tt.key, time.Hour, callback)
			if err != nil {
				t.Errorf("Remember() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Remember() = %v, want %v", got, tt.want)
			}
			if callbackCalled != tt.callbackRuns {
				t.Errorf("Remember() callback called = %v, want %v", callbackCalled, tt.callbackRuns)
			}
		})
	}
}

func TestRedisStore_Remember_StoresValue(t *testing.T) {
	t.Run("stores computed value for subsequent calls", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "")
		defer mr.Close()
		defer func() { _ = store.Shutdown(context.Background()) }()

		callCount := 0
		callback := func() interface{} {
			callCount++
			return "computed"
		}

		// First call - should compute
		store.Remember("key", time.Hour, callback)
		if callCount != 1 {
			t.Errorf("First call: callback count = %d, want 1", callCount)
		}

		// Second call - should use cache
		store.Remember("key", time.Hour, callback)
		if callCount != 1 {
			t.Errorf("Second call: callback count = %d, want 1 (should use cache)", callCount)
		}
	})
}

func TestRedisStore_RememberForever(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(s *RedisStore)
		key      string
		callback func() interface{}
		want     interface{}
	}{
		{
			name: "returns cached value without calling callback",
			setup: func(s *RedisStore) {
				s.Forever("key1", "cached")
			},
			key:      "key1",
			callback: func() interface{} { return "computed" },
			want:     "cached",
		},
		{
			name:     "computes and stores value when key does not exist",
			setup:    func(s *RedisStore) {},
			key:      "key1",
			callback: func() interface{} { return "computed" },
			want:     "computed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mr := newTestRedisStore(t, "")
			defer mr.Close()
			defer func() { _ = store.Shutdown(context.Background()) }()

			tt.setup(store)

			got, err := store.RememberForever(tt.key, tt.callback)
			if err != nil {
				t.Errorf("RememberForever() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RememberForever() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRedisStore_Many(t *testing.T) {
	tests := []struct {
		name  string
		setup func(s *RedisStore)
		keys  []string
		want  map[string]interface{}
	}{
		{
			name: "retrieves multiple existing values",
			setup: func(s *RedisStore) {
				s.Put("key1", "value1", time.Hour)
				s.Put("key2", "value2", time.Hour)
				s.Put("key3", "value3", time.Hour)
			},
			keys: []string{"key1", "key2"},
			want: map[string]interface{}{"key1": "value1", "key2": "value2"},
		},
		{
			name: "skips nonexistent keys",
			setup: func(s *RedisStore) {
				s.Put("key1", "value1", time.Hour)
			},
			keys: []string{"key1", "nonexistent"},
			want: map[string]interface{}{"key1": "value1"},
		},
		{
			name:  "returns empty map when no keys exist",
			setup: func(s *RedisStore) {},
			keys:  []string{"key1", "key2"},
			want:  map[string]interface{}{},
		},
		{
			name:  "handles empty keys slice",
			setup: func(s *RedisStore) {},
			keys:  []string{},
			want:  map[string]interface{}{},
		},
		{
			name: "retrieves mixed value types",
			setup: func(s *RedisStore) {
				s.Put("str", "hello", time.Hour)
				s.Put("num", 42, time.Hour)
				s.Put("bool", true, time.Hour)
			},
			keys: []string{"str", "num", "bool"},
			want: map[string]interface{}{"str": "hello", "num": float64(42), "bool": true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mr := newTestRedisStore(t, "")
			defer mr.Close()
			defer func() { _ = store.Shutdown(context.Background()) }()

			tt.setup(store)

			got := store.Many(tt.keys)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Many() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRedisStore_Many_WithPrefix(t *testing.T) {
	t.Run("retrieves values with prefix correctly", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "myapp")
		defer mr.Close()
		defer func() { _ = store.Shutdown(context.Background()) }()

		store.Put("key1", "value1", time.Hour)
		store.Put("key2", "value2", time.Hour)

		got := store.Many([]string{"key1", "key2"})
		want := map[string]interface{}{"key1": "value1", "key2": "value2"}

		if !reflect.DeepEqual(got, want) {
			t.Errorf("Many() = %v, want %v", got, want)
		}
	})
}

func TestRedisStore_PutMany(t *testing.T) {
	t.Run("stores multiple values", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "")
		defer mr.Close()
		defer func() { _ = store.Shutdown(context.Background()) }()

		items := map[string]interface{}{
			"key1": "value1",
			"key2": "value2",
			"key3": float64(42),
		}

		err := store.PutMany(items, time.Hour)
		if err != nil {
			t.Errorf("PutMany() error = %v", err)
		}

		for key, want := range items {
			got, found := store.Get(key)
			if !found {
				t.Errorf("PutMany() key %s not found", key)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("PutMany() key %s = %v, want %v", key, got, want)
			}
		}
	})
}

func TestRedisStore_PutMany_EmptyMap(t *testing.T) {
	t.Run("handles empty map", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "")
		defer mr.Close()
		defer func() { _ = store.Shutdown(context.Background()) }()

		err := store.PutMany(map[string]interface{}{}, time.Hour)
		if err != nil {
			t.Errorf("PutMany() error = %v", err)
		}
	})
}

func TestRedisStore_PutMany_MarshalFailure(t *testing.T) {
	t.Run("returns error when any value cannot be marshaled", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "")
		defer mr.Close()
		defer func() { _ = store.Shutdown(context.Background()) }()

		ch := make(chan int)
		items := map[string]interface{}{
			"key1": "value1",
			"key2": ch,
		}

		err := store.PutMany(items, time.Hour)
		if err == nil {
			t.Error("PutMany() expected error for unmarshalable value")
		}
	})
}

func TestRedisStore_GetPrefix(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		want   string
	}{
		{"returns prefix when set", "myapp", "myapp"},
		{"returns empty string when no prefix", "", ""},
		{"returns complex prefix", "myapp:cache:v1", "myapp:cache:v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mr := newTestRedisStore(t, tt.prefix)
			defer mr.Close()
			defer func() { _ = store.Shutdown(context.Background()) }()

			if got := store.GetPrefix(); got != tt.want {
				t.Errorf("GetPrefix() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRedisStore_PrefixedKeys(t *testing.T) {
	t.Run("isolates keys with different prefixes", func(t *testing.T) {
		mr, err := miniredis.Run()
		if err != nil {
			t.Fatalf("failed to start miniredis: %v", err)
		}
		defer mr.Close()

		store1, err := NewRedisStore(context.Background(), "app1", mr.Host(), mr.Server().Addr().Port, "", 0, false)
		if err != nil {
			t.Fatalf("NewRedisStore() error = %v", err)
		}
		defer func() { _ = store1.Shutdown(context.Background()) }()

		store2, err := NewRedisStore(context.Background(), "app2", mr.Host(), mr.Server().Addr().Port, "", 0, false)
		if err != nil {
			t.Fatalf("NewRedisStore() error = %v", err)
		}
		defer func() { _ = store2.Shutdown(context.Background()) }()

		store1.Put("key", "value1", time.Hour)
		store2.Put("key", "value2", time.Hour)

		val1, _ := store1.Get("key")
		val2, _ := store2.Get("key")

		if val1 != "value1" {
			t.Errorf("store1 Get() = %v, want value1", val1)
		}
		if val2 != "value2" {
			t.Errorf("store2 Get() = %v, want value2", val2)
		}
	})
}

func TestRedisStore_Shutdown(t *testing.T) {
	t.Run("closes connection successfully", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "")
		defer mr.Close()

		err := store.Shutdown(context.Background())
		if err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
}

func TestRedisStore_ConcurrentAccess(t *testing.T) {
	t.Run("handles concurrent reads and writes safely", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "")
		defer mr.Close()
		defer func() { _ = store.Shutdown(context.Background()) }()

		var wg sync.WaitGroup
		numGoroutines := 10
		numOperations := 50

		// Start concurrent writers
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for j := 0; j < numOperations; j++ {
					key := "key"
					store.Put(key, id*numOperations+j, time.Hour)
				}
			}(i)
		}

		// Start concurrent readers
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < numOperations; j++ {
					store.Get("key")
				}
			}()
		}

		wg.Wait()
	})

	t.Run("handles concurrent increment safely", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "")
		defer mr.Close()
		defer func() { _ = store.Shutdown(context.Background()) }()

		mr.Set(store.prefixedKey("counter"), "0")

		var wg sync.WaitGroup
		numGoroutines := 10
		incrementsPerGoroutine := 100

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < incrementsPerGoroutine; j++ {
					store.Increment("counter", 1)
				}
			}()
		}

		wg.Wait()

		// Read the final value directly from miniredis
		val, err := mr.Get(store.prefixedKey("counter"))
		if err != nil {
			t.Fatalf("failed to get counter: %v", err)
		}

		expected := "1000" // 10 * 100
		if val != expected {
			t.Errorf("concurrent Increment() = %v, want %v", val, expected)
		}
	})
}

func TestRedisStore_StructValue(t *testing.T) {
	type TestStruct struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	t.Run("stores and retrieves struct value", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "")
		defer mr.Close()
		defer func() { _ = store.Shutdown(context.Background()) }()

		original := TestStruct{Name: "test", Count: 42}
		err := store.Put("struct", original, time.Hour)
		if err != nil {
			t.Fatalf("Put() error = %v", err)
		}

		got, found := store.Get("struct")
		if !found {
			t.Fatal("Get() struct not found")
		}

		// JSON unmarshals to map[string]interface{}
		gotMap, ok := got.(map[string]interface{})
		if !ok {
			t.Fatalf("Get() returned %T, want map[string]interface{}", got)
		}

		if gotMap["name"] != original.Name {
			t.Errorf("Get() name = %v, want %v", gotMap["name"], original.Name)
		}
		if gotMap["count"] != float64(original.Count) {
			t.Errorf("Get() count = %v, want %v", gotMap["count"], float64(original.Count))
		}
	})
}

func TestRedisStore_NilValue(t *testing.T) {
	t.Run("stores and retrieves nil value", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "")
		defer mr.Close()
		defer func() { _ = store.Shutdown(context.Background()) }()

		err := store.Put("nilkey", nil, time.Hour)
		if err != nil {
			t.Fatalf("Put() error = %v", err)
		}

		got, found := store.Get("nilkey")
		if !found {
			t.Fatal("Get() nil value not found")
		}
		if got != nil {
			t.Errorf("Get() = %v, want nil", got)
		}
	})
}

func TestRedisStore_EmptyStringKey(t *testing.T) {
	t.Run("handles empty string key", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "prefix")
		defer mr.Close()
		defer func() { _ = store.Shutdown(context.Background()) }()

		err := store.Put("", "value", time.Hour)
		if err != nil {
			t.Fatalf("Put() error = %v", err)
		}

		got, found := store.Get("")
		if !found {
			t.Fatal("Get() empty key not found")
		}
		if got != "value" {
			t.Errorf("Get() = %v, want value", got)
		}
	})
}

func TestRedisStore_SpecialCharactersInKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"key with spaces", "key with spaces"},
		{"key with colons", "key:with:colons"},
		{"key with unicode", "key-日本語"},
		{"key with special chars", "key!@#$%^&*()"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mr := newTestRedisStore(t, "")
			defer mr.Close()
			defer func() { _ = store.Shutdown(context.Background()) }()

			err := store.Put(tt.key, "value", time.Hour)
			if err != nil {
				t.Fatalf("Put() error = %v", err)
			}

			got, found := store.Get(tt.key)
			if !found {
				t.Fatalf("Get() key %q not found", tt.key)
			}
			if got != "value" {
				t.Errorf("Get() = %v, want value", got)
			}
		})
	}
}

func TestRedisStore_LargeValue(t *testing.T) {
	t.Run("handles large values", func(t *testing.T) {
		store, mr := newTestRedisStore(t, "")
		defer mr.Close()
		defer func() { _ = store.Shutdown(context.Background()) }()

		// Create a large string (1MB)
		largeValue := make([]byte, 1024*1024)
		for i := range largeValue {
			largeValue[i] = 'a'
		}

		err := store.Put("large", string(largeValue), time.Hour)
		if err != nil {
			t.Fatalf("Put() error = %v", err)
		}

		got, found := store.Get("large")
		if !found {
			t.Fatal("Get() large value not found")
		}
		if got != string(largeValue) {
			t.Error("Get() large value mismatch")
		}
	})
}
