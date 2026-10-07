package routerbridge

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
)

// Commit listeners that panic while the error pipeline renders a returned
// error are contained by the router, not by the pipeline's own recovery:
// with two panicking listeners ahead of an ordinary one, the client gets a
// 500 and the ordinary listener runs once, with that 500.
func TestInstall_ListenerPanicsDuringRenderReachTheRouter(t *testing.T) {
	h := problem.NewHandler(problem.WithHandlerLogger(&errLineLogger{}))
	r := router.New()
	Install(r, WithHandler(func() contract.ErrorHandler { return h }))
	var seen []int
	r.Use(func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			c.BeforeCommit(func(status int, _ http.ResponseWriter) { seen = append(seen, status) })
			c.BeforeCommit(func(int, http.ResponseWriter) { panic("second listener exploded") })
			c.BeforeCommit(func(int, http.ResponseWriter) { panic("first listener exploded") })
			return next(c)
		}
	})
	r.Get("/missing", func(*router.Context) error { return problem.NotFound() })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !reflect.DeepEqual(seen, []int{500}) {
		t.Fatalf("the ordinary listener saw %v, want [500] once", seen)
	}
}

// An http.ErrAbortHandler from a listener that runs during the pipeline's
// last-resort answer keeps its meaning: it reaches net/http.
func TestInstall_ListenerAbortDuringLastResortReachesNetHTTP(t *testing.T) {
	h := problem.NewHandler(problem.WithHandlerLogger(&errLineLogger{}))
	r := router.New()
	Install(r, WithHandler(func() contract.ErrorHandler { return h }))
	r.Use(func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			c.BeforeCommit(func(int, http.ResponseWriter) { panic(http.ErrAbortHandler) })
			c.BeforeCommit(func(int, http.ResponseWriter) { panic("first listener exploded") })
			return next(c)
		}
	})
	r.Get("/missing", func(*router.Context) error { return problem.NotFound() })
	escaped := func() (p any) {
		defer func() { p = recover() }()
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/missing", nil))
		return nil
	}()
	if err, ok := escaped.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
		t.Fatalf("ServeHTTP ended with %v, want the http.ErrAbortHandler panic", escaped)
	}
}
