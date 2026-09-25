package router

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

func TestErrorHandlerMiddleware_Paths(t *testing.T) {
	handlerErr := errors.New("boom")

	tests := []struct {
		name        string
		ret         error
		handles     bool
		wantCalled  bool
		wantHandled bool
		wantErr     error
		wantStatus  int
	}{
		{
			name:        "handled error returns Handled wrapping the cause",
			ret:         handlerErr,
			handles:     true,
			wantCalled:  true,
			wantHandled: true,
			wantErr:     handlerErr,
			wantStatus:  http.StatusTeapot,
		},
		{
			name:       "declined error passes through unchanged",
			ret:        handlerErr,
			handles:    false,
			wantCalled: true,
			wantErr:    handlerErr,
			wantStatus: http.StatusOK,
		},
		{
			name:       "success does not call fn",
			ret:        nil,
			wantStatus: http.StatusOK,
		},
		{
			name:        "bare ErrResponseWritten skips fn",
			ret:         contract.ErrResponseWritten,
			handles:     true,
			wantHandled: true,
			wantErr:     contract.ErrResponseWritten,
			wantStatus:  http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen error
			called := false
			mw := ErrorHandlerMiddleware(func(c *Context, err error) bool {
				called = true
				seen = err
				if tt.handles {
					c.Response.WriteHeader(http.StatusTeapot)
				}
				return tt.handles
			})
			final := mw(func(c *Context) error { return tt.ret })

			w := httptest.NewRecorder()
			got := final(NewContext(w, httptest.NewRequest("GET", "/", nil)))

			if called != tt.wantCalled {
				t.Fatalf("fn called = %v, want %v", called, tt.wantCalled)
			}
			if called && !errors.Is(seen, handlerErr) {
				t.Errorf("fn saw %v, want %v", seen, handlerErr)
			}
			if tt.wantErr == nil {
				if got != nil {
					t.Fatalf("middleware returned %v, want nil", got)
				}
			} else if !errors.Is(got, tt.wantErr) {
				t.Fatalf("middleware returned %v, want a chain holding %v", got, tt.wantErr)
			}
			if handled := errors.Is(got, contract.ErrResponseWritten); handled != tt.wantHandled {
				t.Errorf("errors.Is(ret, ErrResponseWritten) = %v, want %v", handled, tt.wantHandled)
			}
			if tt.wantHandled && tt.wantCalled && contract.HandledCause(got) != handlerErr {
				t.Errorf("HandledCause = %v, want %v", contract.HandledCause(got), handlerErr)
			}
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestPanicError_Contract(t *testing.T) {
	sentinel := errors.New("inner")
	notFound := contract.NewHTTPError(http.StatusNotFound)

	tests := []struct {
		name     string
		err      *PanicError
		wantText string
		wantIs   error
	}{
		{"string panic", &PanicError{Err: panicerr.FromRecovered("kaboom")}, "panic: kaboom", nil},
		{"error panic unwraps", &PanicError{Err: panicerr.FromRecovered(sentinel)}, "panic: inner", sentinel},
		{"http error panic still 500", &PanicError{Err: panicerr.FromRecovered(notFound)}, "panic: Not Found", notFound},
		{"nil Err", &PanicError{}, "panic", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.wantText {
				t.Errorf("Error() = %q, want %q", got, tt.wantText)
			}
			if tt.wantIs != nil && !errors.Is(tt.err, tt.wantIs) {
				t.Errorf("errors.Is(%v, %v) = false", tt.err, tt.wantIs)
			}
			status, _, ok := contract.StatusOf(fmt.Errorf("wrapped: %w", tt.err))
			if !ok || status != http.StatusInternalServerError {
				t.Errorf("StatusOf = %d, %v, want 500, true", status, ok)
			}
			var rep contract.Reportable
			if !errors.As(tt.err, &rep) || !rep.ShouldReport() {
				t.Error("a PanicError must always report")
			}
		})
	}

	var nilErr *PanicError
	if nilErr.Unwrap() != nil {
		t.Error("nil PanicError Unwrap must be nil")
	}
}

// gzipAllWriter compresses every body byte written through it.
type gzipAllWriter struct {
	http.ResponseWriter
	zw *gzip.Writer
}

func (g *gzipAllWriter) Write(p []byte) (int, error) { return g.zw.Write(p) }

// compressAll wraps next the way an on-the-fly compression handler does:
// the response is labelled gzip before next runs, and every body byte next
// writes goes through the compressor.
func compressAll(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		defer zw.Close()
		next.ServeHTTP(&gzipAllWriter{ResponseWriter: w, zw: zw}, r)
	})
}

// TestDefaultErrorHandler_CompressingWriterKeepsContentEncoding asserts the
// standalone router's default error answer keeps the Content-Encoding a
// compression handler wrapping the router's writer set up front, as
// http.Error does: the error body goes through that writer, so a client
// decoding by the label reads it.
func TestDefaultErrorHandler_CompressingWriterKeepsContentEncoding(t *testing.T) {
	r := New()
	r.Get("/fail", func(c *Context) error { return errors.New("db down") })
	srv := httptest.NewServer(compressAll(r))
	t.Cleanup(srv.Close)

	tests := []struct {
		name     string
		accept   string
		wantType string
	}{
		{name: "problem json", accept: "application/json", wantType: "application/problem+json"},
		{name: "plain text", accept: "text/html", wantType: "text/plain; charset=utf-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/fail", nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Accept", tt.accept)
			// Asking for gzip explicitly makes the transport hand back
			// the body exactly as the server coded it.
			req.Header.Set("Accept-Encoding", "gzip")
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", resp.StatusCode)
			}
			if enc := resp.Header.Get("Content-Encoding"); enc != "gzip" {
				t.Fatalf("Content-Encoding = %q, want the wrapping handler's gzip", enc)
			}
			if ct := resp.Header.Get("Content-Type"); ct != tt.wantType {
				t.Errorf("Content-Type = %q, want %q", ct, tt.wantType)
			}
			zr, err := gzip.NewReader(resp.Body)
			if err != nil {
				t.Fatalf("gzip error body: %v", err)
			}
			body, err := io.ReadAll(zr)
			if err != nil {
				t.Fatalf("decode gzip error body: %v", err)
			}
			if !strings.Contains(string(body), "Internal Server Error") {
				t.Errorf("decoded body = %q, want the 500 answer", body)
			}
		})
	}
}

// lazyGzipWriter labels the response gzip and starts compressing on its
// first Write, so a response with nothing written carries no label.
type lazyGzipWriter struct {
	http.ResponseWriter
	zw *gzip.Writer
}

func (g *lazyGzipWriter) Write(p []byte) (int, error) {
	if g.zw == nil {
		g.Header().Set("Content-Encoding", "gzip")
		g.zw = gzip.NewWriter(g.ResponseWriter)
	}
	return g.zw.Write(p)
}

// lazyCompress is a velocity middleware that swaps c.Response for a
// lazyGzipWriter and finishes the stream when one was started.
func lazyCompress(next HandlerFunc) HandlerFunc {
	return func(c *Context) error {
		gw := &lazyGzipWriter{ResponseWriter: c.Response}
		c.Response = gw
		err := next(c)
		if gw.zw != nil {
			if cerr := gw.zw.Close(); err == nil {
				err = cerr
			}
		}
		return err
	}
}

// TestDefaultErrorHandler_LazyLabelCompressorLeavesErrorPlain asserts the
// supported design for a compressing middleware that swaps c.Response: it
// labels on its first write, so a handler that fails before writing leaves
// no label, and the boundary's body, written to the router's own writer,
// arrives plain and readable.
func TestDefaultErrorHandler_LazyLabelCompressorLeavesErrorPlain(t *testing.T) {
	r := New()
	r.Use(lazyCompress)
	r.Get("/fail", func(c *Context) error { return errors.New("db down") })
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	tests := []struct {
		name     string
		accept   string
		wantType string
	}{
		{name: "problem json", accept: "application/json", wantType: "application/problem+json"},
		{name: "plain text", accept: "text/html", wantType: "text/plain; charset=utf-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/fail", nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Accept", tt.accept)
			// Asking for gzip explicitly makes the transport hand back
			// the body exactly as the server coded it.
			req.Header.Set("Accept-Encoding", "gzip")
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if resp.StatusCode != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", resp.StatusCode)
			}
			if enc := resp.Header.Get("Content-Encoding"); enc != "" {
				t.Fatalf("Content-Encoding = %q, want none: nothing went through the compressor (body %.80q)", enc, body)
			}
			if ct := resp.Header.Get("Content-Type"); ct != tt.wantType {
				t.Errorf("Content-Type = %q, want %q", ct, tt.wantType)
			}
			if !strings.Contains(string(body), "Internal Server Error") {
				t.Errorf("body = %q, want the plain 500 answer", body)
			}
		})
	}
}
