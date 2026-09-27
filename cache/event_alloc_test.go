package cache

import (
	"context"
	"errors"
	"testing"
)

// TestManager_NoDispatcherBuildsNoEvent requires the cache event builders
// to build nothing, span ids included, when no event dispatcher is
// installed.
func TestManager_NoDispatcherBuildsNoEvent(t *testing.T) {
	m := &Manager{}
	ctx := context.Background()
	opErr := errors.New("store down")
	allocs := testing.AllocsPerRun(100, func() {
		m.dispatchCacheHit(ctx, "k", "memory")
		m.dispatchCacheMiss(ctx, "k", "memory")
		m.dispatchCacheWritten(ctx, "k", "memory", 0)
		m.dispatchCacheForgotten(ctx, "k", "memory")
		m.dispatchCacheOperationFailed(ctx, "memory", "put", "k", opErr)
	})
	if allocs != 0 {
		t.Errorf("cache event builders allocated %.0f times with no dispatcher, want 0", allocs)
	}
}
