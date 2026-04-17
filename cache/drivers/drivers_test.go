package drivers

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestNewMemoryStore(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		want   string
	}{
		{"creates store with prefix", "app", "app"},
		{"creates store with empty prefix", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore(tt.prefix)
			store.Start()
			defer store.Close()

			if got := store.GetPrefix(); got != tt.want {
				t.Errorf("GetPrefix() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMemoryStore_Get(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(s *MemoryStore)
		key       string
		wantValue interface{}
		wantFound bool
	}{
		{
			name: "returns value when key exists",
			setup: func(s *MemoryStore) {
				s.Put("key1", "value1", time.Hour)
			},
			key:       "key1",
			wantValue: "value1",
			wantFound: true,
		},
		{
			name:      "returns false when key does not exist",
			setup:     func(s *MemoryStore) {},
			key:       "nonexistent",
			wantValue: nil,
			wantFound: false,
		},
		{
			name: "returns false when key is expired",
			setup: func(s *MemoryStore) {
				s.Put("expired", "value", 50*time.Millisecond)
				time.Sleep(100 * time.Millisecond)
			},
			key:       "expired",
			wantValue: nil,
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore("")
			store.Start()
			defer store.Close()

			tt.setup(store)

			got, found := store.Get(tt.key)
			if found != tt.wantFound {
				t.Errorf("Get() found = %v, want %v", found, tt.wantFound)
			}
			if !reflect.DeepEqual(got, tt.wantValue) {
				t.Errorf("Get() value = %v, want %v", got, tt.wantValue)
			}
		})
	}
}

func TestMemoryStore_GetString(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(s *MemoryStore)
		key       string
		wantValue string
		wantFound bool
	}{
		{
			name: "returns string value when key exists",
			setup: func(s *MemoryStore) {
				s.Put("key1", "hello", time.Hour)
			},
			key:       "key1",
			wantValue: "hello",
			wantFound: true,
		},
		{
			name:      "returns false when key does not exist",
			setup:     func(s *MemoryStore) {},
			key:       "nonexistent",
			wantValue: "",
			wantFound: false,
		},
		{
			name: "returns false when value is not a string",
			setup: func(s *MemoryStore) {
				s.Put("key1", 123, time.Hour)
			},
			key:       "key1",
			wantValue: "",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore("")
			store.Start()
			defer store.Close()

			tt.setup(store)

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

func TestMemoryStore_Put(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value interface{}
		ttl   time.Duration
	}{
		{"stores string value", "key1", "value1", time.Hour},
		{"stores int value", "key2", 42, time.Hour},
		{"stores struct value", "key3", struct{ Name string }{"test"}, time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore("")
			store.Start()
			defer store.Close()

			err := store.Put(tt.key, tt.value, tt.ttl)
			if err != nil {
				t.Errorf("Put() error = %v", err)
			}

			got, found := store.Get(tt.key)
			if !found {
				t.Error("Put() value not found after store")
			}
			if !reflect.DeepEqual(got, tt.value) {
				t.Errorf("Put() stored value = %v, want %v", got, tt.value)
			}
		})
	}
}

func TestMemoryStore_Forever(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value interface{}
	}{
		{"stores value forever", "key1", "value1"},
		{"stores int value forever", "key2", 42},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore("")
			store.Start()
			defer store.Close()

			err := store.Forever(tt.key, tt.value)
			if err != nil {
				t.Errorf("Forever() error = %v", err)
			}

			got, found := store.Get(tt.key)
			if !found {
				t.Error("Forever() value not found after store")
			}
			if !reflect.DeepEqual(got, tt.value) {
				t.Errorf("Forever() stored value = %v, want %v", got, tt.value)
			}
		})
	}
}

func TestMemoryStore_Forget(t *testing.T) {
	tests := []struct {
		name  string
		setup func(s *MemoryStore)
		key   string
	}{
		{
			name: "removes existing key",
			setup: func(s *MemoryStore) {
				s.Put("key1", "value1", time.Hour)
			},
			key: "key1",
		},
		{
			name:  "does not error when key does not exist",
			setup: func(s *MemoryStore) {},
			key:   "nonexistent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore("")
			store.Start()
			defer store.Close()

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

func TestMemoryStore_Flush(t *testing.T) {
	t.Run("removes all values", func(t *testing.T) {
		store := NewMemoryStore("")
		store.Start()
		defer store.Close()

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

func TestMemoryStore_Increment(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(s *MemoryStore)
		key       string
		value     int64
		want      int64
		wantErr   bool
		errString string
	}{
		{
			name: "increments existing int64 value",
			setup: func(s *MemoryStore) {
				s.Put("counter", int64(10), time.Hour)
			},
			key:   "counter",
			value: 5,
			want:  15,
		},
		{
			name: "increments existing int value",
			setup: func(s *MemoryStore) {
				s.Put("counter", int(10), time.Hour)
			},
			key:   "counter",
			value: 5,
			want:  15,
		},
		{
			name: "increments existing float64 value",
			setup: func(s *MemoryStore) {
				s.Put("counter", float64(10), time.Hour)
			},
			key:   "counter",
			value: 5,
			want:  15,
		},
		{
			name:  "creates value when key does not exist",
			setup: func(s *MemoryStore) {},
			key:   "counter",
			value: 5,
			want:  5,
		},
		{
			name: "returns error when value is not numeric",
			setup: func(s *MemoryStore) {
				s.Put("key", "not a number", time.Hour)
			},
			key:       "key",
			value:     1,
			want:      0,
			wantErr:   true,
			errString: "value is not numeric",
		},
		{
			name: "preserves expiration time",
			setup: func(s *MemoryStore) {
				s.Put("counter", int64(10), time.Hour)
			},
			key:   "counter",
			value: 5,
			want:  15,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore("")
			store.Start()
			defer store.Close()

			tt.setup(store)

			got, err := store.Increment(tt.key, tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("Increment() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil && err.Error() != tt.errString {
				t.Errorf("Increment() error = %v, want %v", err.Error(), tt.errString)
			}
			if got != tt.want {
				t.Errorf("Increment() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMemoryStore_Increment_PreservesExpiration(t *testing.T) {
	t.Run("preserves expiration after increment", func(t *testing.T) {
		store := NewMemoryStore("")
		store.Start()
		defer store.Close()

		store.Put("counter", int64(10), 200*time.Millisecond)

		_, err := store.Increment("counter", 5)
		if err != nil {
			t.Fatalf("Increment() error = %v", err)
		}

		time.Sleep(250 * time.Millisecond)

		if store.Has("counter") {
			t.Error("Increment() did not preserve expiration - key should have expired")
		}
	})
}

func TestMemoryStore_Decrement(t *testing.T) {
	tests := []struct {
		name  string
		setup func(s *MemoryStore)
		key   string
		value int64
		want  int64
	}{
		{
			name: "decrements existing value",
			setup: func(s *MemoryStore) {
				s.Put("counter", int64(10), time.Hour)
			},
			key:   "counter",
			value: 3,
			want:  7,
		},
		{
			name:  "creates negative value when key does not exist",
			setup: func(s *MemoryStore) {},
			key:   "counter",
			value: 5,
			want:  -5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore("")
			store.Start()
			defer store.Close()

			tt.setup(store)

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

func TestMemoryStore_Remember(t *testing.T) {
	tests := []struct {
		name         string
		setup        func(s *MemoryStore)
		key          string
		callback     func() interface{}
		want         interface{}
		callbackRuns bool
	}{
		{
			name: "returns cached value without calling callback",
			setup: func(s *MemoryStore) {
				s.Put("key1", "cached", time.Hour)
			},
			key:          "key1",
			callback:     func() interface{} { return "computed" },
			want:         "cached",
			callbackRuns: false,
		},
		{
			name:         "computes and stores value when key does not exist",
			setup:        func(s *MemoryStore) {},
			key:          "key1",
			callback:     func() interface{} { return "computed" },
			want:         "computed",
			callbackRuns: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore("")
			store.Start()
			defer store.Close()

			tt.setup(store)

			got, err := store.Remember(tt.key, time.Hour, tt.callback)
			if err != nil {
				t.Errorf("Remember() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Remember() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMemoryStore_RememberForever(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(s *MemoryStore)
		key      string
		callback func() interface{}
		want     interface{}
	}{
		{
			name: "returns cached value without calling callback",
			setup: func(s *MemoryStore) {
				s.Forever("key1", "cached")
			},
			key:      "key1",
			callback: func() interface{} { return "computed" },
			want:     "cached",
		},
		{
			name:     "computes and stores value when key does not exist",
			setup:    func(s *MemoryStore) {},
			key:      "key1",
			callback: func() interface{} { return "computed" },
			want:     "computed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore("")
			store.Start()
			defer store.Close()

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

func TestMemoryStore_Many(t *testing.T) {
	tests := []struct {
		name  string
		setup func(s *MemoryStore)
		keys  []string
		want  map[string]interface{}
	}{
		{
			name: "retrieves multiple existing values",
			setup: func(s *MemoryStore) {
				s.Put("key1", "value1", time.Hour)
				s.Put("key2", "value2", time.Hour)
				s.Put("key3", "value3", time.Hour)
			},
			keys: []string{"key1", "key2"},
			want: map[string]interface{}{"key1": "value1", "key2": "value2"},
		},
		{
			name: "skips nonexistent keys",
			setup: func(s *MemoryStore) {
				s.Put("key1", "value1", time.Hour)
			},
			keys: []string{"key1", "nonexistent"},
			want: map[string]interface{}{"key1": "value1"},
		},
		{
			name:  "returns empty map when no keys exist",
			setup: func(s *MemoryStore) {},
			keys:  []string{"key1", "key2"},
			want:  map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore("")
			store.Start()
			defer store.Close()

			tt.setup(store)

			got := store.Many(tt.keys)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Many() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMemoryStore_PutMany(t *testing.T) {
	t.Run("stores multiple values", func(t *testing.T) {
		store := NewMemoryStore("")
		store.Start()
		defer store.Close()

		items := map[string]interface{}{
			"key1": "value1",
			"key2": "value2",
			"key3": 42,
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

func TestMemoryStore_Has(t *testing.T) {
	tests := []struct {
		name  string
		setup func(s *MemoryStore)
		key   string
		want  bool
	}{
		{
			name: "returns true when key exists",
			setup: func(s *MemoryStore) {
				s.Put("key1", "value1", time.Hour)
			},
			key:  "key1",
			want: true,
		},
		{
			name:  "returns false when key does not exist",
			setup: func(s *MemoryStore) {},
			key:   "nonexistent",
			want:  false,
		},
		{
			name: "returns false when key is expired",
			setup: func(s *MemoryStore) {
				s.Put("expired", "value", 50*time.Millisecond)
				time.Sleep(100 * time.Millisecond)
			},
			key:  "expired",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore("")
			store.Start()
			defer store.Close()

			tt.setup(store)

			if got := store.Has(tt.key); got != tt.want {
				t.Errorf("Has() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMemoryStore_GetPrefix(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		want   string
	}{
		{"returns prefix when set", "myapp", "myapp"},
		{"returns empty string when no prefix", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore(tt.prefix)
			store.Start()
			defer store.Close()

			if got := store.GetPrefix(); got != tt.want {
				t.Errorf("GetPrefix() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMemoryStore_PrefixedKeys(t *testing.T) {
	t.Run("isolates keys with different prefixes", func(t *testing.T) {
		store1 := NewMemoryStore("app1")
		store1.Start()
		defer store1.Close()
		store2 := NewMemoryStore("app2")
		store2.Start()
		defer store2.Close()

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

// FileStore Tests

func TestNewFileStore(t *testing.T) {
	tests := []struct {
		name    string
		prefix  string
		path    string
		wantErr bool
	}{
		{"creates store with custom path", "app", "", false},
		{"creates store with prefix", "myprefix", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			path := tt.path
			if path == "" {
				path = tempDir
			}

			store, err := NewFileStore(tt.prefix, path)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewFileStore() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if store == nil && !tt.wantErr {
				t.Error("NewFileStore() returned nil store")
			}
		})
	}
}

func TestNewFileStore_DefaultPath(t *testing.T) {
	t.Run("uses default path when empty", func(t *testing.T) {
		// This test verifies the default path behavior
		// We clean up after ourselves
		defaultPath := "storage/framework/cache/data"

		store, err := NewFileStore("test", "")
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}

		if store.path != defaultPath {
			t.Errorf("NewFileStore() path = %v, want %v", store.path, defaultPath)
		}

		// Cleanup
		os.RemoveAll("storage")
	})
}

func TestFileStore_Get(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(s *FileStore)
		key       string
		wantValue interface{}
		wantFound bool
	}{
		{
			name: "returns value when key exists",
			setup: func(s *FileStore) {
				s.Put("key1", "value1", time.Hour)
			},
			key:       "key1",
			wantValue: "value1",
			wantFound: true,
		},
		{
			name:      "returns false when key does not exist",
			setup:     func(s *FileStore) {},
			key:       "nonexistent",
			wantValue: nil,
			wantFound: false,
		},
		{
			name: "returns false when key is expired",
			setup: func(s *FileStore) {
				s.Put("expired", "value", 50*time.Millisecond)
				time.Sleep(100 * time.Millisecond)
			},
			key:       "expired",
			wantValue: nil,
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			store, err := NewFileStore("", tempDir)
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}

			tt.setup(store)

			got, found := store.Get(tt.key)
			if found != tt.wantFound {
				t.Errorf("Get() found = %v, want %v", found, tt.wantFound)
			}
			if !reflect.DeepEqual(got, tt.wantValue) {
				t.Errorf("Get() value = %v, want %v", got, tt.wantValue)
			}
		})
	}
}

func TestFileStore_GetString(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(s *FileStore)
		key       string
		wantValue string
		wantFound bool
	}{
		{
			name: "returns string value when key exists",
			setup: func(s *FileStore) {
				s.Put("key1", "hello", time.Hour)
			},
			key:       "key1",
			wantValue: "hello",
			wantFound: true,
		},
		{
			name:      "returns false when key does not exist",
			setup:     func(s *FileStore) {},
			key:       "nonexistent",
			wantValue: "",
			wantFound: false,
		},
		{
			name: "returns false when value is not a string",
			setup: func(s *FileStore) {
				s.Put("key1", 123, time.Hour)
			},
			key:       "key1",
			wantValue: "",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			store, err := NewFileStore("", tempDir)
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}

			tt.setup(store)

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

func TestFileStore_Put(t *testing.T) {
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
		{"stores map value", "key4", map[string]string{"a": "b"}, time.Hour, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			store, err := NewFileStore("", tempDir)
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}

			err = store.Put(tt.key, tt.value, tt.ttl)
			if (err != nil) != tt.wantErr {
				t.Errorf("Put() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				_, found := store.Get(tt.key)
				if !found {
					t.Error("Put() value not found after store")
				}
			}
		})
	}
}

func TestFileStore_Put_MarshalFailure(t *testing.T) {
	t.Run("returns error when value cannot be marshaled", func(t *testing.T) {
		tempDir := t.TempDir()
		store, err := NewFileStore("", tempDir)
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}

		// Channels cannot be marshaled to JSON
		ch := make(chan int)
		err = store.Put("key", ch, time.Hour)
		if err == nil {
			t.Error("Put() expected error for unmarshalable value")
		}
	})
}

func TestFileStore_Forever(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   interface{}
		wantErr bool
	}{
		{"stores value forever", "key1", "value1", false},
		{"stores int value forever", "key2", 42, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			store, err := NewFileStore("", tempDir)
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}

			err = store.Forever(tt.key, tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("Forever() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			_, found := store.Get(tt.key)
			if !found {
				t.Error("Forever() value not found after store")
			}
		})
	}
}

func TestFileStore_Forever_MarshalFailure(t *testing.T) {
	t.Run("returns error when value cannot be marshaled", func(t *testing.T) {
		tempDir := t.TempDir()
		store, err := NewFileStore("", tempDir)
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}

		ch := make(chan int)
		err = store.Forever("key", ch)
		if err == nil {
			t.Error("Forever() expected error for unmarshalable value")
		}
	})
}

func TestFileStore_Forget(t *testing.T) {
	tests := []struct {
		name  string
		setup func(s *FileStore)
		key   string
	}{
		{
			name: "removes existing key",
			setup: func(s *FileStore) {
				s.Put("key1", "value1", time.Hour)
			},
			key: "key1",
		},
		{
			name:  "does not error when key does not exist",
			setup: func(s *FileStore) {},
			key:   "nonexistent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			store, err := NewFileStore("", tempDir)
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}

			tt.setup(store)

			err = store.Forget(tt.key)
			if err != nil {
				t.Errorf("Forget() error = %v", err)
			}

			if store.Has(tt.key) {
				t.Error("Forget() key still exists after removal")
			}
		})
	}
}

func TestFileStore_Flush(t *testing.T) {
	t.Run("removes all values", func(t *testing.T) {
		tempDir := t.TempDir()
		store, err := NewFileStore("", tempDir)
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}

		store.Put("key1", "value1", time.Hour)
		store.Put("key2", "value2", time.Hour)
		store.Forever("key3", "value3")

		err = store.Flush()
		if err != nil {
			t.Errorf("Flush() error = %v", err)
		}

		if store.Has("key1") || store.Has("key2") || store.Has("key3") {
			t.Error("Flush() keys still exist after flush")
		}
	})
}

func TestFileStore_Increment(t *testing.T) {
	tests := []struct {
		name  string
		setup func(s *FileStore)
		key   string
		value int64
		want  int64
	}{
		{
			name: "increments existing value",
			setup: func(s *FileStore) {
				s.Put("counter", 10, time.Hour)
			},
			key:   "counter",
			value: 5,
			want:  15,
		},
		{
			name:  "creates value when key does not exist",
			setup: func(s *FileStore) {},
			key:   "counter",
			value: 5,
			want:  5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			store, err := NewFileStore("", tempDir)
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}

			tt.setup(store)

			got, err := store.Increment(tt.key, tt.value)
			if err != nil {
				t.Errorf("Increment() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("Increment() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFileStore_Increment_PreservesExpiration(t *testing.T) {
	t.Run("preserves expiration after increment", func(t *testing.T) {
		tempDir := t.TempDir()
		store, err := NewFileStore("", tempDir)
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}

		store.Put("counter", 10, 200*time.Millisecond)

		_, err = store.Increment("counter", 5)
		if err != nil {
			t.Fatalf("Increment() error = %v", err)
		}

		time.Sleep(250 * time.Millisecond)

		if store.Has("counter") {
			t.Error("Increment() did not preserve expiration - key should have expired")
		}
	})
}

func TestFileStore_Decrement(t *testing.T) {
	tests := []struct {
		name  string
		setup func(s *FileStore)
		key   string
		value int64
		want  int64
	}{
		{
			name: "decrements existing value",
			setup: func(s *FileStore) {
				s.Put("counter", 10, time.Hour)
			},
			key:   "counter",
			value: 3,
			want:  7,
		},
		{
			name:  "creates negative value when key does not exist",
			setup: func(s *FileStore) {},
			key:   "counter",
			value: 5,
			want:  -5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			store, err := NewFileStore("", tempDir)
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}

			tt.setup(store)

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

func TestFileStore_Remember(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(s *FileStore)
		key      string
		callback func() interface{}
		want     interface{}
	}{
		{
			name: "returns cached value without calling callback",
			setup: func(s *FileStore) {
				s.Put("key1", "cached", time.Hour)
			},
			key:      "key1",
			callback: func() interface{} { return "computed" },
			want:     "cached",
		},
		{
			name:     "computes and stores value when key does not exist",
			setup:    func(s *FileStore) {},
			key:      "key1",
			callback: func() interface{} { return "computed" },
			want:     "computed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			store, err := NewFileStore("", tempDir)
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}

			tt.setup(store)

			got, err := store.Remember(tt.key, time.Hour, tt.callback)
			if err != nil {
				t.Errorf("Remember() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Remember() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFileStore_RememberForever(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(s *FileStore)
		key      string
		callback func() interface{}
		want     interface{}
	}{
		{
			name: "returns cached value without calling callback",
			setup: func(s *FileStore) {
				s.Forever("key1", "cached")
			},
			key:      "key1",
			callback: func() interface{} { return "computed" },
			want:     "cached",
		},
		{
			name:     "computes and stores value when key does not exist",
			setup:    func(s *FileStore) {},
			key:      "key1",
			callback: func() interface{} { return "computed" },
			want:     "computed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			store, err := NewFileStore("", tempDir)
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}

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

func TestFileStore_Many(t *testing.T) {
	tests := []struct {
		name  string
		setup func(s *FileStore)
		keys  []string
		want  map[string]interface{}
	}{
		{
			name: "retrieves multiple existing values",
			setup: func(s *FileStore) {
				s.Put("key1", "value1", time.Hour)
				s.Put("key2", "value2", time.Hour)
				s.Put("key3", "value3", time.Hour)
			},
			keys: []string{"key1", "key2"},
			want: map[string]interface{}{"key1": "value1", "key2": "value2"},
		},
		{
			name: "skips nonexistent keys",
			setup: func(s *FileStore) {
				s.Put("key1", "value1", time.Hour)
			},
			keys: []string{"key1", "nonexistent"},
			want: map[string]interface{}{"key1": "value1"},
		},
		{
			name:  "returns empty map when no keys exist",
			setup: func(s *FileStore) {},
			keys:  []string{"key1", "key2"},
			want:  map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			store, err := NewFileStore("", tempDir)
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}

			tt.setup(store)

			got := store.Many(tt.keys)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Many() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFileStore_PutMany(t *testing.T) {
	t.Run("stores multiple values", func(t *testing.T) {
		tempDir := t.TempDir()
		store, err := NewFileStore("", tempDir)
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}

		items := map[string]interface{}{
			"key1": "value1",
			"key2": "value2",
			"key3": float64(42),
		}

		err = store.PutMany(items, time.Hour)
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

func TestFileStore_Has(t *testing.T) {
	tests := []struct {
		name  string
		setup func(s *FileStore)
		key   string
		want  bool
	}{
		{
			name: "returns true when key exists",
			setup: func(s *FileStore) {
				s.Put("key1", "value1", time.Hour)
			},
			key:  "key1",
			want: true,
		},
		{
			name:  "returns false when key does not exist",
			setup: func(s *FileStore) {},
			key:   "nonexistent",
			want:  false,
		},
		{
			name: "returns false when key is expired",
			setup: func(s *FileStore) {
				s.Put("expired", "value", 50*time.Millisecond)
				time.Sleep(100 * time.Millisecond)
			},
			key:  "expired",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			store, err := NewFileStore("", tempDir)
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}

			tt.setup(store)

			if got := store.Has(tt.key); got != tt.want {
				t.Errorf("Has() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFileStore_GetPrefix(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		want   string
	}{
		{"returns prefix when set", "myapp", "myapp"},
		{"returns empty string when no prefix", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			store, err := NewFileStore(tt.prefix, tempDir)
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}

			if got := store.GetPrefix(); got != tt.want {
				t.Errorf("GetPrefix() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFileStore_CorruptedFile(t *testing.T) {
	t.Run("returns false when file contains corrupted data", func(t *testing.T) {
		tempDir := t.TempDir()
		store, err := NewFileStore("", tempDir)
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}

		// Put a valid value to get the file path
		store.Put("key1", "value1", time.Hour)

		// Get the cache file path and corrupt it
		filePath := store.getCacheFilePath("key1")
		err = os.WriteFile(filePath, []byte("not valid json"), 0644)
		if err != nil {
			t.Fatalf("Failed to write corrupted data: %v", err)
		}

		_, found := store.Get("key1")
		if found {
			t.Error("Get() should return false for corrupted file")
		}
	})
}

func TestFileStore_PrefixedKeys(t *testing.T) {
	t.Run("isolates keys with different prefixes", func(t *testing.T) {
		tempDir := t.TempDir()

		store1, err := NewFileStore("app1", tempDir)
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}
		store2, err := NewFileStore("app2", filepath.Join(tempDir, "app2"))
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}

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

func TestFileStore_PutMany_MarshalFailure(t *testing.T) {
	t.Run("returns error when any value cannot be marshaled", func(t *testing.T) {
		tempDir := t.TempDir()
		store, err := NewFileStore("", tempDir)
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}

		ch := make(chan int)
		items := map[string]interface{}{
			"key1": "value1",
			"key2": ch,
		}

		err = store.PutMany(items, time.Hour)
		if err == nil {
			t.Error("PutMany() expected error for unmarshalable value")
		}
	})
}

func TestFileStore_CacheFilePath(t *testing.T) {
	t.Run("creates consistent file paths for same key", func(t *testing.T) {
		tempDir := t.TempDir()
		store, err := NewFileStore("", tempDir)
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}

		path1 := store.getCacheFilePath("mykey")
		path2 := store.getCacheFilePath("mykey")

		if path1 != path2 {
			t.Errorf("getCacheFilePath() returned different paths for same key: %s vs %s", path1, path2)
		}
	})

	t.Run("creates different file paths for different keys", func(t *testing.T) {
		tempDir := t.TempDir()
		store, err := NewFileStore("", tempDir)
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}

		path1 := store.getCacheFilePath("key1")
		path2 := store.getCacheFilePath("key2")

		if path1 == path2 {
			t.Errorf("getCacheFilePath() returned same path for different keys")
		}
	})
}

func TestMemoryStore_ConcurrentAccess(t *testing.T) {
	t.Run("handles concurrent reads and writes safely", func(t *testing.T) {
		store := NewMemoryStore("")
		store.Start()
		defer store.Close()

		done := make(chan bool)
		numGoroutines := 10
		numOperations := 100

		// Start concurrent writers
		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				for j := 0; j < numOperations; j++ {
					key := "key"
					store.Put(key, id*numOperations+j, time.Hour)
				}
				done <- true
			}(i)
		}

		// Start concurrent readers
		for i := 0; i < numGoroutines; i++ {
			go func() {
				for j := 0; j < numOperations; j++ {
					store.Get("key")
				}
				done <- true
			}()
		}

		// Wait for all goroutines to complete
		for i := 0; i < numGoroutines*2; i++ {
			<-done
		}
	})

	t.Run("handles concurrent increment safely", func(t *testing.T) {
		store := NewMemoryStore("")
		store.Start()
		defer store.Close()

		store.Put("counter", int64(0), time.Hour)

		done := make(chan bool)
		numGoroutines := 10
		incrementsPerGoroutine := 100

		for i := 0; i < numGoroutines; i++ {
			go func() {
				for j := 0; j < incrementsPerGoroutine; j++ {
					store.Increment("counter", 1)
				}
				done <- true
			}()
		}

		for i := 0; i < numGoroutines; i++ {
			<-done
		}

		val, found := store.Get("counter")
		if !found {
			t.Fatal("counter not found")
		}
		expected := int64(numGoroutines * incrementsPerGoroutine)
		if val != expected {
			t.Errorf("concurrent Increment() = %v, want %v", val, expected)
		}
	})
}

func TestFileStore_ConcurrentAccess(t *testing.T) {
	t.Run("handles concurrent reads and writes safely", func(t *testing.T) {
		tempDir := t.TempDir()
		store, err := NewFileStore("", tempDir)
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}

		done := make(chan bool)
		numGoroutines := 5
		numOperations := 20

		// Start concurrent writers
		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				for j := 0; j < numOperations; j++ {
					key := "key"
					store.Put(key, id*numOperations+j, time.Hour)
				}
				done <- true
			}(i)
		}

		// Start concurrent readers
		for i := 0; i < numGoroutines; i++ {
			go func() {
				for j := 0; j < numOperations; j++ {
					store.Get("key")
				}
				done <- true
			}()
		}

		// Wait for all goroutines to complete
		for i := 0; i < numGoroutines*2; i++ {
			<-done
		}
	})
}

// RedisStore tests are in redis_test.go

func TestMemoryStore_Many_SkipsExpired(t *testing.T) {
	t.Run("skips expired keys when retrieving many", func(t *testing.T) {
		store := NewMemoryStore("")
		store.Start()
		defer store.Close()

		store.Put("key1", "value1", time.Hour)
		store.Put("key2", "value2", 50*time.Millisecond)
		store.Put("key3", "value3", time.Hour)

		time.Sleep(100 * time.Millisecond)

		result := store.Many([]string{"key1", "key2", "key3"})

		if len(result) != 2 {
			t.Errorf("Many() returned %d items, want 2", len(result))
		}
		if _, exists := result["key2"]; exists {
			t.Error("Many() should not return expired key2")
		}
	})
}

func TestFileStore_InvalidJSON(t *testing.T) {
	t.Run("returns false when file has invalid JSON value", func(t *testing.T) {
		tempDir := t.TempDir()
		store, err := NewFileStore("", tempDir)
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}

		// Put a valid value first
		store.Put("key1", "value1", time.Hour)

		// Corrupt the file with valid JSON structure but invalid value field
		filePath := store.getCacheFilePath("key1")
		corruptedJSON := `{"value": invalid, "expiration": null}`
		err = os.WriteFile(filePath, []byte(corruptedJSON), 0644)
		if err != nil {
			t.Fatalf("Failed to write corrupted data: %v", err)
		}

		_, found := store.Get("key1")
		if found {
			t.Error("Get() should return false for invalid JSON")
		}
	})
}

func TestMemoryStore_Forever_DoesNotExpire(t *testing.T) {
	t.Run("value stored forever does not expire", func(t *testing.T) {
		store := NewMemoryStore("")
		store.Start()
		defer store.Close()

		store.Forever("key", "value")

		// Even after some time, the value should still exist
		time.Sleep(100 * time.Millisecond)

		val, found := store.Get("key")
		if !found {
			t.Error("Forever() value should not expire")
		}
		if val != "value" {
			t.Errorf("Forever() value = %v, want value", val)
		}
	})
}

func TestFileStore_Forever_DoesNotExpire(t *testing.T) {
	t.Run("value stored forever does not expire", func(t *testing.T) {
		tempDir := t.TempDir()
		store, err := NewFileStore("", tempDir)
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}

		store.Forever("key", "value")

		// Even after some time, the value should still exist
		time.Sleep(100 * time.Millisecond)

		val, found := store.Get("key")
		if !found {
			t.Error("Forever() value should not expire")
		}
		if val != "value" {
			t.Errorf("Forever() value = %v, want value", val)
		}
	})
}

func TestMemoryStore_Increment_CreatesWithNoExpiration(t *testing.T) {
	t.Run("creates new key with no expiration when key does not exist", func(t *testing.T) {
		store := NewMemoryStore("")
		store.Start()
		defer store.Close()

		_, err := store.Increment("newkey", 5)
		if err != nil {
			t.Errorf("Increment() error = %v", err)
		}

		// Value should persist as it has no expiration
		time.Sleep(100 * time.Millisecond)

		val, found := store.Get("newkey")
		if !found {
			t.Error("Increment() created key should not expire")
		}
		if val != int64(5) {
			t.Errorf("Increment() value = %v, want 5", val)
		}
	})
}

func TestFileStore_Increment_PreservesNoExpiration(t *testing.T) {
	t.Run("preserves no expiration for forever values", func(t *testing.T) {
		tempDir := t.TempDir()
		store, err := NewFileStore("", tempDir)
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}

		store.Forever("counter", 10)

		_, err = store.Increment("counter", 5)
		if err != nil {
			t.Errorf("Increment() error = %v", err)
		}

		// Value should persist as it has no expiration
		time.Sleep(100 * time.Millisecond)

		val, found := store.Get("counter")
		if !found {
			t.Error("Increment() should preserve no expiration")
		}
		if val != float64(15) {
			t.Errorf("Increment() value = %v, want 15", val)
		}
	})
}

func TestMemoryStore_Forget_WithPrefix(t *testing.T) {
	t.Run("removes correct key when prefix is set", func(t *testing.T) {
		store := NewMemoryStore("myprefix")
		store.Start()
		defer store.Close()

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

func TestFileStore_Forget_WithPrefix(t *testing.T) {
	t.Run("removes correct key when prefix is set", func(t *testing.T) {
		tempDir := t.TempDir()
		store, err := NewFileStore("myprefix", tempDir)
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}

		store.Put("key1", "value1", time.Hour)
		store.Put("key2", "value2", time.Hour)

		err = store.Forget("key1")
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
