package bond

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testRootTemplate = `<!DOCTYPE html>
<html>
<head>{{ .inertiaHead }}</head>
<body>{{ .inertia }}</body>
</html>`

func TestHTTPGateway_Dispatch_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/render" {
			t.Errorf("expected /render, got %s", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected application/json content-type, got %s", ct)
		}

		var got Page
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if got.Component != "Home" {
			t.Errorf("expected Home component, got %s", got.Component)
		}

		_ = json.NewEncoder(w).Encode(SSRResponse{
			Head: []string{"<title>Home</title>", `<meta name="description" content="hi">`},
			Body: `<div class="rendered">hello</div>`,
		})
	}))
	defer server.Close()

	gw := NewHTTPGateway(server.URL)
	resp, err := gw.Dispatch(context.Background(), Page{Component: "Home", URL: "/"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Body != `<div class="rendered">hello</div>` {
		t.Errorf("unexpected body: %q", resp.Body)
	}
	if len(resp.Head) != 2 {
		t.Errorf("expected 2 head tags, got %d", len(resp.Head))
	}
}

func TestHTTPGateway_Dispatch_ServerError_FallsBack(t *testing.T) {
	var failures int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	gw := NewHTTPGateway(server.URL)
	gw.OnFailure = func(page Page, err error) { atomic.AddInt32(&failures, 1) }

	resp, err := gw.Dispatch(context.Background(), Page{Component: "Home"})
	if err != nil {
		t.Fatalf("dispatch returned error, expected graceful fallback: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil response on failure, got %+v", resp)
	}
	if atomic.LoadInt32(&failures) != 1 {
		t.Errorf("expected OnFailure to be called once, got %d", failures)
	}
}

func TestHTTPGateway_Dispatch_Unreachable_FallsBack(t *testing.T) {
	// 127.0.0.1:1 is reserved and refuses connections fast enough to keep
	// the test quick without depending on timeout behavior.
	gw := NewHTTPGateway("http://127.0.0.1:1")
	gw.Timeout = 500 * time.Millisecond
	gw.Client.Timeout = 500 * time.Millisecond

	called := false
	gw.OnFailure = func(page Page, err error) { called = true }

	resp, err := gw.Dispatch(context.Background(), Page{Component: "Home"})
	if err != nil {
		t.Fatalf("expected graceful fallback, got error: %v", err)
	}
	if resp != nil {
		t.Error("expected nil response")
	}
	if !called {
		t.Error("expected OnFailure to be invoked")
	}
}

func TestHTTPGateway_Dispatch_MalformedJSON_FallsBack(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "not json")
	}))
	defer server.Close()

	gw := NewHTTPGateway(server.URL)
	resp, err := gw.Dispatch(context.Background(), Page{Component: "Home"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Error("expected nil response on malformed JSON")
	}
}

func TestHTTPGateway_Dispatch_ExcludedPath(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer server.Close()

	gw := NewHTTPGateway(server.URL)
	gw.Except = []string{"/admin"}

	resp, err := gw.Dispatch(context.Background(), Page{Component: "Admin", URL: "/admin/users"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Error("expected excluded path to skip SSR")
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Errorf("expected 0 dispatches for excluded path, got %d", calls)
	}
}

func TestHTTPGateway_Dispatch_EmptyURL(t *testing.T) {
	gw := &HTTPGateway{}
	resp, err := gw.Dispatch(context.Background(), Page{Component: "Home"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Error("expected nil response when URL is empty")
	}
}

func TestHTTPGateway_Dispatch_RespectsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block long enough that context cancellation wins.
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	gw := NewHTTPGateway(server.URL)
	gw.Timeout = 100 * time.Millisecond
	gw.Client.Timeout = 100 * time.Millisecond

	resp, err := gw.Dispatch(context.Background(), Page{Component: "Home"})
	if err != nil {
		t.Fatalf("expected graceful fallback on timeout, got: %v", err)
	}
	if resp != nil {
		t.Error("expected nil response on timeout")
	}
}

func TestHTTPGateway_IsHealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	gw := NewHTTPGateway(server.URL)
	ok, err := gw.IsHealthy(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected healthy SSR server")
	}
}

func TestHTTPGateway_IsHealthy_EmptyURL(t *testing.T) {
	gw := &HTTPGateway{}
	if _, err := gw.IsHealthy(context.Background()); err == nil {
		t.Error("expected error for unconfigured gateway")
	}
}

// fakeGateway is an in-memory SSRGateway for Bond-level tests so the
// renderer can be exercised without running an HTTP server.
type fakeGateway struct {
	resp *SSRResponse
	err  error
	seen atomic.Int32
}

func (f *fakeGateway) Dispatch(ctx context.Context, page Page) (*SSRResponse, error) {
	f.seen.Add(1)
	return f.resp, f.err
}

func TestBond_RenderHTML_WithSSR_InjectsBodyAndHead(t *testing.T) {
	b, err := New(Config{RootTemplate: testRootTemplate})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	b.SetSSRGateway(&fakeGateway{
		resp: &SSRResponse{
			Head: []string{"<title>Home</title>"},
			Body: `<h1>SSR Hello</h1>`,
		},
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	page := Page{Component: "Home", Props: Props{}, URL: "/", Version: "1"}

	if err := b.renderHTML(r.Context(), w, page); err != nil {
		t.Fatalf("renderHTML: %v", err)
	}
	body := w.Body.String()

	if !strings.Contains(body, `<h1>SSR Hello</h1>`) {
		t.Errorf("expected SSR body in output, got %q", body)
	}
	if !strings.Contains(body, "<title>Home</title>") {
		t.Errorf("expected SSR head in output, got %q", body)
	}
	if !strings.Contains(body, `data-page='`) {
		t.Error("expected data-page attribute preserved for hydration")
	}
}

func TestBond_RenderHTML_SSRFailure_FallsBackToCSR(t *testing.T) {
	b, err := New(Config{RootTemplate: testRootTemplate})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	b.SetSSRGateway(&fakeGateway{err: errors.New("unreachable")})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	page := Page{Component: "Home", URL: "/", Version: "1"}

	if err := b.renderHTML(r.Context(), w, page); err != nil {
		t.Fatalf("renderHTML should not surface SSR error: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, `<div id="app" data-page='`) {
		t.Error("expected CSR container on SSR failure")
	}
	if strings.Contains(body, `<title>`) {
		t.Error("expected no SSR head content on fallback")
	}
}

func TestBond_New_BuildsSSRGatewayWhenEnabled(t *testing.T) {
	b, err := New(Config{
		RootTemplate: testRootTemplate,
		SSR: SSRConfig{
			Enabled: true,
			URL:     "http://127.0.0.1:13714",
			Timeout: 5 * time.Second,
			Except:  []string{"/admin"},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw, ok := b.ssr.(*HTTPGateway)
	if !ok {
		t.Fatalf("expected *HTTPGateway, got %T", b.ssr)
	}
	if gw.URL != "http://127.0.0.1:13714/render" {
		t.Errorf("URL mismatch: %s", gw.URL)
	}
	if gw.Timeout != 5*time.Second {
		t.Errorf("Timeout mismatch: %s", gw.Timeout)
	}
	if len(gw.Except) != 1 || gw.Except[0] != "/admin" {
		t.Errorf("Except mismatch: %v", gw.Except)
	}
}

func TestBond_New_NoSSRByDefault(t *testing.T) {
	b, err := New(Config{RootTemplate: testRootTemplate})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if b.ssr != nil {
		t.Error("expected no SSR gateway by default")
	}
}
