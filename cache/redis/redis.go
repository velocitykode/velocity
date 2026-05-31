// Package redis is the per-driver leaf that backs Velocity's cache with
// Redis. It lives outside cache/drivers so the cache core never pulls in the
// go-redis client: importing this package (directly or via cache/standard)
// registers the "redis" cache store factory and attaches go-redis only to the
// binaries that ask for it.
//
//	import _ "github.com/velocitykode/velocity/cache/redis"
//
// The store self-registers into cache.Drivers() from init(); use New for a
// standalone store without going through the registry.
package redis

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/velocitykode/velocity/cache/drivers"
	"github.com/velocitykode/velocity/contract"
)

// Conformance assertions: the Redis driver satisfies the contract interfaces.
var (
	_ contract.CacheStore = (*RedisStore)(nil)
	_ contract.CacheLock  = (*RedisLock)(nil)
)

// RedisStore implements a Redis-based cache store
type RedisStore struct {
	client *redis.Client
	prefix string
}

// NewRedisStore creates a new Redis cache store.
// Set tlsEnabled to true to enable TLS connections with a minimum of TLS 1.2.
//
// The caller's ctx is used for the initial Ping so a misconfigured Redis
// fails fast under the caller's deadline instead of hanging on a default
// dial timeout. Pass context.Background only when no caller ctx is
// available (e.g. tests).
//
// Field validation that StoreConfig.Validate previously enforced (host
// non-empty, port positive) lives here so third-party cache drivers can
// register their own factories without StoreConfig.Validate having to know
// about every driver's required fields.
func NewRedisStore(ctx context.Context, prefix string, host string, port int, password string, database int, tlsEnabled bool) (*RedisStore, error) {
	if host == "" {
		return nil, fmt.Errorf("velocity/cache: redis driver requires host")
	}
	if port <= 0 {
		return nil, fmt.Errorf("velocity/cache: redis driver requires positive port")
	}

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

	// Test the connection under the caller's deadline.
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("velocity/cache: failed to connect to redis: %w", err)
	}

	// An empty prefix on Redis is dangerous: Flush would SCAN/DEL every
	// key in the database, including keys owned by other applications
	// sharing the same Redis instance. Warn loudly at startup so the
	// operator notices before they ever call Flush in production.
	if prefix == "" {
		slog.Default().Warn(
			"velocity/cache: redis store configured with empty prefix; Flush is disabled until a prefix is set (see ErrCannotFlushUnprefixed). Set CACHE_PREFIX or per-store Prefix.",
		)
	}

	return &RedisStore{
		client: client,
		prefix: prefix,
	}, nil
}

// Shutdown closes the Redis client connection. The context is accepted
// for interface uniformity with other ShutdownAware types; go-redis's
// Close is synchronous, so the deadline is only consulted when it is
// already cancelled.
func (s *RedisStore) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.client.Close()
}

// prefixedKey returns the key with prefix.
func (s *RedisStore) prefixedKey(key string) string {
	return drivers.PrefixKey(s.prefix, key)
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
//
// Deprecated: use GetCtx with a request-scoped context.Context.
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
//
// Deprecated: use GetStringCtx with a request-scoped context.Context.
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
//
// Deprecated: use PutCtx with a request-scoped context.Context.
func (s *RedisStore) Put(key string, value interface{}, ttl time.Duration) error {
	return s.PutCtx(context.Background(), key, value, ttl)
}

// AddCtx atomically stores a value only if the key does not already exist.
// Uses Redis SET ... NX EX which is atomic on the server. Returns true if
// the key was inserted, false on contention (key already present). The
// caller's ctx is propagated so the SETNX can be cancelled in-flight.
func (s *RedisStore) AddCtx(ctx context.Context, key string, value interface{}, ttl time.Duration) (bool, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return false, fmt.Errorf("velocity/cache: failed to marshal value: %w", err)
	}
	ok, err := s.client.SetNX(ctx, s.prefixedKey(key), data, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("velocity/cache: redis setnx failed: %w", err)
	}
	return ok, nil
}

// Add atomically stores a value only if the key does not already exist.
//
// Deprecated: use AddCtx with a request-scoped context.Context.
func (s *RedisStore) Add(key string, value interface{}, ttl time.Duration) (bool, error) {
	return s.AddCtx(context.Background(), key, value, ttl)
}

// ForeverCtx stores a value in the cache indefinitely using the provided context.
func (s *RedisStore) ForeverCtx(ctx context.Context, key string, value interface{}) error {
	return s.PutCtx(ctx, key, value, 0)
}

// Forever stores a value in the cache indefinitely.
//
// Deprecated: use ForeverCtx with a request-scoped context.Context.
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
//
// Deprecated: use ForgetCtx with a request-scoped context.Context.
func (s *RedisStore) Forget(key string) error {
	return s.ForgetCtx(context.Background(), key)
}

// FlushCtx removes all cache keys matching the configured prefix using the
// provided context. Refuses to operate when the prefix is empty -- a SCAN
// pattern of "*" would iterate every key in the Redis database and the
// subsequent DEL would wipe data owned by every other application sharing
// the instance. Callers that genuinely want that behaviour must call
// FlushAllUnsafeCtx explicitly.
func (s *RedisStore) FlushCtx(ctx context.Context) error {
	if s.prefix == "" {
		return drivers.ErrCannotFlushUnprefixed
	}
	return s.flushPattern(ctx, s.prefix+":*")
}

// Flush removes all cache keys matching the configured prefix.
//
// Deprecated: use FlushCtx with a request-scoped context.Context.
func (s *RedisStore) Flush() error {
	return s.FlushCtx(context.Background())
}

// FlushAllUnsafe is the explicit, opt-in escape hatch that DOES wipe every
// key in the connected Redis database (pattern "*"). It exists so callers
// that genuinely own the entire Redis instance (e.g. per-app dedicated
// Redis, integration tests) can still flush. The name is deliberately
// alarming so a code reviewer cannot mistake it for the bounded Flush.
// Prefer setting a non-empty prefix and using Flush.
func (s *RedisStore) FlushAllUnsafe() error {
	return s.FlushAllUnsafeCtx(context.Background())
}

// FlushAllUnsafeCtx is the ctx-aware variant of FlushAllUnsafe.
func (s *RedisStore) FlushAllUnsafeCtx(ctx context.Context) error {
	return s.flushPattern(ctx, "*")
}

// flushPattern iterates SCAN+DEL for the supplied match pattern. The
// caller is responsible for choosing a safe pattern; this method does no
// guard checking.
func (s *RedisStore) flushPattern(ctx context.Context, pattern string) error {
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

// HasCtx checks if a key exists in the cache using the provided context.
func (s *RedisStore) HasCtx(ctx context.Context, key string) bool {
	result, err := s.client.Exists(ctx, s.prefixedKey(key)).Result()
	if err != nil {
		return false
	}
	return result > 0
}

// Has checks if a key exists in the cache.
//
// Deprecated: use HasCtx with a request-scoped context.Context.
func (s *RedisStore) Has(key string) bool {
	return s.HasCtx(context.Background(), key)
}

// IncrementCtx increments a numeric value using the provided context.
func (s *RedisStore) IncrementCtx(ctx context.Context, key string, value int64) (int64, error) {
	return s.client.IncrBy(ctx, s.prefixedKey(key), value).Result()
}

// Increment increments a numeric value.
//
// Deprecated: use IncrementCtx with a request-scoped context.Context.
func (s *RedisStore) Increment(key string, value int64) (int64, error) {
	return s.IncrementCtx(context.Background(), key, value)
}

// DecrementCtx decrements a numeric value using the provided context.
func (s *RedisStore) DecrementCtx(ctx context.Context, key string, value int64) (int64, error) {
	return s.client.DecrBy(ctx, s.prefixedKey(key), value).Result()
}

// Decrement decrements a numeric value.
//
// Deprecated: use DecrementCtx with a request-scoped context.Context.
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
//
// Deprecated: use ManyCtx with a request-scoped context.Context.
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
//
// Deprecated: use PutManyCtx with a request-scoped context.Context.
func (s *RedisStore) PutMany(items map[string]interface{}, ttl time.Duration) error {
	return s.PutManyCtx(context.Background(), items, ttl)
}

// Remember gets from cache or computes and stores.
func (s *RedisStore) Remember(key string, ttl time.Duration, callback func() interface{}) (interface{}, error) {
	return drivers.RememberFrom(s, s, key, ttl, callback)
}

// RememberForever gets from cache or computes and stores forever.
func (s *RedisStore) RememberForever(key string, callback func() interface{}) (interface{}, error) {
	return drivers.RememberForeverFrom(s, s, key, callback)
}

// GetPrefix returns the cache prefix
func (s *RedisStore) GetPrefix() string {
	return s.prefix
}
