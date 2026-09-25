package routerbridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
)

// TestInstall_FinalizeHookPanicIsReported asserts a BeforeFirstWrite hook
// that panics when the router fires it after the boundary (nothing wrote a
// response: a handler returning nil, a client-gone cancel answered with
// nothing, an unmatched request whose middleware answered nothing) reaches
// the installed pipeline as a recovered panic: a 500, exactly one report
// with Recovered set, one RequestFailed with Recovered set, RequestHandled
// with 500, and no line from the router's own error logger.
func TestInstall_FinalizeHookPanicIsReported(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		handler router.HandlerFunc
		ctx     func() context.Context
	}{
		{
			name:    "handler returns nil",
			path:    "/x",
			handler: func(*router.Context) error { return nil },
			ctx:     context.Background,
		},
		{
			name:    "client gone cancel",
			path:    "/x",
			handler: returnCtxErr,
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
		},
		{
			name: "unmatched middleware answers nothing",
			path: "/nowhere",
			ctx:  context.Background,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				mu        sync.Mutex
				recovered []bool
				failed    []bool
				handled   []int
				routerLog int
			)
			h := problem.NewHandler(
				problem.WithHandlerLogger(&errLineLogger{}),
				problem.WithReporters(problem.NewCallbackReporter(func(_ error, ctx *problem.ErrorContext) {
					mu.Lock()
					recovered = append(recovered, ctx.Recovered)
					mu.Unlock()
				})),
			)
			h.SetDebug(false)
			r := router.New()
			r.SetErrorLogger(func(string, ...any) { mu.Lock(); routerLog++; mu.Unlock() })
			Install(r, WithHandler(func() contract.ErrorHandler { return h }))
			r.SetEventDispatcher(func(_ context.Context, event interface{}) error {
				mu.Lock()
				defer mu.Unlock()
				switch ev := event.(type) {
				case *router.RequestFailed:
					failed = append(failed, ev.Recovered)
				case *router.RequestHandled:
					handled = append(handled, ev.StatusCode)
				}
				return nil
			})
			r.Use(func(next router.HandlerFunc) router.HandlerFunc {
				return func(c *router.Context) error {
					if hk, ok := c.Response.(interface{ BeforeFirstWrite(func()) }); ok {
						hk.BeforeFirstWrite(func() { panic("hook exploded") })
					}
					if tt.handler == nil {
						return nil
					}
					return next(c)
				}
			})
			if tt.handler != nil {
				r.Get("/x", tt.handler)
			}

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.path, nil).WithContext(tt.ctx()))

			if w.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", w.Code)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(recovered) != 1 || !recovered[0] {
				t.Errorf("reports (Recovered flags) = %v, want exactly one recovered report", recovered)
			}
			recoveredFailures := 0
			for _, rec := range failed {
				if rec {
					recoveredFailures++
				}
			}
			if recoveredFailures != 1 {
				t.Errorf("RequestFailed Recovered flags = %v, want exactly one recovered", failed)
			}
			if len(handled) != 1 || handled[0] != http.StatusInternalServerError {
				t.Errorf("RequestHandled statuses = %v, want [500]", handled)
			}
			if routerLog != 0 {
				t.Errorf("router error log entries = %d with the pipeline installed, want 0", routerLog)
			}
		})
	}
}
