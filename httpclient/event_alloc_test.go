package httpclient

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestClient_NoDispatcherBuildsNoEvent requires the request event builders
// to build nothing when no event dispatcher is installed.
func TestClient_NoDispatcherBuildsNoEvent(t *testing.T) {
	c := New()
	ctx := context.Background()
	reqErr := errors.New("connection refused")
	allocs := testing.AllocsPerRun(100, func() {
		c.dispatchRequestSent(ctx, "GET", "https://example.test/", 200, time.Millisecond, 0, 0)
		c.dispatchRequestFailed(ctx, "GET", "https://example.test/", reqErr, time.Millisecond)
	})
	if allocs != 0 {
		t.Errorf("request event builders allocated %.0f times with no dispatcher, want 0", allocs)
	}
}
