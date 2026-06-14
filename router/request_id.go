package router

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

var requestCounter uint64

// requestIDContext exposes the lazily-generated request ID under the
// exported RequestIDKey as a string, materializing it on first read.
// This preserves the documented context-key value type (string) for any
// consumer reading RequestIDKey directly, without forcing eager
// generation for requests that never read it.
type requestIDContext struct {
	context.Context
	lazy *lazyRequestID
}

func (c requestIDContext) Value(key any) any {
	if key == RequestIDKey {
		return c.lazy.get()
	}
	return c.Context.Value(key)
}

// lazyRequestID defers request ID generation until the first read. The
// real ID (crypto/rand backed, identical format to eager generation) is
// computed at most once and cached, so repeated reads within a request
// return the same value and requests that never read the ID pay nothing.
type lazyRequestID struct {
	once sync.Once
	id   string
}

// get materializes the request ID on first call and returns the cached
// value thereafter. Safe for concurrent use.
func (l *lazyRequestID) get() string {
	l.once.Do(func() {
		l.id = generateRequestID()
	})
	return l.id
}

// generateRequestID generates a unique request ID
// Format: timestamp-counter-random (e.g., "1706369234-1-a1b2c3d4")
func generateRequestID() string {
	counter := atomic.AddUint64(&requestCounter, 1)
	ts := time.Now().Unix()

	// Generate 4 random bytes for uniqueness
	randomBytes := make([]byte, 4)
	_, _ = rand.Read(randomBytes)

	return hex.EncodeToString([]byte{
		byte(ts >> 24), byte(ts >> 16), byte(ts >> 8), byte(ts),
		byte(counter >> 8), byte(counter),
	}) + hex.EncodeToString(randomBytes)
}

// GetRequestID extracts the request ID from the request context,
// generating it lazily on first read when no consumer forced it earlier.
func GetRequestID(r *http.Request) string {
	switch v := r.Context().Value(RequestIDKey).(type) {
	case *lazyRequestID:
		return v.get()
	case string: // backward compat: an eagerly stored string ID
		return v
	default:
		return ""
	}
}

// GetRoutePattern extracts the matched route pattern from the request
// context. Matched routes carry it in the bundled routeData; the
// RoutePatternKey form is retained for compatibility with any caller
// that sets it directly.
func GetRoutePattern(r *http.Request) string {
	// routeDataContext answers RoutePatternKey from the bundled match; a
	// RoutePatternKey override layered above wins first (last-writer-wins).
	if pattern, ok := r.Context().Value(RoutePatternKey).(string); ok {
		return pattern
	}
	return ""
}
