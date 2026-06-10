package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// logCapture records every call made through the wired error logger.
type logCapture struct {
	mu      sync.Mutex
	entries []capturedEntry
}

type capturedEntry struct {
	msg string
	kvs []any
}

func (c *logCapture) fn(msg string, kvs ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, capturedEntry{msg: msg, kvs: kvs})
}

func (c *logCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *logCapture) kv(i int, key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	kvs := c.entries[i].kvs
	for j := 0; j+1 < len(kvs); j += 2 {
		if kvs[j] == key {
			return kvs[j+1], true
		}
	}
	return nil, false
}

func serveErrLogReq(r *VelocityRouterV2, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestVelocityRouterV2_DefaultErrorLogging(t *testing.T) {
	t.Run("handler error logs exactly one entry", func(t *testing.T) {
		r := NewV2()
		capture := &logCapture{}
		r.SetErrorLogger(capture.fn)
		r.Get("/boom", func(c *Context) error {
			return errors.New("db exploded")
		})

		w := serveErrLogReq(r, "GET", "/boom")

		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500, got %d", w.Code)
		}
		if capture.count() != 1 {
			t.Fatalf("expected exactly 1 log entry, got %d", capture.count())
		}
		if v, ok := capture.kv(0, "error"); !ok || !strings.Contains(v.(string), "db exploded") {
			t.Errorf("expected error kv containing %q, got %v", "db exploded", v)
		}
		if v, ok := capture.kv(0, "method"); !ok || v != "GET" {
			t.Errorf("expected method kv GET, got %v", v)
		}
		if v, ok := capture.kv(0, "path"); !ok || v != "/boom" {
			t.Errorf("expected path kv /boom, got %v", v)
		}
		if _, ok := capture.kv(0, "stack"); ok {
			t.Error("non-panic error should not carry a stack kv")
		}
	})

	t.Run("recovered panic logs exactly one entry with stack", func(t *testing.T) {
		r := NewV2()
		capture := &logCapture{}
		r.SetErrorLogger(capture.fn)
		r.Get("/panic", func(c *Context) error {
			panic("kaboom")
		})

		w := serveErrLogReq(r, "GET", "/panic")

		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500, got %d", w.Code)
		}
		if capture.count() != 1 {
			t.Fatalf("expected exactly 1 log entry, got %d", capture.count())
		}
		if v, ok := capture.kv(0, "error"); !ok || !strings.Contains(v.(string), "kaboom") {
			t.Errorf("expected error kv containing %q, got %v", "kaboom", v)
		}
		if v, ok := capture.kv(0, "stack"); !ok || v.(string) == "" {
			t.Error("panic entry must carry a non-empty stack kv")
		}
	})

	t.Run("5xx HTTPError is logged", func(t *testing.T) {
		r := NewV2()
		capture := &logCapture{}
		r.SetErrorLogger(capture.fn)
		r.Get("/unavailable", func(c *Context) error {
			return NewHTTPError(http.StatusServiceUnavailable, "down")
		})

		w := serveErrLogReq(r, "GET", "/unavailable")

		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", w.Code)
		}
		if capture.count() != 1 {
			t.Fatalf("expected exactly 1 log entry, got %d", capture.count())
		}
		// The handler-supplied message is server-side detail: it must be
		// logged but never echoed to the client on the 5xx default path.
		if body := w.Body.String(); strings.Contains(body, "down") {
			t.Errorf("5xx HTTPError message leaked to client: %q", body)
		} else if !strings.Contains(body, http.StatusText(http.StatusServiceUnavailable)) {
			t.Errorf("expected generic %q body, got %q", http.StatusText(http.StatusServiceUnavailable), body)
		}
		if v, ok := capture.kv(0, "error"); !ok || !strings.Contains(v.(string), "down") {
			t.Errorf("expected error kv containing %q, got %v", "down", v)
		}
	})

	t.Run("4xx HTTPError is not logged", func(t *testing.T) {
		r := NewV2()
		capture := &logCapture{}
		r.SetErrorLogger(capture.fn)
		r.Get("/missing", func(c *Context) error {
			return NewHTTPError(http.StatusNotFound, "nope")
		})

		w := serveErrLogReq(r, "GET", "/missing")

		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d", w.Code)
		}
		if capture.count() != 0 {
			t.Fatalf("4xx HTTPError must not log, got %d entries", capture.count())
		}
		// 4xx messages are client-facing by design and must keep passing
		// through unchanged.
		if body := w.Body.String(); !strings.Contains(body, "nope") {
			t.Errorf("4xx HTTPError message must reach the client, got %q", body)
		}
	})

	t.Run("custom ErrorHandler suppresses default logging", func(t *testing.T) {
		r := NewV2()
		capture := &logCapture{}
		r.SetErrorLogger(capture.fn)
		r.ErrorHandler = func(ctx *Context, err error) {
			ctx.Response.WriteHeader(http.StatusBadGateway)
		}
		r.Get("/boom", func(c *Context) error {
			return errors.New("handled by consumer")
		})
		r.Get("/panic", func(c *Context) error {
			panic("handled by consumer")
		})

		serveErrLogReq(r, "GET", "/boom")
		serveErrLogReq(r, "GET", "/panic")

		if capture.count() != 0 {
			t.Fatalf("custom ErrorHandler owns reporting; expected 0 entries, got %d", capture.count())
		}
	})

	t.Run("ErrValidationAborted is not logged", func(t *testing.T) {
		r := NewV2()
		capture := &logCapture{}
		r.SetErrorLogger(capture.fn)
		r.Get("/invalid", func(c *Context) error {
			c.Response.WriteHeader(http.StatusSeeOther)
			return ErrValidationAborted
		})

		serveErrLogReq(r, "GET", "/invalid")

		if capture.count() != 0 {
			t.Fatalf("validation abort is not a failure; expected 0 entries, got %d", capture.count())
		}
	})

	t.Run("no logger wired stays silent and still writes 500", func(t *testing.T) {
		r := NewV2()
		r.Get("/boom", func(c *Context) error {
			return errors.New("standalone router")
		})

		w := serveErrLogReq(r, "GET", "/boom")

		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500, got %d", w.Code)
		}
	})
}
