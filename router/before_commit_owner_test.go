package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// An error handler that renders through Wrap, whose own listener panics,
// raises a listener's panic that belongs to another commit owner: the
// request's owner made no progress, so the router does not answer again.
// The request ends, with the handler called once.
func TestBeforeCommit_ListenerPanicOfAnotherOwnerIsNotRetried(t *testing.T) {
	calls := 0
	r := New()
	r.SetErrorHandler(func(c *Context, _ error, _ ErrorInfo) {
		calls++
		Wrap(func(inner *Context) error {
			inner.BeforeCommit(func(int, http.ResponseWriter) { panic("inner listener exploded") })
			inner.Response.WriteHeader(http.StatusInternalServerError)
			return nil
		})(c.Response, c.Request)
	})
	r.Get("/x", func(*Context) error { return errors.New("failed") })
	var escaped any
	if p := hostile.Within(t, hostile.Deadline, func() {
		defer func() { escaped = recover() }()
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	}); p != nil {
		t.Fatalf("panicked: %v", p)
	}
	if escaped == nil {
		t.Fatal("the error handler's panic was swallowed")
	}
	if calls > 2 {
		t.Fatalf("error handler calls = %d: the boundary kept answering", calls)
	}
}
