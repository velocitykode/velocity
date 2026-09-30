package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// eventsBenchWriter is a ResponseWriter that allocates nothing per request.
type eventsBenchWriter struct{ header http.Header }

func (w *eventsBenchWriter) Header() http.Header         { return w.header }
func (w *eventsBenchWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *eventsBenchWriter) WriteHeader(int)             {}

// BenchmarkServeHTTP_Events measures a matched and an unmatched request
// with and without an event dispatcher: with none, no request event is
// built.
func BenchmarkServeHTTP_Events(b *testing.B) {
	for _, withDisp := range []bool{false, true} {
		for _, path := range []string{"/users/42", "/nowhere"} {
			name := "none" + path
			if withDisp {
				name = "dispatcher" + path
			}
			b.Run(name, func(b *testing.B) {
				r := New()
				r.Get("/users/{id}", func(c *Context) error { c.Response.WriteHeader(http.StatusOK); return nil })
				if withDisp {
					r.SetEventDispatcher(func(context.Context, interface{}) error { return nil })
				}
				r.Freeze()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				w := &eventsBenchWriter{header: http.Header{}}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					clear(w.header)
					r.ServeHTTP(w, req)
				}
			})
		}
	}
}

// BenchmarkServeHTTP_EventsParallel is BenchmarkServeHTTP_Events for the
// matched request, served in parallel.
func BenchmarkServeHTTP_EventsParallel(b *testing.B) {
	for _, withDisp := range []bool{false, true} {
		name := "none"
		if withDisp {
			name = "dispatcher"
		}
		b.Run(name, func(b *testing.B) {
			r := New()
			r.Get("/users/{id}", func(c *Context) error { c.Response.WriteHeader(http.StatusOK); return nil })
			if withDisp {
				r.SetEventDispatcher(func(context.Context, interface{}) error { return nil })
			}
			r.Freeze()
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				req := httptest.NewRequest(http.MethodGet, "/users/42", nil)
				w := &eventsBenchWriter{header: http.Header{}}
				for pb.Next() {
					clear(w.header)
					r.ServeHTTP(w, req)
				}
			})
		})
	}
}
