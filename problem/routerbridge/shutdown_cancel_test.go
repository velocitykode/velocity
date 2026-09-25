package routerbridge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
)

// warnCounter is a contract.Logger counting warn and error lines.
type warnCounter struct {
	mu          sync.Mutex
	warn, error int
}

func (l *warnCounter) Debug(string, ...any) {}
func (l *warnCounter) Info(string, ...any)  {}
func (l *warnCounter) Warn(string, ...any)  { l.mu.Lock(); l.warn++; l.mu.Unlock() }
func (l *warnCounter) Error(string, ...any) { l.mu.Lock(); l.error++; l.mu.Unlock() }
func (l *warnCounter) Fatal(string, ...any) { l.mu.Lock(); l.error++; l.mu.Unlock() }

func (l *warnCounter) counts() (int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.warn, l.error
}

// TestInstall_ServerShutdownCancel asserts the standalone router and the
// pipeline answer a request context the server cancelled while shutting
// down (cause contract.ErrServerShuttingDown) with 503, Retry-After: 1 and
// Connection: close, log it at warn and report nothing, while a context
// done for any other cause (the client went away) gets nothing written,
// logged or reported.
func TestInstall_ServerShutdownCancel(t *testing.T) {
	tests := []struct {
		name     string
		ctx      func() context.Context
		handler  router.HandlerFunc
		accept   string
		wantCode int // 0: nothing written
	}{
		{
			name:     "server shutdown",
			ctx:      func() context.Context { return cancelledWith(context.Background(), contract.ErrServerShuttingDown) },
			handler:  returnCtxErr,
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name:     "server shutdown json",
			ctx:      func() context.Context { return cancelledWith(context.Background(), contract.ErrServerShuttingDown) },
			handler:  returnCtxErr,
			accept:   "application/json",
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name: "server shutdown through a child context",
			ctx: func() context.Context {
				parent, cancel := context.WithCancelCause(context.Background())
				child, stop := context.WithCancel(parent)
				_ = stop
				cancel(contract.ErrServerShuttingDown)
				return child
			},
			handler:  returnCtxErr,
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name: "server shutdown under an explicit 503",
			ctx:  func() context.Context { return cancelledWith(context.Background(), contract.ErrServerShuttingDown) },
			handler: func(c *router.Context) error {
				return contract.NewHTTPError(http.StatusServiceUnavailable).WithHeader("Retry-After", "30").WithCause(c.Request.Context().Err())
			},
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name: "client gone",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			handler: returnCtxErr,
		},
		{
			name:    "client gone with another cause",
			ctx:     func() context.Context { return cancelledWith(context.Background(), context.Canceled) },
			handler: returnCtxErr,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var warns, errs int
			var mu sync.Mutex
			standalone := router.New()
			standalone.SetWarnLogger(func(string, ...any) { mu.Lock(); warns++; mu.Unlock() })
			standalone.SetErrorLogger(func(string, ...any) { mu.Lock(); errs++; mu.Unlock() })
			standalone.Get("/x", tt.handler)

			rec := &recordingReporter{}
			logger := &warnCounter{}
			h := problem.NewHandler(problem.WithReporters(rec), problem.WithHandlerLogger(logger))
			h.SetDebug(false)
			installed := router.New()
			Install(installed, WithHandler(func() contract.ErrorHandler { return h }))
			installed.Get("/x", tt.handler)

			for name, r := range map[string]*router.VelocityRouterV2{"standalone": standalone, "pipeline": installed} {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(tt.ctx())
				if tt.accept != "" {
					req.Header.Set("Accept", tt.accept)
				}
				r.ServeHTTP(w, req)
				if tt.wantCode == 0 {
					if w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
						t.Errorf("%s: wrote %d %q, want nothing", name, w.Code, w.Body.String())
					}
					continue
				}
				if w.Code != tt.wantCode {
					t.Errorf("%s: status = %d, want %d", name, w.Code, tt.wantCode)
				}
				if got := w.Header().Get("Retry-After"); got != "1" {
					t.Errorf("%s: Retry-After = %q, want 1", name, got)
				}
				if got := w.Header().Get("Connection"); got != "close" {
					t.Errorf("%s: Connection = %q, want close", name, got)
				}
				if tt.accept == "application/json" && w.Header().Get("Content-Type") != "application/problem+json" {
					t.Errorf("%s: Content-Type = %q, want problem+json", name, w.Header().Get("Content-Type"))
				}
			}

			wantWarn := 0
			if tt.wantCode != 0 {
				wantWarn = 1
			}
			mu.Lock()
			if warns != wantWarn || errs != 0 {
				t.Errorf("standalone logged %d warn and %d error lines, want %d and 0", warns, errs, wantWarn)
			}
			mu.Unlock()
			if pw, pe := logger.counts(); pw != wantWarn || pe != 0 {
				t.Errorf("pipeline logged %d warn and %d error lines, want %d and 0", pw, pe, wantWarn)
			}
			if rec.count() != 0 {
				t.Errorf("pipeline reports = %d, want 0", rec.count())
			}
		})
	}
}

// TestInstall_ServerShutdownCancelAfterTimeout asserts the shutdown answer
// survives router.Timeout's handoff: the handler finishes in time, then the
// server cancels the request context with contract.ErrServerShuttingDown
// while outer middleware is still running, and that middleware returns the
// context's error. The standalone router and the pipeline answer 503 with
// Retry-After: 1 and log one warn line; the same chain cancelled without
// that cause (the client went away) gets nothing written or logged.
func TestInstall_ServerShutdownCancelAfterTimeout(t *testing.T) {
	tests := []struct {
		name     string
		pipeline bool
		cause    error
		accept   string
		wantCode int // 0: nothing written
	}{
		{name: "standalone shutdown", cause: contract.ErrServerShuttingDown, wantCode: http.StatusServiceUnavailable},
		{name: "standalone shutdown json", cause: contract.ErrServerShuttingDown, accept: "application/json", wantCode: http.StatusServiceUnavailable},
		{name: "standalone client gone", cause: context.Canceled},
		{name: "pipeline shutdown", pipeline: true, cause: contract.ErrServerShuttingDown, wantCode: http.StatusServiceUnavailable},
		{name: "pipeline shutdown json", pipeline: true, cause: contract.ErrServerShuttingDown, accept: "application/json", wantCode: http.StatusServiceUnavailable},
		{name: "pipeline client gone", pipeline: true, cause: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var warns, errs int
			logger := &warnCounter{}
			rec := &recordingReporter{}
			r := router.New()
			if tt.pipeline {
				h := problem.NewHandler(problem.WithReporters(rec), problem.WithHandlerLogger(logger))
				h.SetDebug(false)
				Install(r, WithHandler(func() contract.ErrorHandler { return h }))
			} else {
				r.SetWarnLogger(func(string, ...any) { mu.Lock(); warns++; mu.Unlock() })
				r.SetErrorLogger(func(string, ...any) { mu.Lock(); errs++; mu.Unlock() })
			}

			parent, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			var cause error
			r.Use(func(next router.HandlerFunc) router.HandlerFunc {
				return func(c *router.Context) error {
					if err := next(c); err != nil {
						return err
					}
					cancel(tt.cause)
					cause = context.Cause(c.Request.Context())
					return c.Request.Context().Err()
				}
			})
			r.Use(router.Timeout(time.Minute))
			r.Get("/x", func(*router.Context) error { return nil })

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(parent)
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}
			r.ServeHTTP(w, req)

			if !errors.Is(cause, tt.cause) {
				t.Errorf("context.Cause after the handoff = %v, want %v", cause, tt.cause)
			}
			wantWarn := 0
			if tt.wantCode == 0 {
				if w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
					t.Errorf("wrote %d %q, want nothing", w.Code, w.Body.String())
				}
			} else {
				wantWarn = 1
				if w.Code != tt.wantCode {
					t.Errorf("status = %d, want %d", w.Code, tt.wantCode)
				}
				if got := w.Header().Get("Retry-After"); got != "1" {
					t.Errorf("Retry-After = %q, want 1", got)
				}
				if got := w.Header().Get("Connection"); got != "close" {
					t.Errorf("Connection = %q, want close", got)
				}
				if tt.accept == "application/json" && w.Header().Get("Content-Type") != "application/problem+json" {
					t.Errorf("Content-Type = %q, want problem+json", w.Header().Get("Content-Type"))
				}
			}
			gotWarn, gotErr := logger.counts()
			if !tt.pipeline {
				mu.Lock()
				gotWarn, gotErr = warns, errs
				mu.Unlock()
			}
			if gotWarn != wantWarn || gotErr != 0 {
				t.Errorf("logged %d warn and %d error lines, want %d and 0", gotWarn, gotErr, wantWarn)
			}
			if rec.count() != 0 {
				t.Errorf("reports = %d, want 0", rec.count())
			}
		})
	}
}

func returnCtxErr(c *router.Context) error { return c.Request.Context().Err() }

// cancelledWith returns a context derived from parent, already cancelled
// with cause.
func cancelledWith(parent context.Context, cause error) context.Context {
	ctx, cancel := context.WithCancelCause(parent)
	cancel(cause)
	return ctx
}
