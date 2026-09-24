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

func returnCtxErr(c *router.Context) error { return c.Request.Context().Err() }

// cancelledWith returns a context derived from parent, already cancelled
// with cause.
func cancelledWith(parent context.Context, cause error) context.Context {
	ctx, cancel := context.WithCancelCause(parent)
	cancel(cause)
	return ctx
}
