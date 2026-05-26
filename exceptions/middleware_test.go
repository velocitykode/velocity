package exceptions

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/internal/clientip"
)

func TestNewHTTPRenderContext(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)

	ctx := newHTTPRenderContext(w, r)

	if ctx == nil {
		t.Fatal("newHTTPRenderContext returned nil")
	}
	if ctx.w != w {
		t.Error("ResponseWriter not set")
	}
	if ctx.r != r {
		t.Error("Request not set")
	}
}

func TestHTTPRenderContext_WriteHeader(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	ctx := newHTTPRenderContext(w, r)

	ctx.WriteHeader(http.StatusNotFound)

	if w.Code != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want %d", w.Code, http.StatusNotFound)
	}

	// Second call should be ignored
	ctx.WriteHeader(http.StatusOK)
	if w.Code != http.StatusNotFound {
		t.Error("Second WriteHeader should be ignored")
	}
}

func TestHTTPRenderContext_Write(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	ctx := newHTTPRenderContext(w, r)

	n, err := ctx.Write([]byte("test"))

	if err != nil {
		t.Errorf("Write() error = %v", err)
	}
	if n != 4 {
		t.Errorf("Write() n = %d, want 4", n)
	}
	if w.Body.String() != "test" {
		t.Error("Data not written")
	}
}

func TestHTTPRenderContext_SetHeader(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	ctx := newHTTPRenderContext(w, r)

	ctx.SetHeader("X-Custom", "value")

	if w.Header().Get("X-Custom") != "value" {
		t.Error("Header not set")
	}
}

func TestHTTPRenderContext_GetHeader(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Accept", "application/json")
	ctx := newHTTPRenderContext(w, r)

	if ctx.GetHeader("Accept") != "application/json" {
		t.Error("GetHeader did not return correct value")
	}
}

func TestHTTPRenderContext_RequestPath(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/users", nil)
	ctx := newHTTPRenderContext(w, r)

	if ctx.RequestPath() != "/api/users" {
		t.Errorf("RequestPath() = %q, want /api/users", ctx.RequestPath())
	}
}

func TestHTTPRenderContext_RequestMethod(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/test", nil)
	ctx := newHTTPRenderContext(w, r)

	if ctx.RequestMethod() != "POST" {
		t.Errorf("RequestMethod() = %q, want POST", ctx.RequestMethod())
	}
}

func TestHTTPRenderContext_WantsJSON(t *testing.T) {
	tests := []struct {
		name         string
		accept       string
		contentType  string
		xRequestWith string
		path         string
		want         bool
	}{
		{"accept json", "application/json", "", "", "/", true},
		{"content-type json", "", "application/json", "", "/", true},
		{"xhr request", "", "", "XMLHttpRequest", "/", true},
		{"api path", "", "", "", "/api/users", true},
		{"html accept", "text/html", "", "", "/", false},
		{"no indicators", "", "", "", "/", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", tt.path, nil)
			if tt.accept != "" {
				r.Header.Set("Accept", tt.accept)
			}
			if tt.contentType != "" {
				r.Header.Set("Content-Type", tt.contentType)
			}
			if tt.xRequestWith != "" {
				r.Header.Set("X-Requested-With", tt.xRequestWith)
			}

			ctx := newHTTPRenderContext(w, r)

			if got := ctx.WantsJSON(); got != tt.want {
				t.Errorf("WantsJSON() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMiddleware(t *testing.T) {
	h := NewHandler(WithDebug(false))
	middleware := Middleware(h)

	tests := []struct {
		name       string
		handler    http.Handler
		wantPanic  bool
		wantStatus int
	}{
		{
			name: "normal handler",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}),
			wantPanic:  false,
			wantStatus: http.StatusOK,
		},
		{
			name: "panic handler",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				panic("test panic")
			}),
			wantPanic:  true,
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapped := middleware(tt.handler)

			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/test", nil)
			r.Header.Set("Accept", "application/json")

			// Should not panic
			wrapped.ServeHTTP(w, r)

			if w.Code != tt.wantStatus {
				t.Errorf("StatusCode = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestMiddlewareFunc(t *testing.T) {
	h := NewHandler(WithDebug(false))
	middleware := MiddlewareFunc(h)

	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantStatus int
	}{
		{
			name: "normal handler",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "panic handler",
			handler: func(w http.ResponseWriter, r *http.Request) {
				panic("test panic")
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapped := middleware(tt.handler)

			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/test", nil)
			r.Header.Set("Accept", "application/json")

			wrapped(w, r)

			if w.Code != tt.wantStatus {
				t.Errorf("StatusCode = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestRecoverMiddleware(t *testing.T) {
	h := NewHandler(WithDebug(false))
	middleware := Middleware(h)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})

	wrapped := middleware(handler)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Accept", "application/json")

	wrapped.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestErrorHandler(t *testing.T) {
	var reportedErr error
	mockReporter := NewCallbackReporter(func(err error, ctx *ExceptionContext) {
		reportedErr = err
	})

	h := NewHandler(WithReporters(mockReporter))
	errorHandler := ErrorHandler(h)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Accept", "application/json")

	testErr := NewNotFoundHttpException("Resource not found")
	errorHandler(w, r, testErr)

	if reportedErr != nil {
		t.Error("404 errors should not be reported")
	}
	if w.Code != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want %d", w.Code, http.StatusNotFound)
	}
}

// TestGetClientIP_SecureDefault confirms the post-C-05 behaviour:
// without any trusted-proxy list installed, X-Forwarded-For and
// X-Real-IP are IGNORED. Spoofed headers from a direct-internet client
// can no longer poison the audit log. RemoteAddr is the only source.
func TestGetClientIP_SecureDefault(t *testing.T) {
	tests := []struct {
		name          string
		xForwardedFor string
		xRealIP       string
		remoteAddr    string
		want          string
	}{
		{
			name:       "remote addr with port",
			remoteAddr: "192.168.1.3:12345",
			want:       "192.168.1.3",
		},
		{
			name:       "ipv6 with brackets+port",
			remoteAddr: "[2001:db8::1]:443",
			want:       "2001:db8::1",
		},
		{
			name:          "spoofed XFF ignored when no trusted proxies configured",
			xForwardedFor: "8.8.8.8",
			remoteAddr:    "203.0.113.9:54321",
			want:          "203.0.113.9",
		},
		{
			name:          "spoofed XFF with multiple hops ignored",
			xForwardedFor: "8.8.8.8, 1.2.3.4, 5.6.7.8",
			remoteAddr:    "203.0.113.9:54321",
			want:          "203.0.113.9",
		},
		{
			name:       "spoofed X-Real-IP ignored",
			xRealIP:    "8.8.8.8",
			remoteAddr: "203.0.113.9:54321",
			want:       "203.0.113.9",
		},
		{
			name:          "spoofed X-Forwarded-For + X-Real-IP both ignored",
			xForwardedFor: "10.0.0.1",
			xRealIP:       "10.0.0.2",
			remoteAddr:    "10.0.0.3:1234",
			want:          "10.0.0.3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			if tt.xForwardedFor != "" {
				r.Header.Set("X-Forwarded-For", tt.xForwardedFor)
			}
			if tt.xRealIP != "" {
				r.Header.Set("X-Real-IP", tt.xRealIP)
			}
			if tt.remoteAddr != "" {
				r.RemoteAddr = tt.remoteAddr
			}

			// nil trusted-proxy list: forwarded headers MUST be ignored.
			got := getClientIP(r, nil)
			if got != tt.want {
				t.Errorf("getClientIP() = %q, want %q (spoofed header leaked into audit log)", got, tt.want)
			}
		})
	}
}

// TestGetClientIP_HonorsTrustedProxies confirms that when a trusted-
// proxy list IS installed and the direct peer is in it, forwarded
// headers ARE honoured via clientip.Extract's right-most-of-trusted
// semantics. This is the explicit opt-in for load-balancer
// deployments.
func TestGetClientIP_HonorsTrustedProxies(t *testing.T) {
	trusted, err := clientip.ParseCIDRs([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("ParseCIDRs: %v", err)
	}

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:443"
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.2")

	if got := getClientIP(r, trusted); got != "203.0.113.9" {
		t.Errorf("getClientIP = %q, want %q", got, "203.0.113.9")
	}

	// Spoofed XFF from an UNTRUSTED direct peer must still be ignored
	// even when a trust list is installed.
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.RemoteAddr = "203.0.113.9:54321"
	r2.Header.Set("X-Forwarded-For", "8.8.8.8")
	if got := getClientIP(r2, trusted); got != "203.0.113.9" {
		t.Errorf("spoofed XFF from untrusted peer leaked: got %q, want %q", got, "203.0.113.9")
	}
}

// TestErrorHandler_RecordsRealClientIP_NotSpoofedXFF wires the
// Handler-side setter end-to-end: a deployment with NO trusted proxies
// (the default after `velocity.New` on a direct-internet host) must
// record RemoteAddr on the ExceptionContext, even when the attacker
// sends X-Forwarded-For. This is the regression pin for the audit
// finding (log poisoning / forensics evasion).
func TestErrorHandler_RecordsRealClientIP_NotSpoofedXFF(t *testing.T) {
	var captured *ExceptionContext
	h := NewHandler(WithReporters(NewCallbackReporter(func(_ error, exCtx *ExceptionContext) {
		captured = exCtx
	})))

	// No SetTrustedProxies call: handler defaults to no trust.
	eh := ErrorHandler(h)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.9:54321"
	r.Header.Set("X-Forwarded-For", "8.8.8.8")
	r.Header.Set("X-Real-IP", "9.9.9.9")

	eh(w, r, NewInternalServerErrorException("boom"))

	if captured == nil {
		t.Fatal("no exception context captured")
	}
	if captured.IP != "203.0.113.9" {
		t.Fatalf("ExceptionContext.IP = %q, want %q (spoofed XFF/X-Real-IP leaked into audit log)", captured.IP, "203.0.113.9")
	}
}

// TestHandler_SetTrustedProxies_DefensiveCopy: caller mutation of the
// slice handed in must not affect the handler's view.
func TestHandler_SetTrustedProxies_DefensiveCopy(t *testing.T) {
	h := NewHandler()
	proxies, _ := clientip.ParseCIDRs([]string{"10.0.0.0/8"})
	h.SetTrustedProxies(proxies)

	for i := range proxies {
		proxies[i] = nil
	}

	got := h.getTrustedProxies()
	if len(got) != 1 || got[0] == nil {
		t.Fatalf("handler observed caller mutation: %v", got)
	}
}

// TestHandler_SetTrustedProxies_NilClears: passing nil reverts to the
// secure default (no trust).
func TestHandler_SetTrustedProxies_NilClears(t *testing.T) {
	h := NewHandler()
	proxies, _ := clientip.ParseCIDRs([]string{"10.0.0.0/8"})
	h.SetTrustedProxies(proxies)
	h.SetTrustedProxies(nil)
	if got := h.getTrustedProxies(); got != nil {
		t.Fatalf("expected nil after clear, got %v", got)
	}
}

func TestVelocityContextAdapter(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/test", nil)
	r.Header.Set("Accept", "application/json")
	r.Header.Set("Content-Type", "application/json")

	adapter := NewVelocityContextAdapter(w, r)

	// Test WriteHeader
	adapter.WriteHeader(http.StatusCreated)
	if w.Code != http.StatusCreated {
		t.Errorf("WriteHeader: got %d, want %d", w.Code, http.StatusCreated)
	}

	// Test Write
	n, err := adapter.Write([]byte("test"))
	if err != nil || n != 4 {
		t.Error("Write failed")
	}

	// Test SetHeader
	adapter.SetHeader("X-Custom", "value")
	if w.Header().Get("X-Custom") != "value" {
		t.Error("SetHeader failed")
	}

	// Test GetHeader
	if adapter.GetHeader("Accept") != "application/json" {
		t.Error("GetHeader failed")
	}

	// Test RequestPath
	if adapter.RequestPath() != "/api/test" {
		t.Errorf("RequestPath() = %q, want /api/test", adapter.RequestPath())
	}

	// Test RequestMethod
	if adapter.RequestMethod() != "POST" {
		t.Errorf("RequestMethod() = %q, want POST", adapter.RequestMethod())
	}

	// Test WantsJSON
	if !adapter.WantsJSON() {
		t.Error("WantsJSON should return true")
	}
}

func TestVelocityContextAdapter_WantsJSON(t *testing.T) {
	tests := []struct {
		name         string
		accept       string
		contentType  string
		xRequestWith string
		path         string
		want         bool
	}{
		{"accept json", "application/json", "", "", "/", true},
		{"content-type json", "", "application/json", "", "/", true},
		{"xhr", "", "", "XMLHttpRequest", "/", true},
		{"api path", "", "", "", "/api/users", true},
		{"html", "text/html", "", "", "/", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", tt.path, nil)
			if tt.accept != "" {
				r.Header.Set("Accept", tt.accept)
			}
			if tt.contentType != "" {
				r.Header.Set("Content-Type", tt.contentType)
			}
			if tt.xRequestWith != "" {
				r.Header.Set("X-Requested-With", tt.xRequestWith)
			}

			adapter := NewVelocityContextAdapter(w, r)

			if got := adapter.WantsJSON(); got != tt.want {
				t.Errorf("WantsJSON() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVelocityContextAdapter_WriteHeader_OnlyOnce(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	adapter := NewVelocityContextAdapter(w, r)

	adapter.WriteHeader(http.StatusNotFound)
	adapter.WriteHeader(http.StatusOK) // Should be ignored

	if w.Code != http.StatusNotFound {
		t.Error("Second WriteHeader should be ignored")
	}
}
