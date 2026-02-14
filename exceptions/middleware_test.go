package exceptions

import (
	"net/http"
	"net/http/httptest"
	"testing"
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

func TestGetClientIP(t *testing.T) {
	tests := []struct {
		name          string
		xForwardedFor string
		xRealIP       string
		remoteAddr    string
		want          string
	}{
		{
			name:          "x-forwarded-for single",
			xForwardedFor: "192.168.1.1",
			want:          "192.168.1.1",
		},
		{
			name:          "x-forwarded-for multiple",
			xForwardedFor: "192.168.1.1, 10.0.0.1, 172.16.0.1",
			want:          "192.168.1.1",
		},
		{
			name:    "x-real-ip",
			xRealIP: "192.168.1.2",
			want:    "192.168.1.2",
		},
		{
			name:       "remote addr with port",
			remoteAddr: "192.168.1.3:12345",
			want:       "192.168.1.3",
		},
		{
			name:       "remote addr without port",
			remoteAddr: "192.168.1.4",
			want:       "192.168.1.4",
		},
		{
			name:          "priority x-forwarded-for",
			xForwardedFor: "10.0.0.1",
			xRealIP:       "10.0.0.2",
			remoteAddr:    "10.0.0.3:1234",
			want:          "10.0.0.1",
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

			got := getClientIP(r)
			if got != tt.want {
				t.Errorf("getClientIP() = %q, want %q", got, tt.want)
			}
		})
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
