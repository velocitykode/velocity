package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// assertUnsupportedMediaType asserts the middleware rejected the request
// by returning a 415 *contract.HTTPError and wrote nothing itself.
func assertUnsupportedMediaType(t *testing.T, err error, w *httptest.ResponseRecorder) {
	t.Helper()
	var he *contract.HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("err = %v (%T), want a *contract.HTTPError", err, err)
	}
	if he.StatusCode() != http.StatusUnsupportedMediaType || he.Message != "Unsupported Media Type" {
		t.Errorf("got %d %q, want 415 %q", he.StatusCode(), he.Message, "Unsupported Media Type")
	}
	if w.Body.Len() != 0 || len(w.Header()) != 0 {
		t.Errorf("middleware wrote body %q headers %v, want nothing", w.Body.String(), w.Header())
	}
}

func TestContentType_AllowsValidType(t *testing.T) {
	handler := ContentType("application/json")(func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	c, w := NewTestContext("POST", "/test", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestContentType_RejectsInvalidType(t *testing.T) {
	handler := ContentType("application/json")(func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	c, w := NewTestContext("POST", "/test", strings.NewReader("name=test"))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Request.ContentLength = 9

	assertUnsupportedMediaType(t, handler(c), w)
}

func TestContentType_SkipsGET(t *testing.T) {
	handler := ContentType("application/json")(func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	c, w := NewTestContext("GET", "/test")
	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestContentType_SkipsHEAD(t *testing.T) {
	handler := ContentType("application/json")(func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	c, w := NewTestContext("HEAD", "/test")
	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestContentType_SkipsOPTIONS(t *testing.T) {
	handler := ContentType("application/json")(func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	c, w := NewTestContext("OPTIONS", "/test")
	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestContentType_SkipsDELETEWithoutBody(t *testing.T) {
	handler := ContentType("application/json")(func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	c, w := NewTestContext("DELETE", "/test")
	// ContentLength is 0 by default for nil body
	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestContentType_ChecksDELETEWithBody(t *testing.T) {
	handler := ContentType("application/json")(func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	c, w := NewTestContext("DELETE", "/test", strings.NewReader(`{"id": 1}`))
	c.Request.Header.Set("Content-Type", "text/plain")
	c.Request.ContentLength = 9

	assertUnsupportedMediaType(t, handler(c), w)
}

func TestContentType_ChecksDELETEWithChunkedBody(t *testing.T) {
	handler := ContentType("application/json")(func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	c, w := NewTestContext("DELETE", "/test", strings.NewReader(`{"id": 1}`))
	c.Request.Header.Set("Content-Type", "text/plain")
	c.Request.ContentLength = -1 // chunked encoding

	assertUnsupportedMediaType(t, handler(c), w)
}

func TestContentType_NoContentTypeWithBody(t *testing.T) {
	handler := ContentType("application/json")(func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	c, w := NewTestContext("POST", "/test", strings.NewReader("data"))
	c.Request.ContentLength = 4
	// No Content-Type header set

	assertUnsupportedMediaType(t, handler(c), w)
}

func TestContentType_NoContentTypeNoBody(t *testing.T) {
	handler := ContentType("application/json")(func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	c, w := NewTestContext("POST", "/test")
	// No content-type, no body

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestContentType_WithCharsetParam(t *testing.T) {
	handler := ContentType("application/json")(func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	c, w := NewTestContext("POST", "/test", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json; charset=utf-8")

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestContentType_MultipleAllowed(t *testing.T) {
	handler := ContentType("application/json", "application/xml")(func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	// JSON should pass
	c1, w1 := NewTestContext("POST", "/test", strings.NewReader(`{}`))
	c1.Request.Header.Set("Content-Type", "application/json")
	if err := handler(c1); err != nil {
		t.Fatalf("JSON: unexpected error: %v", err)
	}
	if w1.Code != http.StatusOK {
		t.Errorf("JSON: expected 200, got %d", w1.Code)
	}

	// XML should pass
	c2, w2 := NewTestContext("POST", "/test", strings.NewReader(`<root/>`))
	c2.Request.Header.Set("Content-Type", "application/xml")
	if err := handler(c2); err != nil {
		t.Fatalf("XML: unexpected error: %v", err)
	}
	if w2.Code != http.StatusOK {
		t.Errorf("XML: expected 200, got %d", w2.Code)
	}

	// Form should fail
	c3, w3 := NewTestContext("POST", "/test", strings.NewReader("name=test"))
	c3.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c3.Request.ContentLength = 9
	assertUnsupportedMediaType(t, handler(c3), w3)
}

func TestContentTypeJSON(t *testing.T) {
	handler := ContentTypeJSON()(func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	c, w := NewTestContext("POST", "/test", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestContentTypeForm(t *testing.T) {
	handler := ContentTypeForm()(func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	// URL-encoded should pass
	c1, w1 := NewTestContext("POST", "/test", strings.NewReader("name=test"))
	c1.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := handler(c1); err != nil {
		t.Fatalf("urlencoded: unexpected error: %v", err)
	}
	if w1.Code != http.StatusOK {
		t.Errorf("urlencoded: expected 200, got %d", w1.Code)
	}

	// multipart should pass
	c2, w2 := NewTestContext("POST", "/test", strings.NewReader("data"))
	c2.Request.Header.Set("Content-Type", "multipart/form-data; boundary=something")
	if err := handler(c2); err != nil {
		t.Fatalf("multipart: unexpected error: %v", err)
	}
	if w2.Code != http.StatusOK {
		t.Errorf("multipart: expected 200, got %d", w2.Code)
	}
}

func TestContentType_PUT(t *testing.T) {
	handler := ContentType("application/json")(func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	c, w := NewTestContext("PUT", "/test", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "text/plain")
	c.Request.ContentLength = 2

	assertUnsupportedMediaType(t, handler(c), w)
}

func TestContentType_PATCH(t *testing.T) {
	handler := ContentType("application/json")(func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	c, w := NewTestContext("PATCH", "/test", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}
