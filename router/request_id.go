package router

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync/atomic"
	"time"
)

var requestCounter uint64

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

// GetRequestID extracts the request ID from the request context
func GetRequestID(r *http.Request) string {
	if id, ok := r.Context().Value(RequestIDKey).(string); ok {
		return id
	}
	return ""
}

// GetRoutePattern extracts the matched route pattern from the request context
func GetRoutePattern(r *http.Request) string {
	if pattern, ok := r.Context().Value(RoutePatternKey).(string); ok {
		return pattern
	}
	return ""
}
