package router

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// TestResponseWriter_EarlyHintsKeepFinalStatus asserts a 103 Early Hints
// through the router's writer does not commit the response: the handler's
// final 201 reaches the client, RequestHandled records 201, and an error
// returned after the hint is still answered.
func TestResponseWriter_EarlyHintsKeepFinalStatus(t *testing.T) {
	var mu sync.Mutex
	var handled []int
	r := New()
	r.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		if rh, ok := event.(*RequestHandled); ok {
			mu.Lock()
			handled = append(handled, rh.StatusCode)
			mu.Unlock()
		}
		return nil
	})
	r.Get("/created", func(c *Context) error {
		c.Response.Header().Set("Link", "</app.css>; rel=preload; as=style")
		c.Response.WriteHeader(http.StatusEarlyHints)
		c.Response.WriteHeader(http.StatusCreated)
		_, err := c.Response.Write([]byte("made"))
		return err
	})
	r.Get("/failed", func(c *Context) error {
		c.Response.WriteHeader(http.StatusEarlyHints)
		return contract.NewHTTPError(http.StatusConflict)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	for _, tt := range []struct {
		path string
		want int
	}{
		{"/created", http.StatusCreated},
		{"/failed", http.StatusConflict},
	} {
		resp, err := srv.Client().Get(srv.URL + tt.path)
		if err != nil {
			t.Fatalf("%s: %v", tt.path, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tt.want {
			t.Errorf("%s: client status = %d, want %d", tt.path, resp.StatusCode, tt.want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(handled) != 2 || handled[0] != http.StatusCreated || handled[1] != http.StatusConflict {
		t.Errorf("RequestHandled statuses = %v, want [201 409]", handled)
	}
}

// hijackWriter is a ResponseRecorder whose Hijack hands back one end of a
// pipe, or fails with err when set.
type hijackWriter struct {
	*httptest.ResponseRecorder
	err error
}

func (h *hijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h.err != nil {
		return nil, nil, h.err
	}
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return c1, bufio.NewReadWriter(bufio.NewReader(c1), bufio.NewWriter(c1)), nil
}

// hijackAndClose hijacks through rw and closes the connection.
func hijackAndClose(t *testing.T, rw *responseWriter) {
	t.Helper()
	conn, _, err := rw.Hijack()
	if err != nil {
		t.Fatalf("Hijack: %v", err)
	}
	_ = conn.Close()
}

// TestResponseWriter_CommittedRule asserts the writer's Committed follows
// the contract.CommitReporter rule problem.TrackedWriter uses: a 1xx other
// than 101 does not commit; 101, a final status, a Write, a Flush and a
// successful Hijack do.
func TestResponseWriter_CommittedRule(t *testing.T) {
	tests := []struct {
		name  string
		write func(t *testing.T, rw *responseWriter)
		want  bool
	}{
		{name: "nothing", write: func(*testing.T, *responseWriter) {}, want: false},
		{name: "100 continue", write: func(_ *testing.T, rw *responseWriter) { rw.WriteHeader(http.StatusContinue) }, want: false},
		{name: "103 early hints", write: func(_ *testing.T, rw *responseWriter) { rw.WriteHeader(http.StatusEarlyHints) }, want: false},
		{name: "101 switching", write: func(_ *testing.T, rw *responseWriter) { rw.WriteHeader(http.StatusSwitchingProtocols) }, want: true},
		{name: "200", write: func(_ *testing.T, rw *responseWriter) { rw.WriteHeader(http.StatusOK) }, want: true},
		{name: "write", write: func(_ *testing.T, rw *responseWriter) { _, _ = rw.Write([]byte("x")) }, want: true},
		{name: "flush", write: func(_ *testing.T, rw *responseWriter) { rw.Flush() }, want: true},
		{name: "hijack", write: func(t *testing.T, rw *responseWriter) { hijackAndClose(t, rw) }, want: true},
		{name: "hijack refused", write: func(_ *testing.T, rw *responseWriter) {
			rw.ResponseWriter.(*hijackWriter).err = http.ErrHijacked
			_, _, _ = rw.Hijack()
		}, want: false},
		{name: "103 then hijack", write: func(t *testing.T, rw *responseWriter) {
			rw.WriteHeader(http.StatusEarlyHints)
			hijackAndClose(t, rw)
		}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rw := acquireResponseWriter(&hijackWriter{ResponseRecorder: httptest.NewRecorder()})
			defer releaseResponseWriter(rw)
			tt.write(t, rw)
			var cr contract.CommitReporter = rw
			if got := cr.Committed(); got != tt.want {
				t.Errorf("Committed = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestNewRenderContext_OverRouterWriterSeesCommitment asserts a bare
// contract.NewRenderContext (and so problem.ErrorHandler) over the
// router's writer reports Written once the handler committed, and neither
// its WriteHeader nor its Redirect writes over it.
func TestNewRenderContext_OverRouterWriterSeesCommitment(t *testing.T) {
	r := New()
	r.SetErrorHandler(func(c *Context, err error, _ ErrorInfo) {
		rc := contract.NewRenderContext(c.Response, c.Request)
		if !rc.Written() {
			t.Error("NewRenderContext over the router writer: Written = false after the handler committed")
		}
		if err := rc.Redirect(http.StatusSeeOther, "/elsewhere"); !errors.Is(err, contract.ErrInvalidRedirect) {
			t.Errorf("Redirect over a committed response = %v, want ErrInvalidRedirect", err)
		}
		rc.WriteHeader(http.StatusInternalServerError)
	})
	r.Get("/x", func(c *Context) error {
		c.Response.WriteHeader(http.StatusAccepted)
		_, _ = c.Response.Write([]byte("first"))
		return errors.New("late failure")
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", w.Code)
	}
	if got := w.Body.String(); got != "first" || w.Header().Get("Location") != "" {
		t.Errorf("body = %q Location = %q, want the handler's bytes only", got, w.Header().Get("Location"))
	}
}
