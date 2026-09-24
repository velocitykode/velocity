package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// errMockRejected is the rejection mockCSRF returns.
var errMockRejected = errors.New("mock csrf: rejected")

// mockStateKey is the context key mockCSRF attaches, standing in for the
// real protector's request-scoped token state.
type mockStateKey struct{}

// mockCSRF implements contract.CSRFProtector for testing. Protect attaches
// a context value to every request and, when rejectUnsafe is set, rejects
// unsafe methods with errMockRejected.
type mockCSRF struct {
	rejectUnsafe bool
}

func (m *mockCSRF) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r, err := m.Protect(w, r)
		if err != nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (m *mockCSRF) Protect(_ http.ResponseWriter, r *http.Request) (*http.Request, error) {
	r = r.WithContext(context.WithValue(r.Context(), mockStateKey{}, "attached"))
	if !m.rejectUnsafe {
		return r, nil
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return r, nil
	}
	return r, errMockRejected
}

func TestCSRFMiddleware_Router(t *testing.T) {
	tests := []struct {
		name       string
		csrf       *mockCSRF
		method     string
		wantCalled bool
		wantErr    error
	}{
		{name: "GET passes", csrf: &mockCSRF{rejectUnsafe: true}, method: http.MethodGet, wantCalled: true},
		{name: "HEAD passes", csrf: &mockCSRF{rejectUnsafe: true}, method: http.MethodHead, wantCalled: true},
		{name: "OPTIONS passes", csrf: &mockCSRF{rejectUnsafe: true}, method: http.MethodOptions, wantCalled: true},
		{name: "accepted POST", csrf: &mockCSRF{}, method: http.MethodPost, wantCalled: true},
		{name: "rejected POST", csrf: &mockCSRF{rejectUnsafe: true}, method: http.MethodPost, wantErr: errMockRejected},
		{name: "rejected DELETE", csrf: &mockCSRF{rejectUnsafe: true}, method: http.MethodDelete, wantErr: errMockRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New()
			var gotErr error
			r.SetErrorHandler(func(c *Context, err error, _ ErrorInfo) {
				gotErr = err
				c.Response.WriteHeader(599)
			})
			r.Use(CSRFMiddleware(tt.csrf))
			called := false
			var state any
			r.Match([]string{tt.method}, "/x", func(c *Context) error {
				called = true
				state = c.Request.Context().Value(mockStateKey{})
				return c.String(http.StatusOK, "ok")
			})

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tt.method, "/x", nil))

			if called != tt.wantCalled {
				t.Fatalf("handler called = %v, want %v", called, tt.wantCalled)
			}
			if tt.wantCalled {
				if w.Code != http.StatusOK {
					t.Errorf("status = %d, want 200", w.Code)
				}
				if state != "attached" {
					t.Errorf("handler request context value = %v, want the value Protect attached", state)
				}
				if gotErr != nil {
					t.Errorf("error handler fired with %v", gotErr)
				}
				return
			}
			if !errors.Is(gotErr, tt.wantErr) {
				t.Fatalf("error handler got %v, want %v", gotErr, tt.wantErr)
			}
			if w.Code != 599 {
				t.Errorf("status = %d, want the error handler's 599 (the middleware must write nothing)", w.Code)
			}
		})
	}
}

// TestCSRFMiddleware_RejectionWritesNothing pins that a rejection is
// returned unchanged, the next handler never runs, and the response is
// left untouched for the error pipeline.
func TestCSRFMiddleware_RejectionWritesNothing(t *testing.T) {
	c, w := NewTestContext(http.MethodPost, "/submit")
	orig := c.Request
	err := CSRFMiddleware(&mockCSRF{rejectUnsafe: true})(func(*Context) error {
		t.Fatal("next must not run on rejection")
		return nil
	})(c)

	if !errors.Is(err, errMockRejected) {
		t.Fatalf("err = %v, want %v", err, errMockRejected)
	}
	if w.Body.Len() != 0 || len(w.Header()) != 0 || w.Code != http.StatusOK || w.Flushed {
		t.Errorf("response was written: code=%d headers=%v body=%q", w.Code, w.Header(), w.Body.String())
	}
	if c.Request == orig {
		t.Error("c.Request must be replaced by the request Protect returned")
	}
	if c.Request.Context().Value(mockStateKey{}) != "attached" {
		t.Error("c.Request must carry the context Protect attached")
	}
}

// TestCSRFMiddleware_ReusesContext pins that next receives the original
// *Context (never a Wrap copy) with the request Protect returned.
func TestCSRFMiddleware_ReusesContext(t *testing.T) {
	c, _ := NewTestContext(http.MethodPost, "/submit")
	var got *Context
	err := CSRFMiddleware(&mockCSRF{})(func(inner *Context) error {
		got = inner
		return nil
	})(c)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != c {
		t.Fatal("next must receive the original *Context")
	}
	if c.Request.Context().Value(mockStateKey{}) != "attached" {
		t.Error("c.Request must carry the context Protect attached")
	}
}

// TestCSRFMiddleware_HandlerErrorPropagates pins that an accepted request
// returns next's error unchanged.
func TestCSRFMiddleware_HandlerErrorPropagates(t *testing.T) {
	want := errors.New("handler failed")
	c, _ := NewTestContext(http.MethodPost, "/submit")
	err := CSRFMiddleware(&mockCSRF{})(func(*Context) error { return want })(c)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestCSRFMiddleware_NilInstance(t *testing.T) {
	r := New()
	r.Use(CSRFMiddleware(nil))

	r.Post("/submit", func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest("POST", "/submit", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (passthrough), got %d", w.Code)
	}
}
