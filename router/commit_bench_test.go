package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// BenchmarkCommit measures one request through ServeHTTP for each way a
// response is committed (explicit status, miss answered by the boundary,
// streamed with Flush, empty, a custom error handler answering 401), with
// zero, one and two pre-commit listeners registered by a middleware. The
// zero-listener rows are the guard: a request that registers nothing pays
// no allocation for the registry.
func BenchmarkCommit(b *testing.B) {
	errPlain := errors.New("denied")
	shapes := []struct {
		name    string
		path    string
		handler HandlerFunc
		setup   func(r *VelocityRouterV2)
	}{
		{name: "static", path: "/ping", handler: func(c *Context) error {
			c.Response.WriteHeader(http.StatusOK)
			return nil
		}},
		{name: "dynamic", path: "/users/42/posts/7", handler: func(c *Context) error {
			_ = c.Param("id")
			c.Response.WriteHeader(http.StatusOK)
			return nil
		}},
		{name: "miss", path: "/nope"},
		{name: "stream", path: "/ping", handler: func(c *Context) error {
			c.Response.(http.Flusher).Flush()
			_, _ = c.Response.Write([]byte("a"))
			c.Response.(http.Flusher).Flush()
			return nil
		}},
		{name: "empty", path: "/ping", handler: func(c *Context) error { return nil }},
		{name: "errorhandler401", path: "/ping", handler: func(c *Context) error { return errPlain },
			setup: func(r *VelocityRouterV2) {
				r.SetErrorHandler(func(c *Context, err error, info ErrorInfo) {
					c.Response.WriteHeader(http.StatusUnauthorized)
				})
			}},
	}
	for _, s := range shapes {
		for listeners := 0; listeners <= 2; listeners++ {
			b.Run(s.name+"/listeners="+strconv.Itoa(listeners), func(b *testing.B) {
				r := New()
				if s.setup != nil {
					s.setup(r)
				}
				n := listeners
				// One middleware in every row, so the rows differ only in
				// what it registers.
				r.Use(func(next HandlerFunc) HandlerFunc {
					return func(c *Context) error {
						benchRegisterListeners(c, n)
						return next(c)
					}
				})
				if s.handler != nil {
					r.Get("/ping", s.handler)
					r.Get("/users/{id}/posts/{postID}", s.handler)
				}
				r.Freeze()
				req := httptest.NewRequest(http.MethodGet, s.path, nil)
				b.ReportAllocs()
				for b.Loop() {
					r.ServeHTTP(httptest.NewRecorder(), req)
				}
			})
		}
	}
}
