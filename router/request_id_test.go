package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestRequestIDStableAcrossReads proves the lazily generated ID is
// computed at most once and returns the same value on every read within
// a request, even with no event dispatcher wired (the no-consumer path).
func TestRequestIDStableAcrossReads(t *testing.T) {
	r := NewV2()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	_, req = r.beginRequest(req)

	first := GetRequestID(req)
	if first == "" {
		t.Fatal("request ID was empty on first read")
	}
	for i := 0; i < 5; i++ {
		if got := GetRequestID(req); got != first {
			t.Fatalf("request ID changed across reads: first=%s read#%d=%s", first, i, got)
		}
	}
}

// TestRequestIDStableUnderConcurrentReads guards the sync.Once path
// against the race detector: many goroutines resolving the same holder
// must all observe one identical ID.
func TestRequestIDStableUnderConcurrentReads(t *testing.T) {
	r := NewV2()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	_, req = r.beginRequest(req)

	const n = 32
	ids := make([]string, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			ids[idx] = GetRequestID(req)
		}(i)
	}
	wg.Wait()

	for i := 1; i < n; i++ {
		if ids[i] != ids[0] {
			t.Fatalf("concurrent reads disagreed: ids[0]=%s ids[%d]=%s", ids[0], i, ids[i])
		}
	}
	if ids[0] == "" {
		t.Fatal("concurrent reads produced empty ID")
	}
}

// TestRequestIDEagerMatchesContext confirms that when a dispatcher is
// wired the eagerly materialized event ID is identical to what a later
// GetRequestID read returns (same underlying holder).
func TestRequestIDEagerMatchesContext(t *testing.T) {
	r := NewV2()
	r.SetEventDispatcher(func(ctx context.Context, event interface{}) error { return nil })
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	meta, req := r.beginRequest(req)
	if meta.id == "" {
		t.Fatal("ID not materialized eagerly with dispatcher wired")
	}
	if got := GetRequestID(req); got != meta.id {
		t.Fatalf("eager ID %s != context ID %s", meta.id, got)
	}
}

// TestRequestIDFormat verifies the lazy path preserves the original
// timestamp-counter-random hex format (16 hex chars: 12 + 8 ... see
// generateRequestID) so the change is observably identical to consumers.
func TestRequestIDFormat(t *testing.T) {
	id := generateRequestID()
	// 6 bytes (ts+counter) + 4 bytes (random) = 10 bytes => 20 hex chars.
	if len(id) != 20 {
		t.Fatalf("unexpected ID length %d for %q", len(id), id)
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("non-hex char %q in ID %q", c, id)
		}
	}
}

// BenchmarkBeginRequest_NoConsumer measures the request setup cost when
// no event dispatcher is wired and the ID is never read. Allocs/op
// should drop versus eager generation since crypto/rand + hex encoding
// + concat are deferred and never run.
func BenchmarkBeginRequest_NoConsumer(b *testing.B) {
	r := NewV2()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.beginRequest(req)
	}
}

// BenchmarkBeginRequest_WithConsumer measures the setup cost when a
// dispatcher is wired, forcing eager ID materialization (the cost the
// no-consumer path now avoids).
func BenchmarkBeginRequest_WithConsumer(b *testing.B) {
	r := NewV2()
	r.SetEventDispatcher(func(ctx context.Context, event interface{}) error { return nil })
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.beginRequest(req)
	}
}
