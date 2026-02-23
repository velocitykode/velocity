package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockCSRF implements contract.CSRFProtector for testing.
type mockCSRF struct {
	rejectUnsafe bool // when true, reject non-safe methods with 403
}

func (m *mockCSRF) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.rejectUnsafe {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
			default:
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func TestCSRFMiddleware_SafeMethodsPass(t *testing.T) {
	csrf := &mockCSRF{rejectUnsafe: true}

	for _, method := range []string{"GET", "HEAD", "OPTIONS"} {
		r := New()
		r.Use(CSRFMiddleware(csrf))

		handler := func(c *Context) error {
			return c.String(http.StatusOK, "ok")
		}
		r.Get("/test", handler)
		r.Head("/test", handler)
		r.Options("/test", handler)

		req := httptest.NewRequest(method, "/test", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("[%s] expected 200, got %d", method, w.Code)
		}
	}
}

func TestCSRFMiddleware_UnsafeMethod_ValidToken(t *testing.T) {
	csrf := &mockCSRF{rejectUnsafe: false} // accept all

	r := New()
	r.Use(CSRFMiddleware(csrf))

	called := false
	r.Post("/submit", func(c *Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest("POST", "/submit", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !called {
		t.Error("handler should have been called")
	}
}

func TestCSRFMiddleware_UnsafeMethod_InvalidToken(t *testing.T) {
	csrf := &mockCSRF{rejectUnsafe: true}

	r := New()
	r.Use(CSRFMiddleware(csrf))

	handlerCalled := false
	r.Post("/submit", func(c *Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest("POST", "/submit", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if handlerCalled {
		t.Error("handler should not have been called on CSRF failure")
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

func TestCSRFMiddleware_DeleteMethod(t *testing.T) {
	csrf := &mockCSRF{rejectUnsafe: true}

	r := New()
	r.Use(CSRFMiddleware(csrf))
	r.Delete("/item/1", func(c *Context) error {
		return c.String(http.StatusOK, "deleted")
	})

	req := httptest.NewRequest("DELETE", "/item/1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if handlerCalled := w.Code == http.StatusOK; handlerCalled {
		t.Error("expected CSRF rejection for DELETE with bad token")
	}
}
