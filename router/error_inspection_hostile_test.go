package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// inspectionPanics is a handler error whose method named by method panics
// when the boundary classifies it. It carries a 404 and a header, so an
// answer built from partial facts would show.
type inspectionPanics struct{ method string }

func (e inspectionPanics) Error() string {
	if e.method == "Error" {
		panic("Error broke")
	}
	return "inspection panics"
}

func (e inspectionPanics) Is(error) bool {
	if e.method == "Is" {
		panic("Is broke")
	}
	return false
}

func (e inspectionPanics) As(any) bool {
	if e.method == "As" {
		panic("As broke")
	}
	return false
}

func (e inspectionPanics) Unwrap() error {
	if e.method == "Unwrap" {
		panic("Unwrap broke")
	}
	return nil
}

func (e inspectionPanics) StatusCode() int {
	if e.method == "StatusCode" {
		panic("StatusCode broke")
	}
	return http.StatusNotFound
}

func (e inspectionPanics) Headers() http.Header {
	if e.method == "Headers" {
		panic("Headers broke")
	}
	return http.Header{"X-Partial": {"yes"}}
}

func (e inspectionPanics) ClientMessage() string {
	if e.method == "ClientMessage" {
		panic("ClientMessage broke")
	}
	return "partial message"
}

// inspectionLoop unwraps to itself.
type inspectionLoop struct{}

func (e *inspectionLoop) Error() string { return "loop" }
func (e *inspectionLoop) Unwrap() error { return e }

// A handler error whose methods panic, or whose chain loops back on
// itself, is answered by the default boundary with the fixed unnamed 500:
// no status, header or message of its own, the request returns, and
// RequestFailed fires once.
func TestBoundary_HostileErrorGetsTheFixedAnswer(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"loop", &inspectionLoop{}},
	}
	for _, m := range []string{"Is", "As", "Unwrap", "StatusCode", "Headers", "ClientMessage"} {
		cases = append(cases, struct {
			name string
			err  error
		}{m + " panics", inspectionPanics{method: m}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewV2()
			r.SetLogger(levelLogger{})
			var failed atomic.Int32
			r.SetEventDispatcher(func(_ context.Context, ev any) error {
				if _, ok := ev.(*RequestFailed); ok {
					failed.Add(1)
				}
				return nil
			})
			r.Get("/x", func(*Context) error { return tc.err })
			w := httptest.NewRecorder()
			if p := hostile.Within(t, hostile.Deadline, func() {
				r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
			}); p != nil {
				t.Fatalf("the boundary panicked: %v", p)
			}
			if w.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", w.Code)
			}
			if w.Header().Get("X-Partial") != "" {
				t.Error("the answer carries a header from a chain that could not be read")
			}
			if body := w.Body.String(); body != http.StatusText(http.StatusInternalServerError)+"\n" {
				t.Errorf("body = %q, want the plain 500 text", body)
			}
			hostile.Eventually(t, hostile.Deadline, "RequestFailed", func() bool { return failed.Load() == 1 })
		})
	}
}

// The same errors through DefaultErrorHandler, as an installed error
// handler calls it.
func TestDefaultErrorHandler_HostileErrorGetsTheFixedAnswer(t *testing.T) {
	for _, err := range []error{&inspectionLoop{}, inspectionPanics{method: "Is"}, inspectionPanics{method: "Headers"}} {
		c, w := NewTestContext(http.MethodGet, "/x")
		if p := hostile.Within(t, hostile.Deadline, func() { DefaultErrorHandler(c, err, ErrorInfo{}) }); p != nil {
			t.Fatalf("%T: DefaultErrorHandler panicked: %v", err, p)
		}
		if w.Code != http.StatusInternalServerError || w.Header().Get("X-Partial") != "" {
			t.Errorf("%T: status %d, header %q; want the plain 500", err, w.Code, w.Header().Get("X-Partial"))
		}
	}
}
