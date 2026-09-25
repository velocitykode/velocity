package router

import (
	"errors"
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
	// Install an error handler so a returned error maps to a 500
	// response we can assert on.
	r.SetErrorHandler(func(c *Context, err error, info ErrorInfo) {
		_ = c.String(http.StatusInternalServerError, err.Error())
	})
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

// TestTimeoutWriter_ResultHandoff asserts the handler goroutine's result
// is never lost between the goroutine and the middleware: delivered in
// time it waits in done; delivered as the deadline passed, markTimedOut
// takes it out as a late result; delivered after the timeout, deliver
// refuses it and it stays with the goroutine.
func TestTimeoutWriter_ResultHandoff(t *testing.T) {
	result := errors.New("handler result")
	tests := []struct {
		name        string
		run         func(tw *timeoutWriter, done chan error) (delivered bool, late error, lateOK bool)
		wantDeliver bool
		wantLate    error
		wantInDone  bool
	}{
		{
			name: "delivered in time",
			run: func(tw *timeoutWriter, done chan error) (bool, error, bool) {
				return tw.deliver(done, result), nil, false
			},
			wantDeliver: true,
			wantInDone:  true,
		},
		{
			name: "delivered as the deadline passed",
			run: func(tw *timeoutWriter, done chan error) (bool, error, bool) {
				delivered := tw.deliver(done, result)
				late, ok := tw.markTimedOut(done)
				return delivered, late, ok
			},
			wantDeliver: true,
			wantLate:    result,
		},
		{
			name: "delivered after the timeout",
			run: func(tw *timeoutWriter, done chan error) (bool, error, bool) {
				late, ok := tw.markTimedOut(done)
				return tw.deliver(done, result), late, ok
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tw := newTimeoutWriter(httptest.NewRecorder())
			done := make(chan error, 1)
			delivered, late, lateOK := tt.run(tw, done)
			if delivered != tt.wantDeliver {
				t.Errorf("deliver = %v, want %v", delivered, tt.wantDeliver)
			}
			if lateOK != (tt.wantLate != nil) || !errors.Is(late, tt.wantLate) {
				t.Errorf("markTimedOut = %v, %v; want %v, %v", late, lateOK, tt.wantLate, tt.wantLate != nil)
			}
			if got := len(done); (got != 0) != tt.wantInDone {
				t.Errorf("results left in done = %d, want in done: %v", got, tt.wantInDone)
			}
		})
	}
}
