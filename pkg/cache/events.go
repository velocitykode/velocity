package cache

import (
	"context"
	"time"
)

// CacheHit is dispatched when a cache lookup finds the key
type CacheHit struct {
	Context context.Context
	Key     string
	Store   string
}

// Name returns the event name
func (e *CacheHit) Name() string {
	return "cache.hit"
}

// CacheMiss is dispatched when a cache lookup does not find the key
type CacheMiss struct {
	Context context.Context
	Key     string
	Store   string
}

// Name returns the event name
func (e *CacheMiss) Name() string {
	return "cache.miss"
}

// CacheWritten is dispatched when a value is written to the cache
type CacheWritten struct {
	Context context.Context
	Key     string
	Store   string
	TTL     time.Duration // 0 means forever
}

// Name returns the event name
func (e *CacheWritten) Name() string {
	return "cache.written"
}

// CacheForgotten is dispatched when a key is removed from the cache
type CacheForgotten struct {
	Context context.Context
	Key     string
	Store   string
}

// Name returns the event name
func (e *CacheForgotten) Name() string {
	return "cache.forgotten"
}

// dispatchCacheHit dispatches a CacheHit event
func dispatchCacheHit(ctx context.Context, key, store string) {
	dispatchEvent(&CacheHit{
		Context: ctx,
		Key:     key,
		Store:   store,
	})
}

// dispatchCacheMiss dispatches a CacheMiss event
func dispatchCacheMiss(ctx context.Context, key, store string) {
	dispatchEvent(&CacheMiss{
		Context: ctx,
		Key:     key,
		Store:   store,
	})
}

// dispatchCacheWritten dispatches a CacheWritten event
func dispatchCacheWritten(ctx context.Context, key, store string, ttl time.Duration) {
	dispatchEvent(&CacheWritten{
		Context: ctx,
		Key:     key,
		Store:   store,
		TTL:     ttl,
	})
}

// dispatchCacheForgotten dispatches a CacheForgotten event
func dispatchCacheForgotten(ctx context.Context, key, store string) {
	dispatchEvent(&CacheForgotten{
		Context: ctx,
		Key:     key,
		Store:   store,
	})
}
