package cache

import (
	"context"
	"encoding/json"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventmeta"
)

// Each cache event records its operation as a span of its own under the
// caller's span (a root span when ctx carries no trace): SpanID is the
// operation's span and ParentID the caller's, per trace.ChildSpanIDs.

// CacheHit is dispatched when a cache lookup finds the key
type CacheHit struct {
	contract.EventMeta
	Key   string
	Store string
}

// Name returns the event name
func (e *CacheHit) Name() string {
	return "cache.hit"
}

// CacheMiss is dispatched when a cache lookup does not find the key
type CacheMiss struct {
	contract.EventMeta
	Key   string
	Store string
}

// Name returns the event name
func (e *CacheMiss) Name() string {
	return "cache.missed"
}

// CacheWritten is dispatched when a value is written to the cache
type CacheWritten struct {
	contract.EventMeta
	Key   string
	Store string
	TTL   time.Duration // 0 means forever
}

// Name returns the event name
func (e *CacheWritten) Name() string {
	return "cache.written"
}

// CacheForgotten is dispatched when a key is removed from the cache
type CacheForgotten struct {
	contract.EventMeta
	Key   string
	Store string
}

// Name returns the event name
func (e *CacheForgotten) Name() string {
	return "cache.forgotten"
}

// CacheOperationFailed is dispatched when a cache operation fails
type CacheOperationFailed struct {
	contract.EventMeta
	Store string
	Op    string // "put", "put_many", "add", "forget", "flush", "increment", "decrement"
	Key   string
	Err   error
}

// Name returns the event name
func (e *CacheOperationFailed) Name() string {
	return "cache.operation.failed"
}

// MarshalJSON encodes the event with Err as its text.
func (e CacheOperationFailed) MarshalJSON() ([]byte, error) {
	type fields CacheOperationFailed
	return json.Marshal(struct {
		fields
		Err string `json:",omitempty"`
	}{fields(e), eventmeta.ErrorText(e.Err)})
}

// UnmarshalJSON decodes the event's JSON form: Err becomes an error with
// the encoded text.
func (e *CacheOperationFailed) UnmarshalJSON(data []byte) error {
	type fields CacheOperationFailed
	v := struct {
		*fields
		Err string `json:",omitempty"`
	}{fields: (*fields)(e)}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	e.Err = eventmeta.TextError(v.Err)
	return nil
}

// The dispatch helpers build their event, span ids included, only when a
// dispatcher is installed.

// dispatchCacheHit dispatches a CacheHit event
func (m *Manager) dispatchCacheHit(ctx context.Context, key, store string) {
	if !m.hasEventDispatcher() {
		return
	}
	m.dispatchEvent(ctx, &CacheHit{EventMeta: eventmeta.Child(ctx), Key: key, Store: store})
}

// dispatchCacheMiss dispatches a CacheMiss event
func (m *Manager) dispatchCacheMiss(ctx context.Context, key, store string) {
	if !m.hasEventDispatcher() {
		return
	}
	m.dispatchEvent(ctx, &CacheMiss{EventMeta: eventmeta.Child(ctx), Key: key, Store: store})
}

// dispatchCacheWritten dispatches a CacheWritten event
func (m *Manager) dispatchCacheWritten(ctx context.Context, key, store string, ttl time.Duration) {
	if !m.hasEventDispatcher() {
		return
	}
	m.dispatchEvent(ctx, &CacheWritten{EventMeta: eventmeta.Child(ctx), Key: key, Store: store, TTL: ttl})
}

// dispatchCacheForgotten dispatches a CacheForgotten event
func (m *Manager) dispatchCacheForgotten(ctx context.Context, key, store string) {
	if !m.hasEventDispatcher() {
		return
	}
	m.dispatchEvent(ctx, &CacheForgotten{EventMeta: eventmeta.Child(ctx), Key: key, Store: store})
}

// dispatchCacheOperationFailed dispatches a CacheOperationFailed event for a
// failed store operation. op is one of the lowercase verbs documented on
// CacheOperationFailed; key is empty for keyless operations (flush).
func (m *Manager) dispatchCacheOperationFailed(ctx context.Context, store, op, key string, opErr error) {
	if !m.hasEventDispatcher() {
		return
	}
	m.dispatchEvent(ctx, &CacheOperationFailed{
		EventMeta: eventmeta.Child(ctx),
		Store:     store,
		Op:        op,
		Key:       key,
		Err:       opErr,
	})
}
