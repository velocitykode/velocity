package bond

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"render blew up","type":"render","hint":"check the page component","sourceLocation":"Auth/Login.tsx:42"}`)
	}))
	defer server.Close()

	gw := NewHTTPGateway(server.URL)
	events := collectSSREvents(gw)

	resp, err := gw.Dispatch(context.Background(), Page{Component: "Home", URL: "/login"})
	if err != nil {
		t.Fatalf("dispatch returned error, expected graceful fallback: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil response on failure, got %+v", resp)
	}
	got := events()
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	if got[0].Error != "render blew up" {
		t.Errorf("error passthrough: got %q", got[0].Error)
	}
	if got[0].Type != SSRErrorRender {
		t.Errorf("expected type render, got %q", got[0].Type)
	}
	if got[0].Hint != "check the page component" {
		t.Errorf("hint passthrough: got %q", got[0].Hint)
	}
	if got[0].SourceLocation != "Auth/Login.tsx:42" {
		t.Errorf("sourceLocation passthrough: got %q", got[0].SourceLocation)
	}
	if got[0].Component != "Home" || got[0].URL != "/login" {
		t.Errorf("component/url not propagated: %+v", got[0])
	}
}

func TestHTTPGateway_Dispatch_Unreachable_FallsBack(t *testing.T) {
	gw := NewHTTPGateway("http://127.0.0.1:1")
	gw.Timeout = 500 * time.Millisecond
	gw.Client.Timeout = 500 * time.Millisecond
	events := collectSSREvents(gw)

	resp, err := gw.Dispatch(context.Background(), Page{Component: "Home"})
	if err != nil {
		t.Fatalf("expected graceful fallback, got error: %v", err)
	}
	if resp != nil {
		t.Error("expected nil response")
	}
	got := events()
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	if got[0].Type != SSRErrorConnection {
		t.Errorf("expected connection error type, got %q", got[0].Type)
	}
}

func TestHTTPGateway_Dispatch_ThrowOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	gw := NewHTTPGateway(server.URL)
	gw.ThrowOnError = true

	resp, err := gw.Dispatch(context.Background(), Page{Component: "Home"})
	if err == nil {
		t.Fatal("expected ThrowOnError to surface a non-nil error")
	}
	if resp != nil {
		t.Error("expected nil response alongside error")
	}
}

// collectSSREvents wires a test dispatcher onto gw and returns a
// snapshot accessor. Safe for concurrent writes from the gateway and
// test-thread reads.
func collectSSREvents(gw *HTTPGateway) func() []SSRRenderFailed {
	var (
		mu     sync.Mutex
		events []SSRRenderFailed
	)
	gw.SetEventDispatcher(func(_ context.Context, evt interface{}) error {
		if f, ok := evt.(SSRRenderFailed); ok {
			mu.Lock()
			events = append(events, f)
			mu.Unlock()
		}
		return nil
	})
	return func() []SSRRenderFailed {
		mu.Lock()
		defer mu.Unlock()
		return append([]SSRRenderFailed(nil), events...)
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

	// v3 SSR bodies are self-contained: they ship the data-page JSON
	// script tag alongside the rendered container. Bond must emit them
	// as-is, not re-wrap them.
	ssrBody := `<script data-page="app" type="application/json">{"component":"Home"}</script>` +
		`<div data-server-rendered="true" id="app"><h1>SSR Hello</h1></div>`

	b.SetSSRGateway(&fakeGateway{
		resp: &SSRResponse{
			Head: []string{"<title>Home</title>"},
			Body: ssrBody,
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
	if !strings.Contains(body, `data-server-rendered="true"`) {
		t.Error("expected SSR marker preserved")
	}
	// Guard against nested id="app" — bond must not wrap the self-
	// contained SSR body in its own container div.
	if strings.Count(body, `id="app"`) != 1 {
		t.Errorf("expected exactly one id=\"app\" element, got %d in %q", strings.Count(body, `id="app"`), body)
	}
}

func TestBond_RenderHTML_SSRFailure_FallsBackToCSR(t *testing.T) {
	b, err := New(Config{RootTemplate: testRootTemplate})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Gateway returning (nil, nil) models a graceful failure — the
	// concrete HTTPGateway does this by default when ThrowOnError is
	// off. renderHTML must then emit the CSR container.
	b.SetSSRGateway(&fakeGateway{})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	page := Page{Component: "Home", URL: "/", Version: "1"}

	if err := b.renderHTML(r.Context(), w, page); err != nil {
		t.Fatalf("renderHTML should not surface SSR fallback: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, `<div id="app" data-page='`) {
		t.Error("expected CSR container on SSR fallback")
	}
	if strings.Contains(body, `<title>`) {
		t.Error("expected no SSR head content on fallback")
	}
}

func TestBond_RenderHTML_SSRThrowOnError(t *testing.T) {
	b, err := New(Config{RootTemplate: testRootTemplate})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	boom := errors.New("ssr blew up")
	b.SetSSRGateway(&fakeGateway{err: boom})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	page := Page{Component: "Home", URL: "/"}

	if err := b.renderHTML(r.Context(), w, page); err != boom {
		t.Fatalf("expected ThrowOnError passthrough, got %v", err)
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

func TestHTTPGateway_RejectsPrivateTarget_WhenForbidden(t *testing.T) {
	// With WithAllowPrivate(false), constructing a gateway against a
	// loopback URL must zero out the URL so Dispatch skips SSR.
	gw := NewHTTPGateway("http://127.0.0.1:13714/render", WithAllowPrivate(false))
	if gw.URL != "" {
		t.Errorf("expected URL cleared when private target forbidden, got %q", gw.URL)
	}

	resp, err := gw.Dispatch(context.Background(), Page{Component: "Home", URL: "/"})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp != nil {
		t.Error("expected nil response when URL is zeroed by private-target rejection")
	}
}

func TestHTTPGateway_RejectsMetadataTarget_WhenForbidden(t *testing.T) {
	gw := NewHTTPGateway("http://169.254.169.254/render", WithAllowPrivate(false))
	if gw.URL != "" {
		t.Errorf("expected URL cleared for metadata IP, got %q", gw.URL)
	}
}

func TestHTTPGateway_AllowsPrivateTarget_ByDefault(t *testing.T) {
	// Default behaviour preserves the conventional 127.0.0.1 SSR deployment.
	gw := NewHTTPGateway("http://127.0.0.1:13714/render")
	if gw.URL == "" {
		t.Error("expected URL preserved when AllowPrivate defaults to true")
	}
}

func TestBond_SSR_ForbidPrivateTarget(t *testing.T) {
	b, err := New(Config{
		RootTemplate: testRootTemplate,
		SSR: SSRConfig{
			Enabled:             true,
			URL:                 "http://127.0.0.1:13714/render",
			ForbidPrivateTarget: true,
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw, ok := b.ssr.(*HTTPGateway)
	if !ok {
		t.Fatalf("expected *HTTPGateway, got %T", b.ssr)
	}
	if gw.URL != "" {
		t.Errorf("expected private target rejected, got URL=%q", gw.URL)
	}
}

// TestHTTPGateway_Dispatch_OversizedResponse_RefusedNotTruncated pins the
// ssrResponseCap guard. A misbehaving (or attacker-controlled) SSR server
// that streams more than 10 MiB must NOT be silently truncated into a
// JSON-parse-failure path: the gateway should refuse the payload and
// surface ErrSSRResponseTooLarge via the event stream while CSR-fallback
// returns (nil, nil) for the caller.
func TestHTTPGateway_Dispatch_OversizedResponse_RefusedNotTruncated(t *testing.T) {
	// Build a valid JSON envelope larger than ssrResponseCap. The body
	// field is padded so the total response exceeds 10 MiB.
	pad := strings.Repeat("a", int(ssrResponseCap)+1)
	payload := `{"head":[],"body":"` + pad + `"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()

	gw := NewHTTPGateway(server.URL)
	events := collectSSREvents(gw)

	resp, err := gw.Dispatch(context.Background(), Page{Component: "Home", URL: "/"})
	if err != nil {
		t.Fatalf("dispatch returned error, expected graceful fallback: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil response on oversized body, got %+v", resp)
	}
	got := events()
	if len(got) != 1 {
		t.Fatalf("expected 1 SSRRenderFailed event, got %d", len(got))
	}
	if got[0].Error != ErrSSRResponseTooLarge.Error() {
		t.Errorf("expected ErrSSRResponseTooLarge event, got %q", got[0].Error)
	}
	if got[0].Type != SSRErrorConnection {
		t.Errorf("expected connection error type, got %q", got[0].Type)
	}
}

// TestHTTPGateway_Dispatch_OversizedResponse_ThrowOnError pins the
// ThrowOnError path for the cap. ErrSSRResponseTooLarge must propagate
// out instead of being swallowed for CSR fallback.
func TestHTTPGateway_Dispatch_OversizedResponse_ThrowOnError(t *testing.T) {
	pad := strings.Repeat("a", int(ssrResponseCap)+1)
	payload := `{"head":[],"body":"` + pad + `"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()

	gw := NewHTTPGateway(server.URL)
	gw.ThrowOnError = true

	resp, err := gw.Dispatch(context.Background(), Page{Component: "Home", URL: "/"})
	if err == nil {
		t.Fatal("expected ThrowOnError to surface a non-nil error")
	}
	if !errors.Is(err, ErrSSRResponseTooLarge) {
		t.Errorf("expected ErrSSRResponseTooLarge, got %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil response alongside error, got %+v", resp)
	}
}

// TestHTTPGateway_Dispatch_AtCap_StillSucceeds pins the lower boundary:
// a response of exactly ssrResponseCap bytes parses normally. The +1
// read guards against silent truncation but must not refuse a payload
// that fits.
func TestHTTPGateway_Dispatch_AtCap_StillSucceeds(t *testing.T) {
	// Compute pad length so the total JSON envelope is exactly
	// ssrResponseCap bytes. The envelope is `{"head":[],"body":"<pad>"}`
	// (no trailing newline). Length is 26 wrapper bytes + len(pad).
	envelope := `{"head":[],"body":"` + `"}`
	padLen := int(ssrResponseCap) - len(envelope)
	if padLen <= 0 {
		t.Fatalf("test invariant: ssrResponseCap too small to fit envelope wrapper")
	}
	pad := strings.Repeat("a", padLen)
	payload := `{"head":[],"body":"` + pad + `"}`
	if int64(len(payload)) != ssrResponseCap {
		t.Fatalf("test invariant: payload length %d, want %d", len(payload), ssrResponseCap)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()

	gw := NewHTTPGateway(server.URL)
	resp, err := gw.Dispatch(context.Background(), Page{Component: "Home", URL: "/"})
	if err != nil {
		t.Fatalf("unexpected error at exactly cap: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response at exactly cap")
	}
	if resp.Body != pad {
		t.Errorf("body mismatch at cap boundary")
	}
}
