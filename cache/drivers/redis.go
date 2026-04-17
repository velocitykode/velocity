package drivers

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStore implements a Redis-based cache store
type RedisStore struct {
	client *redis.Client
	prefix string
}

// NewRedisStore creates a new Redis cache store.
// Set tlsEnabled to true to enable TLS connections with a minimum of TLS 1.2.
func NewRedisStore(prefix string, host string, port int, password string, database int, tlsEnabled bool) (*RedisStore, error) {
	opts := &redis.Options{
		Addr:     fmt.Sprintf("%s:%d", host, port),
		Password: password,
		DB:       database,
	}

	// Enable TLS if configured
	if tlsEnabled {
		opts.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}

	client := redis.NewClient(opts)

	// Test the connection
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("velocity/cache: failed to connect to redis: %w", err)
	}

	return &RedisStore{
		client: client,
		prefix: prefix,
	}, nil
}

// Close closes the Redis client connection
func (s *RedisStore) Close() error {
	return s.client.Close()
}

// prefixedKey returns the key with prefix.
func (s *RedisStore) prefixedKey(key string) string {
	return PrefixKey(s.prefix, key)
}

// GetCtx retrieves a value from the cache using the provided context.
func (s *RedisStore) GetCtx(ctx context.Context, key string) (interface{}, bool) {
	data, err := s.client.Get(ctx, s.prefixedKey(key)).Bytes()
	if err != nil {
		return nil, false
	}

	var value interface{}
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, false
	}

	return value, true
}

// Get retrieves a value from the cache.
func (s *RedisStore) Get(key string) (interface{}, bool) {
	return s.GetCtx(context.Background(), key)
}

// GetStringCtx retrieves a string value from the cache using the provided context.
func (s *RedisStore) GetStringCtx(ctx context.Context, key string) (string, bool) {
	val, err := s.client.Get(ctx, s.prefixedKey(key)).Result()
	if err != nil {
		return "", false
	}
	return val, true
}

// GetString retrieves a string value from the cache.
func (s *RedisStore) GetString(key string) (string, bool) {
	return s.GetStringCtx(context.Background(), key)
}

// PutCtx stores a value in the cache with a TTL using the provided context.
func (s *RedisStore) PutCtx(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("velocity/cache: failed to marshal value: %w", err)
	}

	if err := s.client.Set(ctx, s.prefixedKey(key), data, ttl).Err(); err != nil {
		return fmt.Errorf("velocity/cache: redis set failed: %w", err)
	}
	return nil
}

// Put stores a value in the cache with a TTL.
func (s *RedisStore) Put(key string, value interface{}, ttl time.Duration) error {
	return s.PutCtx(context.Background(), key, value, ttl)
}

// ForeverCtx stores a value in the cache indefinitely using the provided context.
func (s *RedisStore) ForeverCtx(ctx context.Context, key string, value interface{}) error {
	return s.PutCtx(ctx, key, value, 0)
}

// Forever stores a value in the cache indefinitely.
func (s *RedisStore) Forever(key string, value interface{}) error {
	return s.ForeverCtx(context.Background(), key, value)
}

// ForgetCtx removes a value from the cache using the provided context.
func (s *RedisStore) ForgetCtx(ctx context.Context, key string) error {
	if err := s.client.Del(ctx, s.prefixedKey(key)).Err(); err != nil {
		return fmt.Errorf("velocity/cache: redis del failed: %w", err)
	}
	return nil
}

// Forget removes a value from the cache.
func (s *RedisStore) Forget(key string) error {
	return s.ForgetCtx(context.Background(), key)
}

// FlushCtx removes all cache keys matching the configured prefix using the provided context.
// Uses SCAN + DEL to avoid destroying the entire Redis database.
func (s *RedisStore) FlushCtx(ctx context.Context) error {
	pattern := s.prefix + ":*"
	if s.prefix == "" {
		pattern = "*"
	}

	var cursor uint64
	for {
		keys, nextCursor, err := s.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return fmt.Errorf("velocity/cache: failed to scan cache keys: %w", err)
		}
		if len(keys) > 0 {
			if err := s.client.Del(ctx, keys...).Err(); err != nil {
				return fmt.Errorf("velocity/cache: failed to delete cache keys: %w", err)
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return nil
}

// Flush removes all cache keys matching the configured prefix.
func (s *RedisStore) Flush() error {
	return s.FlushCtx(context.Background())
}

// HasCtx checks if a key exists in the cache using the provided context.
func (s *RedisStore) HasCtx(ctx context.Context, key string) bool {
	result, err := s.client.Exists(ctx, s.prefixedKey(key)).Result()
	if err != nil {
		return false
	}
	return result > 0
}

// Has checks if a key exists in the cache.
func (s *RedisStore) Has(key string) bool {
	return s.HasCtx(context.Background(), key)
}

// IncrementCtx increments a numeric value using the provided context.
func (s *RedisStore) IncrementCtx(ctx context.Context, key string, value int64) (int64, error) {
	return s.client.IncrBy(ctx, s.prefixedKey(key), value).Result()
}

// Increment increments a numeric value.
func (s *RedisStore) Increment(key string, value int64) (int64, error) {
	return s.IncrementCtx(context.Background(), key, value)
}

// DecrementCtx decrements a numeric value using the provided context.
func (s *RedisStore) DecrementCtx(ctx context.Context, key string, value int64) (int64, error) {
	return s.client.DecrBy(ctx, s.prefixedKey(key), value).Result()
}

// Decrement decrements a numeric value.
func (s *RedisStore) Decrement(key string, value int64) (int64, error) {
	return s.DecrementCtx(context.Background(), key, value)
}

// ManyCtx retrieves multiple values using the provided context.
func (s *RedisStore) ManyCtx(ctx context.Context, keys []string) map[string]interface{} {
	result := make(map[string]interface{})

	if len(keys) == 0 {
		return result
	}

	// Build prefixed keys
	prefixedKeys := make([]string, len(keys))
	for i, key := range keys {
		prefixedKeys[i] = s.prefixedKey(key)
	}

	values, err := s.client.MGet(ctx, prefixedKeys...).Result()
	if err != nil {
		return result
	}

	for i, val := range values {
		if val == nil {
			continue
		}

		strVal, ok := val.(string)
		if !ok {
			continue
		}

		var decoded interface{}
		if err := json.Unmarshal([]byte(strVal), &decoded); err != nil {
			continue
		}

		result[keys[i]] = decoded
	}

	return result
}

// Many retrieves multiple values.
func (s *RedisStore) Many(keys []string) map[string]interface{} {
	return s.ManyCtx(context.Background(), keys)
}

// PutManyCtx stores multiple values using a pipeline and the provided context.
func (s *RedisStore) PutManyCtx(ctx context.Context, items map[string]interface{}, ttl time.Duration) error {
	pipe := s.client.Pipeline()

	for key, value := range items {
		data, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("velocity/cache: failed to marshal value for key %s: %w", key, err)
		}
		pipe.Set(ctx, s.prefixedKey(key), data, ttl)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("velocity/cache: redis pipeline exec failed: %w", err)
	}
	return nil
}

// PutMany stores multiple values using a pipeline.
func (s *RedisStore) PutMany(items map[string]interface{}, ttl time.Duration) error {
	return s.PutManyCtx(context.Background(), items, ttl)
}

// Remember gets from cache or computes and stores.
func (s *RedisStore) Remember(key string, ttl time.Duration, callback func() interface{}) (interface{}, error) {
	return RememberFrom(s, s, key, ttl, callback)
}

// RememberForever gets from cache or computes and stores forever.
func (s *RedisStore) RememberForever(key string, callback func() interface{}) (interface{}, error) {
	return RememberForeverFrom(s, s, key, callback)
}

// GetPrefix returns the cache prefix
func (s *RedisStore) GetPrefix() string {
	return s.prefix
}
