package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewHTTPError(t *testing.T) {
	t.Run("default message from status code", func(t *testing.T) {
		err := NewHTTPError(http.StatusNotFound)
		if err.Code != http.StatusNotFound {
			t.Errorf("expected code %d, got %d", http.StatusNotFound, err.Code)
		}
		if err.Message != "Not Found" {
			t.Errorf("expected message 'Not Found', got %q", err.Message)
		}
		if err.Error() != "Not Found" {
			t.Errorf("expected Error() 'Not Found', got %q", err.Error())
		}
	})

	t.Run("custom message", func(t *testing.T) {
		err := NewHTTPError(http.StatusBadRequest, "invalid email")
		if err.Code != http.StatusBadRequest {
			t.Errorf("expected code %d, got %d", http.StatusBadRequest, err.Code)
		}
		if err.Message != "invalid email" {
			t.Errorf("expected message 'invalid email', got %q", err.Message)
		}
	})

	t.Run("empty custom message uses status text", func(t *testing.T) {
		err := NewHTTPError(http.StatusForbidden, "")
		if err.Message != "Forbidden" {
			t.Errorf("expected message 'Forbidden', got %q", err.Message)
		}
	})

	t.Run("internal error unwrap", func(t *testing.T) {
		inner := errors.New("database connection failed")
		err := NewHTTPError(http.StatusInternalServerError)
		err.Internal = inner

		if !errors.Is(err, inner) {
			t.Error("expected errors.Is to find internal error")
		}
	})

	t.Run("nil internal error", func(t *testing.T) {
		err := NewHTTPError(http.StatusOK)
		if err.Unwrap() != nil {
			t.Error("expected nil Unwrap for no internal error")
		}
	})
}

func TestErrorHandlerMiddleware_AbsorbsError(t *testing.T) {
	handlerErr := errors.New("boom")

	var seen error
	mw := ErrorHandlerMiddleware(func(c *Context, err error) {
		seen = err
		c.Response.WriteHeader(http.StatusTeapot)
		_, _ = c.Response.Write([]byte("handled"))
	})

	final := mw(func(c *Context) error {
		return handlerErr
	})

	w := httptest.NewRecorder()
	c := NewContext(w, httptest.NewRequest("GET", "/", nil))

	if ret := final(c); ret != nil {
		t.Errorf("middleware returned %v, want nil (error must be absorbed)", ret)
	}
	if !errors.Is(seen, handlerErr) {
		t.Errorf("fn saw err = %v, want %v", seen, handlerErr)
	}
	if w.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d (handler fn must run)", w.Code, http.StatusTeapot)
	}
	if w.Body.String() != "handled" {
		t.Errorf("body = %q, want %q", w.Body.String(), "handled")
	}
}

func TestErrorHandlerMiddleware_PassesThroughSuccess(t *testing.T) {
	called := false
	mw := ErrorHandlerMiddleware(func(c *Context, err error) {
		called = true
	})

	final := mw(func(c *Context) error {
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	w := httptest.NewRecorder()
	c := NewContext(w, httptest.NewRequest("GET", "/", nil))

	if err := final(c); err != nil {
		t.Errorf("middleware returned %v on success path, want nil", err)
	}
	if called {
		t.Error("error fn must not run when handler succeeds")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}
