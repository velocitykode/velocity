package problem

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

func TestMiddleware_RecoversPanic(t *testing.T) {
	tests := []struct {
		name  string
		serve func(h *Handler, w http.ResponseWriter, r *http.Request)
	}{
		{"Middleware", func(h *Handler, w http.ResponseWriter, r *http.Request) {
			Middleware(h)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(NotFound()) })).ServeHTTP(w, r)
		}},
		{"MiddlewareFunc", func(h *Handler, w http.ResponseWriter, r *http.Request) {
			MiddlewareFunc(h)(func(http.ResponseWriter, *http.Request) { panic("boom") })(w, r)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rep, _ := newTestHandler()
			w := httptest.NewRecorder()
			tt.serve(h, w, httptest.NewRequest(http.MethodGet, "/x", nil))
			if w.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500 for a panic", w.Code)
			}
			ctx, err := rep.last()
			if err == nil || panicerr.AsTyped(err) == nil {
				t.Fatalf("reported %v, want the recovered panic", err)
			}
			if !ctx.Recovered || ctx.PanicStack == "" || ctx.StackTrace == nil {
				t.Errorf("panic context incomplete: recovered=%v stack=%d", ctx.Recovered, len(ctx.PanicStack))
			}
		})
	}
}

func TestMiddleware_PassesThroughWithoutPanic(t *testing.T) {
	h, rep, _ := newTestHandler()
	w := httptest.NewRecorder()
	Middleware(h)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusNoContent || rep.count() != 0 {
		t.Errorf("got %d with %d reports", w.Code, rep.count())
	}
}

func TestMiddleware_AbortHandlerPropagates(t *testing.T) {
	h, rep, _ := newTestHandler()
	defer func() {
		p := recover()
		err, ok := p.(error)
		if !ok || !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", p)
		}
		if rep.count() != 0 {
			t.Error("ErrAbortHandler must not be reported")
		}
	}()
	Middleware(h)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
}

func TestErrorHandler_Adapter(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(h *Handler)
		path        string
		headers     []string
		err         error
		wantStatus  int
		wantContent string
	}{
		{name: "HTMLForBrowser", path: "/api/users", err: NotFound(), wantStatus: 404, wantContent: "text/html; charset=utf-8"},
		{name: "JSONContentTypeRequestIsNotJSONAccept", path: "/x", headers: []string{"Content-Type", "application/json"}, err: NotFound(), wantStatus: 404, wantContent: "text/html; charset=utf-8"},
		{name: "AcceptJSON", path: "/x", headers: []string{"Accept", "application/json"}, err: BadRequest(), wantStatus: 400, wantContent: ProblemTypeContent},
		{name: "ConfiguredPrefix", setup: func(h *Handler) { h.SetAPIPrefixes("/api/") }, path: "/api/users", err: NotFound(), wantStatus: 404, wantContent: ProblemTypeContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHandler()
			if tt.setup != nil {
				tt.setup(h)
			}
			r := httptest.NewRequest(http.MethodGet, tt.path, nil)
			for i := 0; i+1 < len(tt.headers); i += 2 {
				r.Header.Set(tt.headers[i], tt.headers[i+1])
			}
			w := httptest.NewRecorder()
			ErrorHandler(h)(w, r, tt.err)
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if got := w.Header().Get("Content-Type"); got != tt.wantContent {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantContent)
			}
		})
	}
}

func TestErrorHandler_ClientIPUsesTrustedProxies(t *testing.T) {
	_, trusted, _ := net.ParseCIDR("192.0.2.0/24")
	tests := []struct {
		name    string
		proxies []*net.IPNet
		want    string
	}{
		{"UntrustedIgnoresHeader", nil, "192.0.2.1"},
		{"TrustedHonoursHeader", []*net.IPNet{trusted}, "203.0.113.9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rep, _ := newTestHandler(WithTrustedProxies(tt.proxies))
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			r.Header.Set("X-Forwarded-For", "203.0.113.9")
			ErrorHandler(h)(httptest.NewRecorder(), r, errors.New("boom"))
			ctx, _ := rep.last()
			if ctx.IP != tt.want {
				t.Errorf("IP = %q, want %q", ctx.IP, tt.want)
			}
		})
	}
}

func TestErrorHandler_AcceptsInterface(t *testing.T) {
	fake := NewFakeHandler()
	w := httptest.NewRecorder()
	ErrorHandler(fake)(w, httptest.NewRequest(http.MethodGet, "/x", nil), Forbidden())
	if w.Code != http.StatusForbidden || len(fake.ReportedErrors()) != 1 || len(fake.RenderedErrors()) != 1 {
		t.Errorf("fake handler not driven: %d %v %v", w.Code, fake.Reported, fake.Rendered)
	}
}

// A panic after the handler committed its response is reported once and
// renders nothing over the committed body.
func TestMiddleware_PanicAfterCommitReportsOnly(t *testing.T) {
	handlers := []struct {
		name     string
		handler  func(w http.ResponseWriter)
		wantBody string
	}{
		{"after WriteHeader and body", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("partial"))
			panic("late")
		}, "partial"},
		{"after implicit write", func(w http.ResponseWriter) {
			_, _ = w.Write([]byte("partial"))
			panic("late")
		}, "partial"},
		{"after Flush", func(w http.ResponseWriter) {
			w.(http.Flusher).Flush()
			panic("late")
		}, ""},
		{"after ResponseController flush", func(w http.ResponseWriter) {
			if err := http.NewResponseController(w).Flush(); err != nil {
				panic(err)
			}
			panic("late")
		}, ""},
	}
	adapters := []struct {
		name string
		wrap func(h *Handler, fn func(http.ResponseWriter)) http.Handler
	}{
		{"Middleware", func(h *Handler, fn func(http.ResponseWriter)) http.Handler {
			return Middleware(h)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fn(w) }))
		}},
		{"MiddlewareFunc", func(h *Handler, fn func(http.ResponseWriter)) http.Handler {
			return MiddlewareFunc(h)(func(w http.ResponseWriter, _ *http.Request) { fn(w) })
		}},
	}
	for _, a := range adapters {
		for _, tt := range handlers {
			t.Run(a.name+" "+tt.name, func(t *testing.T) {
				h, rep, _ := newTestHandler()
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/x", nil)
				req.Header.Set("Accept", "application/json")
				a.wrap(h, tt.handler).ServeHTTP(w, req)
				if w.Code != http.StatusOK {
					t.Errorf("status = %d, want 200", w.Code)
				}
				if got := w.Body.String(); got != tt.wantBody {
					t.Errorf("body = %q, want exactly %q", got, tt.wantBody)
				}
				if rep.count() != 1 {
					t.Errorf("reports = %d, want 1", rep.count())
				}
			})
		}
	}
}

// Through a real server a panic after a committed 200 leaves the client
// exactly the handler's bytes.
func TestMiddleware_PanicAfterCommitOverRealServer(t *testing.T) {
	h, rep, _ := newTestHandler()
	srv := httptest.NewServer(Middleware(h)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
		panic("late")
	})))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/x", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != `{"ok":true}` {
		t.Errorf("response = %d %q, want 200 with exactly the handler's body", resp.StatusCode, body)
	}
	if rep.count() != 1 {
		t.Errorf("reports = %d, want 1", rep.count())
	}
}

// The returned-error adapter honours a TrackedWriter the handler wrote
// through.
func TestErrorHandler_TrackedWriterAfterCommit(t *testing.T) {
	h, rep, _ := newTestHandler()
	rec := httptest.NewRecorder()
	tw := NewTrackedWriter(rec)
	tw.WriteHeader(http.StatusOK)
	_, _ = tw.Write([]byte("done"))

	ErrorHandler(h)(tw, httptest.NewRequest(http.MethodGet, "/x", nil), errors.New("late"))

	if rec.Code != http.StatusOK || rec.Body.String() != "done" {
		t.Errorf("response = %d %q, want the committed 200 untouched", rec.Code, rec.Body.String())
	}
	if rep.count() != 1 {
		t.Errorf("reports = %d, want 1", rep.count())
	}
}

func TestTrackedWriter_Commitment(t *testing.T) {
	tests := []struct {
		name string
		act  func(tw *TrackedWriter)
		want bool
	}{
		{"nothing", func(*TrackedWriter) {}, false},
		{"header only", func(tw *TrackedWriter) { tw.Header().Set("X-A", "1") }, false},
		{"informational", func(tw *TrackedWriter) { tw.WriteHeader(http.StatusEarlyHints) }, false},
		{"switching protocols", func(tw *TrackedWriter) { tw.WriteHeader(http.StatusSwitchingProtocols) }, true},
		{"final status", func(tw *TrackedWriter) { tw.WriteHeader(http.StatusNoContent) }, true},
		{"write", func(tw *TrackedWriter) { _, _ = tw.Write([]byte("x")) }, true},
		{"flush", func(tw *TrackedWriter) { tw.Flush() }, true},
		{"response controller flush", func(tw *TrackedWriter) { _ = http.NewResponseController(tw).Flush() }, true},
		{"failed hijack", func(tw *TrackedWriter) { _, _, _ = tw.Hijack() }, false},
		{"push", func(tw *TrackedWriter) { _ = tw.Push("/a.css", nil) }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tw := NewTrackedWriter(rec)
			tt.act(tw)
			if got := tw.Committed(); got != tt.want {
				t.Errorf("Committed() = %v, want %v", got, tt.want)
			}
			if got := contract.NewRenderContext(tw, httptest.NewRequest(http.MethodGet, "/", nil)).Written(); got != tt.want {
				t.Errorf("RenderContext.Written() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTrackedWriter_PassThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	tw := NewTrackedWriter(rec)
	if tw.Unwrap() != rec {
		t.Error("Unwrap must return the wrapped writer")
	}
	if err := tw.Push("/a.css", nil); !errors.Is(err, http.ErrNotSupported) {
		t.Errorf("Push over a non-pusher = %v, want http.ErrNotSupported", err)
	}
	if _, _, err := tw.Hijack(); !errors.Is(err, http.ErrNotSupported) {
		t.Errorf("Hijack over a non-hijacker = %v, want http.ErrNotSupported", err)
	}
	tw.Flush()
	if !rec.Flushed {
		t.Error("Flush must reach the wrapped writer")
	}

	hijacked := make(chan bool, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tw := NewTrackedWriter(w)
		conn, buf, err := tw.Hijack()
		if err != nil {
			hijacked <- false
			return
		}
		hijacked <- tw.Committed()
		_, _ = buf.WriteString("HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n")
		_ = buf.Flush()
		_ = conn.Close()
	}))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if !<-hijacked {
		t.Error("a successful Hijack must pass through and mark the response committed")
	}
}
