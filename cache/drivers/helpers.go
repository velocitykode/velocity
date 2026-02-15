package drivers

import "time"

// CacheGetter is a minimal interface for reading from a cache store.
type CacheGetter interface {
	Get(key string) (interface{}, bool)
}

// CachePutter is a minimal interface for writing to a cache store.
type CachePutter interface {
	Put(key string, value interface{}, ttl time.Duration) error
	Forever(key string, value interface{}) error
}

// PrefixKey returns key prefixed with prefix. If prefix is empty, returns key as-is.
// This replaces the identical prefixedKey methods on MemoryStore, RedisStore, and FileStore.
func PrefixKey(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + ":" + key
}

// GetStringFrom retrieves a string value from any CacheGetter.
// This replaces the identical GetString methods on MemoryStore and FileStore.
func GetStringFrom(store CacheGetter, key string) (string, bool) {
	val, found := store.Get(key)
	if !found {
		return "", false
	}
	str, ok := val.(string)
	if !ok {
		return "", false
	}
	return str, true
}

// RememberFrom gets from cache or computes and stores with a TTL.
// This replaces the identical Remember methods on MemoryStore, RedisStore, and FileStore.
func RememberFrom(store CacheGetter, putter CachePutter, key string, ttl time.Duration, callback func() interface{}) (interface{}, error) {
	if val, found := store.Get(key); found {
		return val, nil
	}
	value := callback()
	if err := putter.Put(key, value, ttl); err != nil {
		return nil, err
	}
	return value, nil
}

// RememberForeverFrom gets from cache or computes and stores forever.
// This replaces the identical RememberForever methods on MemoryStore, RedisStore, and FileStore.
func RememberForeverFrom(store CacheGetter, putter CachePutter, key string, callback func() interface{}) (interface{}, error) {
	if val, found := store.Get(key); found {
		return val, nil
	}
	value := callback()
	if err := putter.Forever(key, value); err != nil {
		return nil, err
	}
	return value, nil
}

// HasFrom checks if a key exists in any CacheGetter.
func HasFrom(store CacheGetter, key string) bool {
	_, found := store.Get(key)
	return found
}
