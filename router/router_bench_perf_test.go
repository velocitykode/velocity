package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// BenchmarkServeMatchedRoute drives the full ServeHTTP path for a matched
// parameterized route. It is the allocs/op guard for the router polish
// bundle: lazy param map (R3), single bundled context clone (R4), pooled
// responseWriter (R5), and pooled path-parts buffer (R6) should all keep
// per-request allocations low. No event dispatcher is wired, so the param
// map must not be materialized.
func BenchmarkServeMatchedRoute(b *testing.B) {
	r := New()
	r.Get("/users/{id}/posts/{postID}", func(c *Context) error {
		// Touch a param via the slice path (c.Param), the supported hot
		// path, which must not force the map form.
		_ = c.Param("id")
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})
	r.Freeze()

	req := httptest.NewRequest("GET", "/users/42/posts/7", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}
}

// BenchmarkTreeMatch isolates the radix-tree walk for a dynamic route,
// exercising the index-based path split (R6) and the snapshot build
// without the surrounding HTTP machinery.
func BenchmarkTreeMatch(b *testing.B) {
	tree := NewTree()
	if err := tree.Insert("GET", "/users/{id}/posts/{postID}", dummyHandler); err != nil {
		b.Fatalf("insert: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if m := tree.Match("GET", "/users/42/posts/7"); m == nil {
			b.Fatal("expected match")
		}
	}
}
