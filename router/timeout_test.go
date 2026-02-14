package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTimeout_HandlerCompletesBeforeDeadline(t *testing.T) {
	r := New()
	r.Use(Timeout(500 * time.Millisecond))
	r.Get("/fast", func(c *Context) error {
		return c.String(http.StatusOK, "OK")
	})

	req := httptest.NewRequest("GET", "/fast", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "OK") {
		t.Errorf("expected body OK, got %q", w.Body.String())
	}
}

func TestTimeout_HandlerExceedsDeadline(t *testing.T) {
	r := New()
	r.Use(Timeout(50 * time.Millisecond))
	r.Get("/slow", func(c *Context) error {
		// Wait longer than the timeout
		select {
		case <-time.After(500 * time.Millisecond):
		case <-c.Request.Context().Done():
		}
		return nil
	})

	req := httptest.NewRequest("GET", "/slow", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Service Unavailable") {
		t.Errorf("expected Service Unavailable body, got %q", w.Body.String())
	}
}

func TestTimeout_PropagatesHandlerError(t *testing.T) {
	r := New()
	r.Use(Timeout(500 * time.Millisecond))
	r.Get("/err", func(c *Context) error {
		return c.String(http.StatusBadRequest, "bad")
	})

	req := httptest.NewRequest("GET", "/err", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestTimeout_ContextDeadlineSet(t *testing.T) {
	r := New()
	timeout := 200 * time.Millisecond
	r.Use(Timeout(timeout))

	var hasDeadline bool
	r.Get("/check", func(c *Context) error {
		_, hasDeadline = c.Request.Context().Deadline()
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest("GET", "/check", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !hasDeadline {
		t.Error("expected context to have a deadline")
	}
}
