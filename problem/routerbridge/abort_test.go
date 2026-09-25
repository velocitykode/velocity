package routerbridge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
)

// abortObserver records what the router boundary did with one request:
// pipeline reports, RequestFailed and RequestHandled events, and the
// standalone router's error-level lines.
type abortObserver struct {
	mu      sync.Mutex
	reports int
	failed  []bool // RequestFailed Recovered flags
	handled int
	logs    int
}

func (o *abortObserver) router(pipeline bool) *router.VelocityRouterV2 {
	r := router.New()
	r.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		o.mu.Lock()
		defer o.mu.Unlock()
		switch ev := event.(type) {
		case *router.RequestFailed:
			o.failed = append(o.failed, ev.Recovered)
		case *router.RequestHandled:
			o.handled++
		}
		return nil
	})
	if pipeline {
		h := problem.NewHandler(problem.WithReporters(problem.NewCallbackReporter(func(error, *problem.ErrorContext) {
			o.mu.Lock()
			o.reports++
			o.mu.Unlock()
		})))
		h.SetDebug(false)
		Install(r, WithHandler(func() contract.ErrorHandler { return h }))
	} else {
		r.SetErrorLogger(func(string, ...any) { o.mu.Lock(); o.logs++; o.mu.Unlock() })
	}
	return r
}

// onPath is a global middleware running fn first for requests to path, so
// the unmatched and static chains reach it too.
func onPath(path string, fn func(c *router.Context)) router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			if c.Request.URL.Path == path {
				fn(c)
			}
			return next(c)
		}
	}
}

// TestRouter_AbortHandlerPanicAbortsTheConnection asserts a
// panic(http.ErrAbortHandler) on a matched route, on the unmatched and
// static paths, under Timeout, and from a pre-commit hook the router fires
// at finalize, is net/http's abort and not a bug: on a real server the
// client sees the connection cut (no response, or a body ending in an
// unexpected EOF), nothing is reported or logged, no RequestFailed fires,
// RequestHandled still fires once, and the server keeps serving. A
// normal panic on the same route still reports and answers 500.
func TestRouter_AbortHandlerPanicAbortsTheConnection(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.css"), []byte("body{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	abortMidBody := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "partial-")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		panic(http.ErrAbortHandler)
	}
	tests := []struct {
		name  string
		path  string
		setup func(r *router.VelocityRouterV2)
	}{
		{
			name: "matched before any write",
			path: "/x",
			setup: func(r *router.VelocityRouterV2) {
				r.Get("/x", func(*router.Context) error { panic(http.ErrAbortHandler) })
			},
		},
		{
			name: "matched mid body",
			path: "/x",
			setup: func(r *router.VelocityRouterV2) {
				r.Get("/x", func(c *router.Context) error { abortMidBody(c.Response); return nil })
			},
		},
		{
			name: "unmatched",
			path: "/nowhere",
			setup: func(r *router.VelocityRouterV2) {
				r.Use(onPath("/nowhere", func(*router.Context) { panic(http.ErrAbortHandler) }))
				r.Get("/x", func(*router.Context) error { return nil })
			},
		},
		{
			name: "static mid body",
			path: "/app.css",
			setup: func(r *router.VelocityRouterV2) {
				r.Static(dir)
				r.Use(onPath("/app.css", func(c *router.Context) { abortMidBody(c.Response) }))
			},
		},
		{
			name: "under timeout",
			path: "/x",
			setup: func(r *router.VelocityRouterV2) {
				r.Use(router.Timeout(5 * time.Second))
				r.Get("/x", func(c *router.Context) error { abortMidBody(c.Response); return nil })
			},
		},
		{
			name: "pre-commit hook at finalize",
			path: "/x",
			setup: func(r *router.VelocityRouterV2) {
				r.Use(onPath("/x", func(c *router.Context) {
					if hk, ok := c.Response.(interface{ BeforeFirstWrite(func()) }); ok {
						hk.BeforeFirstWrite(func() { panic(http.ErrAbortHandler) })
					}
				}))
				r.Get("/x", func(*router.Context) error { return nil })
			},
		},
	}
	for _, tt := range tests {
		for _, pipeline := range []bool{true, false} {
			name := tt.name + "/standalone"
			if pipeline {
				name = tt.name + "/pipeline"
			}
			t.Run(name, func(t *testing.T) {
				o := &abortObserver{}
				r := o.router(pipeline)
				r.Get("/boom", func(*router.Context) error { panic("a bug") })
				tt.setup(r)
				srv := httptest.NewServer(r)

				resp, getErr := srv.Client().Get(srv.URL + tt.path)
				var readErr error
				if getErr == nil {
					_, readErr = io.ReadAll(resp.Body)
					_ = resp.Body.Close()
				}
				if getErr == nil && readErr == nil {
					t.Errorf("client read a complete response (status %d) from an aborted handler, want the connection cut", resp.StatusCode)
				}
				if getErr == nil && readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
					t.Errorf("body read error = %v, want io.ErrUnexpectedEOF", readErr)
				}

				// The server survived the abort: a normal panic on the
				// same server still reports and answers 500.
				boom, err := srv.Client().Get(srv.URL + "/boom")
				if err != nil {
					t.Fatalf("GET /boom after the abort: %v", err)
				}
				_, _ = io.Copy(io.Discard, boom.Body)
				_ = boom.Body.Close()
				srv.Close()
				if boom.StatusCode != http.StatusInternalServerError {
					t.Errorf("normal panic status = %d, want 500", boom.StatusCode)
				}

				o.mu.Lock()
				defer o.mu.Unlock()
				// Everything below counts the abort and the one normal
				// panic together.
				if pipeline && o.reports != 1 {
					t.Errorf("pipeline reports = %d, want 1 (the normal panic only)", o.reports)
				}
				if !pipeline && o.logs != 1 {
					t.Errorf("standalone error lines = %d, want 1 (the normal panic only)", o.logs)
				}
				if len(o.failed) != 1 || !o.failed[0] {
					t.Errorf("RequestFailed Recovered flags = %v, want [true] (the normal panic only)", o.failed)
				}
				if o.handled != 2 {
					t.Errorf("RequestHandled fired %d times, want 2 (the abort and the normal panic)", o.handled)
				}
			})
		}
	}
}
