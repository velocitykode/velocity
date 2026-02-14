package router

import (
	"net/http"
	"strings"
	"testing"
)

func TestNewTestContext_Basic(t *testing.T) {
	c, w := NewTestContext("GET", "/hello")

	if c.Request.Method != "GET" {
		t.Errorf("expected GET, got %s", c.Request.Method)
	}
	if c.Request.URL.Path != "/hello" {
		t.Errorf("expected /hello, got %s", c.Request.URL.Path)
	}
	if w == nil {
		t.Fatal("expected non-nil ResponseRecorder")
	}
	if c.Response != w {
		t.Error("context.Response should be the ResponseRecorder")
	}
	if c.values == nil {
		t.Error("expected initialized values map")
	}
}

func TestNewTestContext_WithBody(t *testing.T) {
	body := strings.NewReader(`{"key":"value"}`)
	c, _ := NewTestContext("POST", "/submit", body)

	if c.Request.Method != "POST" {
		t.Errorf("expected POST, got %s", c.Request.Method)
	}
	if c.Request.Body == nil {
		t.Fatal("expected non-nil body")
	}
}

func TestNewTestContext_SetGet(t *testing.T) {
	c, _ := NewTestContext("GET", "/test")
	c.Set("user", "alice")

	if c.GetString("user") != "alice" {
		t.Errorf("expected alice, got %s", c.GetString("user"))
	}
}

func TestNewTestContext_WriteResponse(t *testing.T) {
	c, w := NewTestContext("GET", "/test")

	err := c.String(http.StatusOK, "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != "hello" {
		t.Errorf("expected hello, got %q", w.Body.String())
	}
}

func TestNewTestContext_JSONResponse(t *testing.T) {
	c, w := NewTestContext("GET", "/api")

	err := c.JSON(http.StatusCreated, map[string]string{"status": "created"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"status":"created"`) {
		t.Errorf("expected JSON body, got %q", w.Body.String())
	}
}

func TestNewTestContext_NoBody(t *testing.T) {
	c, _ := NewTestContext("DELETE", "/resource/1")

	if c.Request.Method != "DELETE" {
		t.Errorf("expected DELETE, got %s", c.Request.Method)
	}
	if c.Request.Body == nil {
		// httptest.NewRequest with nil body still sets Body to http.NoBody
		// which is fine
	}
}
