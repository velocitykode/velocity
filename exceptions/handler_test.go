package exceptions

import (
	"errors"
	"net/http"
	"sync"
	"testing"
)

func TestNewHandler(t *testing.T) {
	h := NewHandler()

	if h == nil {
		t.Fatal("NewHandler returned nil")
	}
	if h.debug {
		t.Error("debug should default to false")
	}
	if h.environment != "production" {
		t.Errorf("environment = %q, want production", h.environment)
	}
	if len(h.reporters) != 1 {
		t.Error("Should have default LogReporter")
	}
	if _, ok := h.renderers["json"]; !ok {
		t.Error("Should have JSON renderer")
	}
	if _, ok := h.renderers["html"]; !ok {
		t.Error("Should have HTML renderer")
	}
}

func TestNewHandler_WithOptions(t *testing.T) {
	mockReporter := NewCallbackReporter(func(err error, ctx *ExceptionContext) {})

	h := NewHandler(
		WithDebug(true),
		WithEnvironment("testing"),
		WithReporters(mockReporter),
		WithDontReport("TestException"),
	)

	if !h.debug {
		t.Error("debug should be true")
	}
	if h.environment != "testing" {
		t.Errorf("environment = %q, want testing", h.environment)
	}
	if len(h.reporters) != 1 {
		t.Error("Should have one reporter")
	}
	if !h.dontReport["TestException"] {
		t.Error("TestException should be in dontReport")
	}
}

func TestNewHandler_WithRenderers(t *testing.T) {
	customRenderer := NewJSONRenderer()

	h := NewHandler(WithRenderers(map[string]Renderer{
		"custom": customRenderer,
	}))

	if _, ok := h.renderers["custom"]; !ok {
		t.Error("Custom renderer not added")
	}
	// Should still have default renderers
	if _, ok := h.renderers["json"]; !ok {
		t.Error("Default JSON renderer should still exist")
	}
}

func TestHandler_SetDebug(t *testing.T) {
	// In non-production environment, SetDebug should work
	h := NewHandler(WithEnvironment("local"))

	h.SetDebug(true)
	if !h.IsDebug() {
		t.Error("SetDebug did not set debug to true in non-production environment")
	}

	h.SetDebug(false)
	if h.IsDebug() {
		t.Error("SetDebug did not set debug to false")
	}

	// In production environment, SetDebug(true) should be refused
	hp := NewHandler(WithEnvironment("production"))
	hp.SetDebug(true)
	if hp.IsDebug() {
		t.Error("SetDebug should refuse debug mode in production")
	}
}

func TestHandler_SetEnvironment(t *testing.T) {
	h := NewHandler()

	h.SetEnvironment("staging")
	if h.GetEnvironment() != "staging" {
		t.Errorf("GetEnvironment() = %q, want staging", h.GetEnvironment())
	}
}

func TestHandler_AddReporter(t *testing.T) {
	h := NewHandler()
	initialCount := len(h.reporters)

	mockReporter := NewCallbackReporter(func(err error, ctx *ExceptionContext) {})
	h.AddReporter(mockReporter)

	if len(h.reporters) != initialCount+1 {
		t.Error("AddReporter did not add reporter")
	}
}

func TestHandler_SetReporters(t *testing.T) {
	h := NewHandler()

	mockReporter := NewCallbackReporter(func(err error, ctx *ExceptionContext) {})
	h.SetReporters(mockReporter)

	if len(h.reporters) != 1 {
		t.Errorf("SetReporters: got %d reporters, want 1", len(h.reporters))
	}
}

func TestHandler_AddRenderer(t *testing.T) {
	h := NewHandler()

	customRenderer := NewJSONRenderer()
	h.AddRenderer("custom", customRenderer)

	if _, ok := h.renderers["custom"]; !ok {
		t.Error("AddRenderer did not add renderer")
	}
}

func TestHandler_DontReport(t *testing.T) {
	h := NewHandler()

	h.DontReport("TestException")
	if !h.dontReport["TestException"] {
		t.Error("DontReport did not add type")
	}
}

func TestHandler_ShouldReport(t *testing.T) {
	h := NewHandler()
	h.DontReport("NotFoundHttpException")

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"reportable exception", NewHttpException(500, ""), true},
		{"in dontReport list", NewNotFoundHttpException(), false},
		{"implements Reportable false", NewValidationException(nil), false},
		{"simple error", errors.New("test"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := h.ShouldReport(tt.err); got != tt.want {
				t.Errorf("ShouldReport() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHandler_Report(t *testing.T) {
	var reported bool
	mockReporter := NewCallbackReporter(func(err error, ctx *ExceptionContext) {
		reported = true
	})

	h := NewHandler(WithReporters(mockReporter))

	h.Report(errors.New("test"), nil)

	if !reported {
		t.Error("Report did not call reporter")
	}
}

func TestHandler_Report_ShouldNotReport(t *testing.T) {
	var reported bool
	mockReporter := NewCallbackReporter(func(err error, ctx *ExceptionContext) {
		reported = true
	})

	h := NewHandler(WithReporters(mockReporter))
	h.DontReport("NotFoundHttpException")

	h.Report(NewNotFoundHttpException(), nil)

	if reported {
		t.Error("Should not have reported")
	}
}

func TestHandler_Render_Renderable(t *testing.T) {
	h := NewHandler()
	ctx := &mockRenderContext{headers: make(map[string]string)}

	err := NewValidationException(map[string][]string{"field": {"error"}})

	h.Render(ctx, err, nil)

	if ctx.statusCode != http.StatusUnprocessableEntity {
		t.Errorf("StatusCode = %d, want %d", ctx.statusCode, http.StatusUnprocessableEntity)
	}
}

func TestHandler_Render_ContentNegotiation(t *testing.T) {
	h := NewHandler()

	tests := []struct {
		name   string
		accept string
		wantCT string
	}{
		{"json request", "application/json", "application/json"},
		{"html request", "text/html", "text/html; charset=utf-8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &mockRenderContext{
				headers: make(map[string]string),
				accept:  tt.accept,
			}

			h.Render(ctx, errors.New("test"), nil)

			if ctx.headers["Content-Type"] != tt.wantCT {
				t.Errorf("Content-Type = %q, want %q", ctx.headers["Content-Type"], tt.wantCT)
			}
		})
	}
}

func TestHandler_Handle(t *testing.T) {
	var reportedErr error
	mockReporter := NewCallbackReporter(func(err error, ctx *ExceptionContext) {
		reportedErr = err
	})

	h := NewHandler(WithReporters(mockReporter))
	ctx := &mockRenderContext{
		headers:     make(map[string]string),
		accept:      "application/json",
		requestPath: "/test",
		method:      "GET",
	}

	testErr := errors.New("test error")
	h.Handle(ctx, testErr)

	if reportedErr == nil {
		t.Error("Error was not reported")
	}
	if ctx.statusCode == 0 {
		t.Error("Response was not rendered")
	}
}

func TestHandler_HandleWithContext(t *testing.T) {
	var reportedCtx *ExceptionContext
	mockReporter := NewCallbackReporter(func(err error, ctx *ExceptionContext) {
		reportedCtx = ctx
	})

	h := NewHandler(WithReporters(mockReporter))
	ctx := &mockRenderContext{
		headers: make(map[string]string),
		accept:  "application/json",
	}

	exCtx := NewExceptionContext().WithIDs("req-123", "trace-456")
	h.HandleWithContext(ctx, errors.New("test"), exCtx)

	if reportedCtx.RequestID != "req-123" {
		t.Error("Context not passed to reporter")
	}
}

func TestHandler_HandleWithContext_NilContext(t *testing.T) {
	h := NewHandler()
	ctx := &mockRenderContext{headers: make(map[string]string), accept: "application/json"}

	// Should not panic with nil context
	h.HandleWithContext(ctx, errors.New("test"), nil)

	if ctx.statusCode == 0 {
		t.Error("Response was not rendered")
	}
}

func TestHandler_HandleWithContext_NilStackTrace(t *testing.T) {
	var reportedCtx *ExceptionContext
	mockReporter := NewCallbackReporter(func(err error, ctx *ExceptionContext) {
		reportedCtx = ctx
	})

	h := NewHandler(WithReporters(mockReporter))
	ctx := &mockRenderContext{headers: make(map[string]string), accept: "application/json"}

	exCtx := NewExceptionContext() // No stack trace set
	h.HandleWithContext(ctx, errors.New("test"), exCtx)

	if reportedCtx.StackTrace == nil {
		t.Error("Stack trace should be captured")
	}
}

func TestHandler_RegisterCustomHandler(t *testing.T) {
	h := NewHandler()

	var customHandled bool
	h.RegisterCustomHandler((*NotFoundHttpException)(nil), func(ctx RenderContext, err error, exCtx *ExceptionContext) {
		customHandled = true
		ctx.WriteHeader(http.StatusNotFound)
		ctx.Write([]byte("custom not found"))
	})

	ctx := &mockRenderContext{headers: make(map[string]string)}
	h.Render(ctx, NewNotFoundHttpException(), nil)

	if !customHandled {
		t.Error("Custom handler was not called")
	}
	if ctx.statusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want %d", ctx.statusCode, http.StatusNotFound)
	}
}

func TestHandler_HandlePanic_Error(t *testing.T) {
	var reportedErr error
	mockReporter := NewCallbackReporter(func(err error, ctx *ExceptionContext) {
		reportedErr = err
	})

	h := NewHandler(WithReporters(mockReporter))
	ctx := &mockRenderContext{
		headers:     make(map[string]string),
		accept:      "application/json",
		requestPath: "/test",
		method:      "POST",
	}

	panicErr := errors.New("panic error")
	h.HandlePanic(ctx, panicErr)

	if reportedErr == nil {
		t.Error("Panic was not reported")
	}
	if ctx.statusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want %d", ctx.statusCode, http.StatusInternalServerError)
	}
}

func TestHandler_HandlePanic_String(t *testing.T) {
	var reportedErr error
	mockReporter := NewCallbackReporter(func(err error, ctx *ExceptionContext) {
		reportedErr = err
	})

	h := NewHandler(WithReporters(mockReporter))
	ctx := &mockRenderContext{headers: make(map[string]string), accept: "application/json"}

	h.HandlePanic(ctx, "string panic")

	if reportedErr == nil || reportedErr.Error() != "panic: string panic" {
		t.Errorf("reportedErr = %v, want panic: string panic", reportedErr)
	}
}

func TestHandler_HandlePanic_Other(t *testing.T) {
	var reportedErr error
	mockReporter := NewCallbackReporter(func(err error, ctx *ExceptionContext) {
		reportedErr = err
	})

	h := NewHandler(WithReporters(mockReporter))
	ctx := &mockRenderContext{headers: make(map[string]string), accept: "application/json"}

	h.HandlePanic(ctx, 42)

	if reportedErr == nil || reportedErr.Error() != "panic: 42" {
		t.Errorf("reportedErr = %v, want panic: 42", reportedErr)
	}
}

func TestHandler_Concurrent(t *testing.T) {
	h := NewHandler()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Concurrent reads
			h.IsDebug()
			h.GetEnvironment()
			h.ShouldReport(errors.New("test"))

			// Concurrent render
			ctx := &mockRenderContext{headers: make(map[string]string), accept: "application/json"}
			h.Render(ctx, errors.New("test"), nil)
		}()
	}

	wg.Wait()
}

func TestHandler_Render_RenderableFailure(t *testing.T) {
	h := NewHandler()
	ctx := &mockRenderContext{headers: make(map[string]string), accept: "application/json"}

	// Create a mock renderable that fails
	err := &failingRenderable{}

	h.Render(ctx, err, nil)

	// Should fall back to default rendering
	if ctx.statusCode == 0 {
		t.Error("Should have rendered with fallback")
	}
}

type failingRenderable struct {
	*BaseException
}

func (f *failingRenderable) Render(ctx RenderContext) error {
	return errors.New("render failed")
}

func (f *failingRenderable) Error() string {
	return "failing renderable"
}

func TestHandler_APIMode(t *testing.T) {
	h := NewHandler(WithAPIMode(true))

	if !h.IsAPIMode() {
		t.Error("API mode should be enabled")
	}

	h.SetAPIMode(false)
	if h.IsAPIMode() {
		t.Error("API mode should be disabled")
	}
}

func TestHandler_APIPrefixes(t *testing.T) {
	h := NewHandler(WithAPIPrefixes("/api", "/v1"))

	prefixes := h.GetAPIPrefixes()
	if len(prefixes) != 2 {
		t.Errorf("Expected 2 prefixes, got %d", len(prefixes))
	}

	h.SetAPIPrefixes("/api/v2")
	prefixes = h.GetAPIPrefixes()
	if len(prefixes) != 1 || prefixes[0] != "/api/v2" {
		t.Error("SetAPIPrefixes did not update correctly")
	}
}

func TestHandler_isAPIRequest(t *testing.T) {
	tests := []struct {
		name        string
		apiMode     bool
		apiPrefixes []string
		path        string
		wantsJSON   bool
		want        bool
	}{
		{"api mode enabled", true, nil, "/users", false, true},
		{"api prefix match", false, []string{"/api"}, "/api/users", false, true},
		{"api prefix no match", false, []string{"/api"}, "/users", false, false},
		{"wants json", false, nil, "/users", true, true},
		{"no indicators", false, nil, "/users", false, false},
		{"multiple prefixes", false, []string{"/api", "/v1"}, "/v1/users", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(
				WithAPIMode(tt.apiMode),
				WithAPIPrefixes(tt.apiPrefixes...),
			)

			ctx := &mockRenderContext{
				requestPath: tt.path,
				headers:     make(map[string]string),
			}
			if tt.wantsJSON {
				ctx.accept = "application/json"
			}

			got := h.isAPIRequest(ctx)
			if got != tt.want {
				t.Errorf("isAPIRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHandler_Render_APIMode(t *testing.T) {
	h := NewHandler(WithAPIMode(true))

	// Even for HTML accept header, should return JSON in API mode
	ctx := &mockRenderContext{
		headers:     make(map[string]string),
		accept:      "text/html",
		requestPath: "/users",
	}

	h.Render(ctx, errors.New("test"), nil)

	if ctx.headers["Content-Type"] != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ctx.headers["Content-Type"])
	}
}

func TestHandler_Render_APIPrefixes(t *testing.T) {
	h := NewHandler(WithAPIPrefixes("/api"))

	// Request to /api path should get JSON even without JSON accept header
	ctx := &mockRenderContext{
		headers:     make(map[string]string),
		accept:      "text/html",
		requestPath: "/api/users",
	}

	h.Render(ctx, errors.New("test"), nil)

	if ctx.headers["Content-Type"] != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ctx.headers["Content-Type"])
	}
}
