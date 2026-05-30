package redis

import (
	"context"

	"github.com/velocitykode/velocity/cache"
)

// Verify *RedisStore implements cache.ContextStore at compile time so the
// cache Manager's optional-interface assertion picks it up for ctx-aware
// operations. This assertion lives here (not in cache core) so cache never
// references the go-redis-backed concrete type.
var _ cache.ContextStore = (*RedisStore)(nil)

// init registers the redis cache store into the canonical cache registry.
// The redis driver lives in this leaf package so the cache root never pulls
// in the go-redis dependency; importing this package (directly or via
// cache/standard) wires the "redis" factory.
func init() {
	cache.Drivers().Register(cache.DriverRedis, func(ctx context.Context, cfg cache.StoreConfig) (cache.Store, error) {
		return New(ctx, cfg)
	})
}

// New constructs a Redis cache store from the resolved StoreConfig for
// standalone use without going through the cache driver registry. It returns
// the same store the registry path produces, so both routes are equivalent.
func New(ctx context.Context, cfg cache.StoreConfig) (cache.Store, error) {
	return NewRedisStore(ctx, cfg.Prefix, cfg.Host, cfg.Port, cfg.Password, cfg.Database, cfg.TLS)
}
