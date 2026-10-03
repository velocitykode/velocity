package router_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/drain/draintest"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/router"
)

// holdRequest serves one request on r whose handler blocks until the
// returned release is called, and returns once the handler runs.
func holdRequest(t *testing.T, r *router.VelocityRouterV2, path string) (release func()) {
	t.Helper()
	code := hostile.New(t, hostile.Block, nil)
	r.Get(path, func(c *router.Context) error {
		code.Run()
		return c.NoContent()
	})
	done := make(chan struct{})
	go func() { //safe-goroutine: the held request; released by the returned func
		defer close(done)
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}()
	code.AwaitEntered(t)
	var once sync.Once
	return func() { once.Do(func() { code.Release(); <-done }) }
}

// TestRouter_DrainContract runs the drain owner contract against the
// router's request run.
func TestRouter_DrainContract(t *testing.T) {
	draintest.Run(t, func(t *testing.T) draintest.Owner {
		r := router.New()
		r.Get("/probe", func(c *router.Context) error { return c.NoContent() })
		var stopFromHandler error
		r.Get("/stop", func(c *router.Context) error {
			stopFromHandler = r.Shutdown(context.Background())
			return c.NoContent()
		})
		return draintest.Owner{
			Hold: func(t *testing.T) func() { return holdRequest(t, r, "/held") },
			Stop: r.Shutdown,
			Refused: func(t *testing.T) bool {
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))
				return w.Code == http.StatusServiceUnavailable
			},
			StopFromOwnWork: func(t *testing.T) error {
				r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/stop", nil))
				return stopFromHandler
			},
		}
	})
}

// A request arriving once the router's stop began is refused with 503,
// Retry-After and Connection: close, without running the application
// (middleware included) and without a log line; the refusal names
// contract.ErrServerShuttingDown, inside a *contract.HTTPError with
// Status 503, to an installed error handler.
func TestRouter_RefusalAfterShutdownIsA503NobodyLogs(t *testing.T) {
	r := router.New()
	var logged []string
	var mu sync.Mutex
	r.SetLogger(levelLogger{
		onError: func(msg string, _ ...any) { mu.Lock(); logged = append(logged, msg); mu.Unlock() },
		onWarn:  func(msg string, _ ...any) { mu.Lock(); logged = append(logged, msg); mu.Unlock() },
	})
	ran := false
	r.Use(func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error { ran = true; return next(c) }
	})
	r.Get("/", func(c *router.Context) error { return c.NoContent() })
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status after Shutdown = %d, want 503", w.Code)
		}
		if got := w.Header().Get("Retry-After"); got != "1" {
			t.Errorf("Retry-After = %q, want 1", got)
		}
		if got := w.Header().Get("Connection"); got != "close" {
			t.Errorf("Connection = %q, want close", got)
		}
	}
	if ran {
		t.Error("middleware ran for a refused request")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logged) != 0 {
		t.Errorf("refusals logged %q, want nothing", logged)
	}

	h := router.New()
	var cause error
	h.SetErrorHandler(func(c *router.Context, err error, _ router.ErrorInfo) {
		cause = err
		c.Response.WriteHeader(http.StatusServiceUnavailable)
	})
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !errors.Is(cause, contract.ErrServerShuttingDown) {
		t.Errorf("error handler got %v, want contract.ErrServerShuttingDown", cause)
	}
	// A handler matching on *contract.HTTPError finds the 503.
	var he *contract.HTTPError
	if !errors.As(cause, &he) || he.Status != http.StatusServiceUnavailable {
		t.Errorf("errors.As(%v, *contract.HTTPError) = %v, want Status 503", cause, he)
	}
}

// Two routers own their runs: stopping one leaves the other serving.
func TestRouter_ShutdownStopsOnlyItsOwnRouter(t *testing.T) {
	a, b := router.New(), router.New()
	for _, r := range []*router.VelocityRouterV2{a, b} {
		r.Get("/", func(c *router.Context) error { return c.NoContent() })
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	w := httptest.NewRecorder()
	b.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("the other router answered %d, want 204", w.Code)
	}
}

// A pool retired from the router's own work (a listener replacing the
// dispatcher) is not waited for by the call that retired it, and Shutdown
// waits for it: within ctx it returns ctx's error while the retired pool's
// listener is held, and nil once it was released.
func TestRouter_ShutdownWaitsForARetiredPool(t *testing.T) {
	r := router.New()
	code := hostile.New(t, hostile.Block, nil)
	var once sync.Once
	r.SetAsyncEventDispatcher(func(context.Context, any) error {
		once.Do(func() {
			r.SetEventDispatcher(nil)
			code.Run()
		})
		return nil
	}, 1, 4)
	r.Get("/", func(c *router.Context) error { return c.NoContent() })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !code.AwaitEntered(t) {
		return
	}
	// No request is in flight: only the retired pool can hold the stop.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := r.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown with the retired pool's listener held = %v, want context.DeadlineExceeded", err)
	}
	code.Release()
	hostile.Within(t, hostile.Deadline, func() {
		if err := r.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown after the release = %v, want nil", err)
		}
	})
}
