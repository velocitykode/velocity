package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// BenchmarkServeMatchedRoute_Parallel is BenchmarkServeMatchedRoute with
// every P serving at once: the contended cost of the per-request path,
// shared state included.
func BenchmarkServeMatchedRoute_Parallel(b *testing.B) {
	r := New()
	r.Get("/users/{id}/posts/{postID}", func(c *Context) error {
		_ = c.Param("id")
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})
	r.Freeze()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest("GET", "/users/42/posts/7", nil)
		for pb.Next() {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
		}
	})
}
