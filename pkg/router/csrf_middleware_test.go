package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockCSRF implements csrfVerifier for testing.
type mockCSRF struct {
	verifyErr error
	token     string
}

func (m *mockCSRF) VerifyToken(_ *http.Request) error { return m.verifyErr }
func (m *mockCSRF) Token(_ *http.Request) string      { return m.token }

func TestCSRFMiddleware_SafeMethodsPass(t *testing.T) {
	csrf := &mockCSRF{verifyErr: errors.New("should not be called"), token: "tok123"}

	for _, method := range []string{"GET", "HEAD", "OPTIONS"} {
		r := New()
		r.Use(CSRFMiddleware(csrf))

		var gotToken string
		r.Get("/test", func(c *Context) error {
			gotToken = c.GetString("csrf_token")
			return c.String(http.StatusOK, "ok")
		})
		if method != "GET" {
			r.Head("/test", func(c *Context) error {
				gotToken = c.GetString("csrf_token")
				return c.String(http.StatusOK, "ok")
			})
			r.Options("/test", func(c *Context) error {
				gotToken = c.GetString("csrf_token")
				return c.String(http.StatusOK, "ok")
			})
		}

		req := httptest.NewRequest(method, "/test", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("[%s] expected 200, got %d", method, w.Code)
		}
		if gotToken != "tok123" {
			t.Errorf("[%s] expected csrf_token tok123, got %q", method, gotToken)
		}
	}
}

func TestCSRFMiddleware_UnsafeMethod_ValidToken(t *testing.T) {
	csrf := &mockCSRF{verifyErr: nil, token: "valid-token"}

	r := New()
	r.Use(CSRFMiddleware(csrf))

	var gotToken string
	r.Post("/submit", func(c *Context) error {
		gotToken = c.GetString("csrf_token")
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest("POST", "/submit", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if gotToken != "valid-token" {
		t.Errorf("expected csrf_token valid-token, got %q", gotToken)
	}
}

func TestCSRFMiddleware_UnsafeMethod_InvalidToken(t *testing.T) {
	csrf := &mockCSRF{verifyErr: errors.New("token mismatch"), token: ""}

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

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
	if handlerCalled {
		t.Error("handler should not have been called on CSRF failure")
	}
}

func TestCSRFMiddleware_InvalidInstance(t *testing.T) {
	// Pass something that does not satisfy csrfVerifier
	r := New()
	r.Use(CSRFMiddleware("not-a-csrf"))

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
	csrf := &mockCSRF{verifyErr: errors.New("bad token"), token: ""}

	r := New()
	r.Use(CSRFMiddleware(csrf))
	r.Delete("/item/1", func(c *Context) error {
		return c.String(http.StatusOK, "deleted")
	})

	req := httptest.NewRequest("DELETE", "/item/1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for DELETE with bad token, got %d", w.Code)
	}
}
