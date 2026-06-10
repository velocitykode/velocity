package exceptions

import (
	"errors"
	"html/template"
	"net/http"
	"strings"
	"testing"
)

func TestNewJSONRenderer(t *testing.T) {
	r := NewJSONRenderer()
	if r == nil {
		t.Fatal("NewJSONRenderer returned nil")
	}
}

func TestJSONRenderer_ContentType(t *testing.T) {
	r := NewJSONRenderer()
	if r.ContentType() != "application/json" {
		t.Errorf("ContentType() = %q, want %q", r.ContentType(), "application/json")
	}
}

func TestJSONRenderer_Render_HttpException(t *testing.T) {
	r := NewJSONRenderer()
	ctx := &mockRenderContext{headers: make(map[string]string)}
	exCtx := NewExceptionContext().WithIDs("req-123", "trace-456")

	err := NewHttpException(http.StatusBadRequest, "Bad request").
		WithHeader("X-Custom", "value")

	renderErr := r.Render(ctx, err, exCtx, false)

	if renderErr != nil {
		t.Errorf("Render() error = %v", renderErr)
	}
	if ctx.statusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want %d", ctx.statusCode, http.StatusBadRequest)
	}
	if ctx.headers["Content-Type"] != "application/json" {
		t.Error("Content-Type not set")
	}
	if ctx.headers["X-Content-Type-Options"] != "nosniff" {
		t.Error("X-Content-Type-Options not set")
	}
	if ctx.headers["X-Custom"] != "value" {
		t.Error("Custom header not set")
	}

	response := string(ctx.written)
	if !strings.Contains(response, "Bad request") {
		t.Error("Response should contain message")
	}
	if !strings.Contains(response, "req-123") {
		t.Error("Response should contain request_id")
	}
}

func TestJSONRenderer_Render_DebugMode(t *testing.T) {
	r := NewJSONRenderer()
	ctx := &mockRenderContext{headers: make(map[string]string)}
	exCtx := NewExceptionContext().WithStackTrace(CaptureStackTrace(0))

	prev := errors.New("previous error")
	err := NewBaseException("test error", 500).
		WithPrevious(prev).
		WithContext("key", "value")

	renderErr := r.Render(ctx, err, exCtx, true)

	if renderErr != nil {
		t.Errorf("Render() error = %v", renderErr)
	}

	response := string(ctx.written)
	if !strings.Contains(response, "exception") {
		t.Error("Debug response should contain exception type")
	}
	if !strings.Contains(response, "stack_trace") {
		t.Error("Debug response should contain stack trace")
	}
	if !strings.Contains(response, "context") {
		t.Error("Debug response should contain context")
	}
	if !strings.Contains(response, "previous") {
		t.Error("Debug response should contain previous error")
	}
}

func TestJSONRenderer_Render_NonHttpException(t *testing.T) {
	r := NewJSONRenderer()
	ctx := &mockRenderContext{headers: make(map[string]string)}

	err := errors.New("simple error")

	renderErr := r.Render(ctx, err, nil, false)

	if renderErr != nil {
		t.Errorf("Render() error = %v", renderErr)
	}
	if ctx.statusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want %d", ctx.statusCode, http.StatusInternalServerError)
	}
}

func TestJSONRenderer_Render_WithGetStatusCode(t *testing.T) {
	r := NewJSONRenderer()
	ctx := &mockRenderContext{headers: make(map[string]string)}

	err := NewNotFoundHttpException()

	renderErr := r.Render(ctx, err, nil, false)

	if renderErr != nil {
		t.Errorf("Render() error = %v", renderErr)
	}
	if ctx.statusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want %d", ctx.statusCode, http.StatusNotFound)
	}
}

func TestNewHTMLRenderer(t *testing.T) {
	r := NewHTMLRenderer()
	if r == nil {
		t.Fatal("NewHTMLRenderer returned nil")
	}
	if r.debugTemplate == nil {
		t.Error("debugTemplate should be initialized")
	}
	if r.errorTemplate == nil {
		t.Error("errorTemplate should be initialized")
	}
}

func TestNewHTMLRendererWithTemplates(t *testing.T) {
	debugTpl := template.Must(template.New("test").Parse("<html>debug</html>"))
	errorTpl := template.Must(template.New("test").Parse("<html>error</html>"))

	r := NewHTMLRendererWithTemplates(debugTpl, errorTpl)

	if r.debugTemplate != debugTpl {
		t.Error("debugTemplate not set")
	}
	if r.errorTemplate != errorTpl {
		t.Error("errorTemplate not set")
	}
}

func TestHTMLRenderer_ContentType(t *testing.T) {
	r := NewHTMLRenderer()
	if r.ContentType() != "text/html" {
		t.Errorf("ContentType() = %q, want %q", r.ContentType(), "text/html")
	}
}

func TestHTMLRenderer_Render_Production(t *testing.T) {
	r := NewHTMLRenderer()
	ctx := &mockRenderContext{headers: make(map[string]string)}
	exCtx := NewExceptionContext().WithIDs("req-123", "trace-456")

	err := NewHttpException(http.StatusNotFound, "Page not found")

	renderErr := r.Render(ctx, err, exCtx, false)

	if renderErr != nil {
		t.Errorf("Render() error = %v", renderErr)
	}
	if ctx.statusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want %d", ctx.statusCode, http.StatusNotFound)
	}
	if !strings.Contains(ctx.headers["Content-Type"], "text/html") {
		t.Error("Content-Type not set")
	}

	response := string(ctx.written)
	if !strings.Contains(response, "404") {
		t.Error("Response should contain status code")
	}
}

func TestHTMLRenderer_Render_Debug(t *testing.T) {
	r := NewHTMLRenderer()
	ctx := &mockRenderContext{headers: make(map[string]string)}
	exCtx := NewExceptionContext().
		WithStackTrace(CaptureStackTrace(0)).
		WithRequestInfo("GET", "/test", "1.2.3.4", "TestAgent").
		WithIDs("req-123", "trace-456")

	prev := errors.New("previous")
	err := NewInternalServerErrorException("Server error").WithPrevious(prev).WithContext("debug", "info")

	renderErr := r.Render(ctx, err, exCtx, true)

	if renderErr != nil {
		t.Errorf("Render() error = %v", renderErr)
	}
	if ctx.statusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want %d", ctx.statusCode, http.StatusInternalServerError)
	}

	response := string(ctx.written)
	if !strings.Contains(response, "500") {
		t.Error("Response should contain status code")
	}
	if !strings.Contains(response, "Stack Trace") {
		t.Error("Debug response should contain stack trace section")
	}
}

func TestHTMLRenderer_Render_WithHeaders(t *testing.T) {
	r := NewHTMLRenderer()
	ctx := &mockRenderContext{headers: make(map[string]string)}

	err := NewHttpException(http.StatusTooManyRequests, "Rate limited").
		WithHeader("Retry-After", "60")

	renderErr := r.Render(ctx, err, nil, false)

	if renderErr != nil {
		t.Errorf("Render() error = %v", renderErr)
	}
	if ctx.headers["Retry-After"] != "60" {
		t.Error("Custom header not set")
	}
}

func TestHTMLRenderer_Render_NonHttpException(t *testing.T) {
	r := NewHTMLRenderer()
	ctx := &mockRenderContext{headers: make(map[string]string)}

	err := errors.New("simple error")

	renderErr := r.Render(ctx, err, nil, false)

	if renderErr != nil {
		t.Errorf("Render() error = %v", renderErr)
	}
	if ctx.statusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want %d", ctx.statusCode, http.StatusInternalServerError)
	}
}

func TestGetErrorMessage(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		debug bool
		want  string
	}{
		{
			name:  "debug mode shows full message",
			err:   errors.New("detailed error"),
			debug: true,
			want:  "detailed error",
		},
		{
			name:  "production hides server error details",
			err:   NewHttpException(500, "database connection failed"),
			debug: false,
			want:  "Internal Server Error",
		},
		{
			name:  "production shows client error message",
			err:   NewHttpException(400, "bad input"),
			debug: false,
			want:  "bad input",
		},
		{
			name:  "non-http error in production",
			err:   errors.New("internal details"),
			debug: false,
			want:  "An error occurred",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getErrorMessage(tt.err, tt.debug)
			if got != tt.want {
				t.Errorf("getErrorMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetErrorMessage_WithStatusCodeInterface(t *testing.T) {
	// Test with NotFoundHttpException which implements GetStatusCode
	err := NewNotFoundHttpException("Page not found")
	msg := getErrorMessage(err, false)
	if msg != "Page not found" {
		t.Errorf("getErrorMessage() = %q, want %q", msg, "Page not found")
	}

	// Test 5xx via interface
	err2 := NewInternalServerErrorException("details")
	msg2 := getErrorMessage(err2, false)
	if msg2 != "Internal Server Error" {
		t.Errorf("getErrorMessage() = %q, want %q", msg2, "Internal Server Error")
	}
}

func TestGetExceptionType(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"HttpException", NewHttpException(500, ""), "HttpException"},
		{"NotFoundHttpException", NewNotFoundHttpException(), "NotFoundHttpException"},
		{"ValidationException", NewValidationException(nil), "ValidationException"},
		{"BaseException", NewBaseException("", 0), "BaseException"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getExceptionType(tt.err)
			if got != tt.want {
				t.Errorf("getExceptionType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNegotiateRenderer(t *testing.T) {
	renderers := map[string]Renderer{
		"json": NewJSONRenderer(),
		"html": NewHTMLRenderer(),
	}

	tests := []struct {
		name         string
		accept       string
		contentType  string
		xRequestWith string
		requestPath  string
		wantType     string
	}{
		{"json accept", "application/json", "", "", "/", "application/json"},
		{"html accept", "text/html", "", "", "/", "text/html"},
		{"empty accept defaults to html", "", "", "", "/", "text/html"},
		{"wildcard defaults to html", "*/*", "", "", "/", "text/html"},
		{"ajax request", "", "", "XMLHttpRequest", "/", "application/json"},
		{"api path", "", "", "", "/api/users", "application/json"},
		{"json content type", "", "application/json", "", "/", "application/json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &mockRenderContext{
				accept:       tt.accept,
				contentType:  tt.contentType,
				xRequestWith: tt.xRequestWith,
				requestPath:  tt.requestPath,
				headers:      make(map[string]string),
			}

			renderer := NegotiateRenderer(ctx, renderers)
			if renderer.ContentType() != tt.wantType {
				t.Errorf("NegotiateRenderer() content type = %q, want %q", renderer.ContentType(), tt.wantType)
			}
		})
	}
}

func TestNegotiateRenderer_EmptyRenderers(t *testing.T) {
	ctx := &mockRenderContext{headers: make(map[string]string)}
	renderer := NegotiateRenderer(ctx, map[string]Renderer{})

	// Should return a default JSON renderer
	if renderer == nil {
		t.Fatal("Should return a renderer")
	}
	if renderer.ContentType() != "application/json" {
		t.Error("Should fall back to JSON renderer")
	}
}

func TestNegotiateRenderer_OnlyJSON(t *testing.T) {
	ctx := &mockRenderContext{
		accept:  "text/html",
		headers: make(map[string]string),
	}
	renderer := NegotiateRenderer(ctx, map[string]Renderer{
		"json": NewJSONRenderer(),
	})

	// Should fall back to JSON since HTML is not available
	if renderer.ContentType() != "application/json" {
		t.Error("Should fall back to JSON")
	}
}

func TestGetType(t *testing.T) {
	tests := []struct {
		name string
		val  any
		want string
	}{
		{"nil", nil, ""},
		{"HttpException", NewHttpException(500, ""), "HttpException"},
		{"unknown type", errors.New("test"), "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getType(tt.val)
			if got != tt.want {
				t.Errorf("getType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTemplateFuncs(t *testing.T) {
	addFunc := templateFuncs["add"].(func(int, int) int)
	subFunc := templateFuncs["sub"].(func(int, int) int)

	if addFunc(2, 3) != 5 {
		t.Error("add function incorrect")
	}
	if subFunc(5, 3) != 2 {
		t.Error("sub function incorrect")
	}
}

func TestResponseWriter_Write(t *testing.T) {
	ctx := &mockRenderContext{headers: make(map[string]string)}
	rw := &responseWriter{ctx: ctx}

	n, err := rw.Write([]byte("test"))
	if err != nil {
		t.Errorf("Write() error = %v", err)
	}
	if n != 4 {
		t.Errorf("Write() n = %d, want 4", n)
	}
	if string(ctx.written) != "test" {
		t.Error("Data not written correctly")
	}
}

func TestGetTypeName_Nil(t *testing.T) {
	result := getTypeName(nil)
	if result != "" {
		t.Errorf("getTypeName(nil) = %q, want empty", result)
	}
}

func TestGetFullTypeName_Nil(t *testing.T) {
	result := getFullTypeName(nil)
	if result != "" {
		t.Errorf("getFullTypeName(nil) = %q, want empty", result)
	}
}

func TestGetTypeString_Nil(t *testing.T) {
	result := getTypeString(nil)
	if result != "" {
		t.Errorf("getTypeString(nil) = %q, want empty", result)
	}
}

func TestFormatType(t *testing.T) {
	// Test with various types
	result := formatType(NewHttpException(500, ""))
	if result != "HttpException" {
		t.Errorf("formatType() = %q, want HttpException", result)
	}
}
