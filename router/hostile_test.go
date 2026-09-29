package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/internal/hostile"
)

// A late Timeout panic is reported from a goroutine nothing else
// recovers: a logger that panics while it is written, in With or in the
// line, is contained, and the line reaches the fallback logger instead.
func TestReportLatePanic_ContainsAPanickingLogger(t *testing.T) {
	for _, on := range []hostile.Method{hostile.With, hostile.Error} {
		for _, reporter := range []bool{false, true} {
			name := string(on)
			if reporter {
				name += "/reporter panics"
			}
			t.Run(name, func(t *testing.T) {
				code := hostile.New(t, hostile.Panic, nil)
				c, _ := NewTestContext(http.MethodGet, "/")
				c.services = &app.Services{Log: hostile.NewLogger(code, on)}
				if reporter {
					c.services.Errors = panickingReporter{}
				}
				if p := hostile.Within(t, hostile.Deadline, func() {
					reportLatePanic(c, newPanicError(errors.New("late boom"), 0))
				}); p != nil {
					t.Fatalf("reportLatePanic let a panic out: %v", p)
				}
				if code.Calls() == 0 {
					t.Fatal("premise: the logger was not called")
				}
			})
		}
	}
}

// eventRecorder records the type of every event the router dispatches.
type eventRecorder struct {
	mu     sync.Mutex
	events []any
}

func (e *eventRecorder) dispatch(_ context.Context, event any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
	return nil
}

func (e *eventRecorder) handled() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, ev := range e.events {
		if _, ok := ev.(RequestHandled); ok {
			n++
		}
		if _, ok := ev.(*RequestHandled); ok {
			n++
		}
	}
	return n
}

// The default error path writes its line through a logger that may
// panic, in With or in the line: the request is still answered with a 500
// and still gets its RequestHandled event.
func TestRouter_DefaultErrorLineContainsAPanickingLogger(t *testing.T) {
	for _, on := range []hostile.Method{hostile.With, hostile.Error} {
		t.Run(string(on), func(t *testing.T) {
			code := hostile.New(t, hostile.Panic, nil)
			events := &eventRecorder{}
			r := New()
			r.SetLogger(hostile.NewLogger(code, on))
			r.SetEventDispatcher(events.dispatch)
			r.Get("/", func(c *Context) error { return errors.New("handler failed") })
			w := httptest.NewRecorder()
			if p := hostile.Within(t, hostile.Deadline, func() {
				r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
			}); p != nil {
				t.Fatalf("ServeHTTP let the logger's panic out: %v", p)
			}
			if code.Calls() == 0 {
				t.Fatal("premise: the logger was not called")
			}
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", w.Code)
			}
			if events.handled() != 1 {
				t.Fatalf("RequestHandled dispatched %d times, want 1", events.handled())
			}
		})
	}
}

// An error handler that panics while it answers a recovered panic ends
// the request as a panic net/http aborts, but only after the router's
// bookkeeping: RequestHandled is dispatched and the Context goes back to
// the pool.
func TestRouter_ErrorHandlerPanickingOnAPanicStillFinishesTheRequest(t *testing.T) {
	events := &eventRecorder{}
	r := New()
	r.SetEventDispatcher(events.dispatch)
	r.SetErrorHandler(func(*Context, error, ErrorInfo) { panic("error handler broke") })
	r.Get("/", func(c *Context) error { panic("handler broke") })
	p := hostile.Within(t, hostile.Deadline, func() {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	})
	if p == nil {
		t.Fatal("the error handler's panic was swallowed; net/http must still abort the connection")
	}
	if events.handled() != 1 {
		t.Fatalf("RequestHandled dispatched %d times, want 1", events.handled())
	}
}
