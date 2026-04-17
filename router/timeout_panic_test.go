package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestTimeout_RecoversHandlerPanic verifies that a panic inside the inner
// handler does not tear down the server and surfaces through the normal
// error path of Timeout middleware.
func TestTimeout_RecoversHandlerPanic(t *testing.T) {
	r := New()
	r.Use(Timeout(500 * time.Millisecond))
	// Install a final ErrorHandler so a returned error maps to a 500
	// response we can assert on.
	r.ErrorHandler = func(c *Context, err error) {
		_ = c.String(http.StatusInternalServerError, err.Error())
	}
	r.Get("/boom", func(c *Context) error {
		panic("handler boom")
	})

	req := httptest.NewRequest("GET", "/boom", nil)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.ServeHTTP(w, req)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return — Timeout recovery may be broken")
	}

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 from recovered panic, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "velocity/router: timeout handler panic") {
		t.Fatalf("expected wrapped panic error in body, got %q", w.Body.String())
	}
}
