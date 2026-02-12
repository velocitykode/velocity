package cache

import (
	"context"
	"time"

	"github.com/velocitykode/velocity/pkg/trace"
)

// CacheHit is dispatched when a cache lookup finds the key
type CacheHit struct {
	Context  context.Context
	Key      string
	Store    string
	TraceID  string // APM trace ID
	SpanID   string // APM span ID
	ParentID string // Parent span ID for correlation
}

// Name returns the event name
func (e *CacheHit) Name() string {
	return "cache.hit"
}

// CacheMiss is dispatched when a cache lookup does not find the key
type CacheMiss struct {
	Context  context.Context
	Key      string
	Store    string
	TraceID  string // APM trace ID
	SpanID   string // APM span ID
	ParentID string // Parent span ID for correlation
}

// Name returns the event name
func (e *CacheMiss) Name() string {
	return "cache.miss"
}

// CacheWritten is dispatched when a value is written to the cache
type CacheWritten struct {
	Context  context.Context
	Key      string
	Store    string
	TTL      time.Duration // 0 means forever
	TraceID  string        // APM trace ID
	SpanID   string        // APM span ID
	ParentID string        // Parent span ID for correlation
}

// Name returns the event name
func (e *CacheWritten) Name() string {
	return "cache.written"
}

// CacheForgotten is dispatched when a key is removed from the cache
type CacheForgotten struct {
	Context  context.Context
	Key      string
	Store    string
	TraceID  string // APM trace ID
	SpanID   string // APM span ID
	ParentID string // Parent span ID for correlation
}

// Name returns the event name
func (e *CacheForgotten) Name() string {
	return "cache.forgotten"
}

// dispatchCacheHit dispatches a CacheHit event
func (m *Manager) dispatchCacheHit(ctx context.Context, key, store string) {
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	m.dispatchEvent(&CacheHit{
		Context:  ctx,
		Key:      key,
		Store:    store,
		TraceID:  traceID,
		SpanID:   spanID,
		ParentID: parentID,
	})
}

// dispatchCacheMiss dispatches a CacheMiss event
func (m *Manager) dispatchCacheMiss(ctx context.Context, key, store string) {
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	m.dispatchEvent(&CacheMiss{
		Context:  ctx,
		Key:      key,
		Store:    store,
		TraceID:  traceID,
		SpanID:   spanID,
		ParentID: parentID,
	})
}

// dispatchCacheWritten dispatches a CacheWritten event
func (m *Manager) dispatchCacheWritten(ctx context.Context, key, store string, ttl time.Duration) {
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	m.dispatchEvent(&CacheWritten{
		Context:  ctx,
		Key:      key,
		Store:    store,
		TTL:      ttl,
		TraceID:  traceID,
		SpanID:   spanID,
		ParentID: parentID,
	})
}

// dispatchCacheForgotten dispatches a CacheForgotten event
func (m *Manager) dispatchCacheForgotten(ctx context.Context, key, store string) {
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	m.dispatchEvent(&CacheForgotten{
		Context:  ctx,
		Key:      key,
		Store:    store,
		TraceID:  traceID,
		SpanID:   spanID,
		ParentID: parentID,
	})
}
