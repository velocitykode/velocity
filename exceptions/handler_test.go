package exceptions

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
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
	mockReporter := NewCallbackReporter(func(err error, ctx *ErrorContext) {})

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

	mockReporter := NewCallbackReporter(func(err error, ctx *ErrorContext) {})
	h.AddReporter(mockReporter)

	if len(h.reporters) != initialCount+1 {
		t.Error("AddReporter did not add reporter")
	}
}

func TestHandler_SetReporters(t *testing.T) {
	h := NewHandler()

	mockReporter := NewCallbackReporter(func(err error, ctx *ErrorContext) {})
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
	mockReporter := NewCallbackReporter(func(err error, ctx *ErrorContext) {
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
	mockReporter := NewCallbackReporter(func(err error, ctx *ErrorContext) {
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

func TestHandler_HandleRequest_NilContextFromRequest(t *testing.T) {
	var reportedErr error
	var reportedCtx *ErrorContext
	mockReporter := NewCallbackReporter(func(err error, ctx *ErrorContext) {
		reportedErr = err
		reportedCtx = ctx
	})

	h := NewHandler(WithReporters(mockReporter))
	ctx := &mockRenderContext{
		headers:     make(map[string]string),
		accept:      "application/json",
		requestPath: "/test",
		method:      "GET",
	}

	testErr := errors.New("test error")
	h.HandleRequest(ctx, testErr, nil)

	if reportedErr == nil {
		t.Fatal("Error was not reported")
	}
	if reportedCtx.URL != "/test" || reportedCtx.Method != "GET" {
		t.Errorf("context URL/Method = %q/%q, want /test/GET", reportedCtx.URL, reportedCtx.Method)
	}
	if ctx.statusCode == 0 {
		t.Error("Response was not rendered")
	}
}

func TestHandler_HandleRequest(t *testing.T) {
	var reportedCtx *ErrorContext
	mockReporter := NewCallbackReporter(func(err error, ctx *ErrorContext) {
		reportedCtx = ctx
	})

	h := NewHandler(WithReporters(mockReporter))
	ctx := &mockRenderContext{
		headers: make(map[string]string),
		accept:  "application/json",
	}

	exCtx := NewErrorContext().WithIDs("req-123", "trace-456")
	h.HandleRequest(ctx, errors.New("test"), exCtx)

	if reportedCtx.RequestID != "req-123" {
		t.Error("Context not passed to reporter")
	}
}

func TestHandler_HandleRequest_NilContext(t *testing.T) {
	h := NewHandler()
	ctx := &mockRenderContext{headers: make(map[string]string), accept: "application/json"}

	// Should not panic with nil context
	h.HandleRequest(ctx, errors.New("test"), nil)

	if ctx.statusCode == 0 {
		t.Error("Response was not rendered")
	}
}

func TestHandler_HandleRequest_NilStackTrace(t *testing.T) {
	var reportedCtx *ErrorContext
	mockReporter := NewCallbackReporter(func(err error, ctx *ErrorContext) {
		reportedCtx = ctx
	})

	h := NewHandler(WithReporters(mockReporter))
	ctx := &mockRenderContext{headers: make(map[string]string), accept: "application/json"}

	exCtx := NewErrorContext() // No stack trace set
	h.HandleRequest(ctx, errors.New("test"), exCtx)

	if reportedCtx.StackTrace == nil {
		t.Error("Stack trace should be captured")
	}
}

func TestHandler_RegisterCustomHandler(t *testing.T) {
	h := NewHandler()

	var customHandled bool
	h.RegisterCustomHandler((*NotFoundHttpException)(nil), func(ctx RenderContext, err error, exCtx *ErrorContext) {
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
	mockReporter := NewCallbackReporter(func(err error, ctx *ErrorContext) {
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
	mockReporter := NewCallbackReporter(func(err error, ctx *ErrorContext) {
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
	mockReporter := NewCallbackReporter(func(err error, ctx *ErrorContext) {
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

// dontReportCustomError is a custom error type used to pin that WithDontReport
// matches on the %T-derived type name rather than the old hardcoded "error".
type dontReportCustomError struct{}

func (dontReportCustomError) Error() string { return "custom boom" }

func TestWithDontReport_CustomTypeSuppressed(t *testing.T) {
	var reported bool
	rec := NewCallbackReporter(func(error, *ErrorContext) { reported = true })

	// A pointer error from a non-framework package: its real %T name is
	// "*url.Error", and WithDontReport must suppress it using exactly that
	// name (no stripping of the pointer marker or package qualifier).
	err := &url.Error{Op: "Get", URL: "x", Err: errors.New("boom")}
	h := NewHandler(WithReporters(rec), WithDontReport(fmt.Sprintf("%T", err)))

	if h.ShouldReport(err) {
		t.Error("ShouldReport should be false for a dont-report custom type")
	}
	h.Report(err, NewErrorContext())
	if reported {
		t.Error("custom error in dontReport list must not be reported")
	}
}

func TestWithDontReport_LiteralErrorNoLongerSuppresses(t *testing.T) {
	var reported bool
	rec := NewCallbackReporter(func(error, *ErrorContext) { reported = true })

	// Pre-fix every non-builtin error collapsed to "error", so this would have
	// suppressed the custom type. It must no longer match.
	h := NewHandler(WithReporters(rec), WithDontReport("error"))

	err := &dontReportCustomError{}
	if !h.ShouldReport(err) {
		t.Error("ShouldReport should be true: \"error\" must not match a real type name")
	}
	h.Report(err, NewErrorContext())
	if !reported {
		t.Error("custom error must be reported when only \"error\" is in dontReport")
	}
}

func TestHandler_RuleSetters_Store(t *testing.T) {
	h := NewHandler()
	match := func(error) bool { return true }
	h.AddMapRule(contract.MapRule{Match: match})
	h.AddRenderRule(contract.RenderRule{Match: match, Status: 418})
	h.AddReportRule(contract.ReportRule{Match: match})
	h.AddIgnoreRule(contract.IgnoreRule{Match: match})
	h.AddLevelRule(contract.LevelRule{Match: match, Level: contract.LogLevelWarn})
	h.AddThrottleRule(contract.ThrottleRule{Match: match})
	h.IgnoreIf(func(error, *ErrorContext) bool { return false })
	h.ContextUsing(func(error, *ErrorContext) map[string]any { return nil })
	h.JSONWhen(func(*http.Request, error) bool { return true })
	h.BeforeRender(func(_ RenderContext, _ error, status int) int { return status })
	h.SetErrorPageRenderer(nil)

	tests := []struct {
		name string
		got  int
	}{
		{"map", len(h.mapRules)},
		{"render", len(h.renderRules)},
		{"report", len(h.reportRules)},
		{"ignore", len(h.ignoreRules)},
		{"level", len(h.levelRules)},
		{"throttle", len(h.throttleRules)},
		{"ignore predicates", len(h.ignorePredicates)},
		{"context providers", len(h.contextProviders)},
		{"before render", len(h.beforeRender)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != 1 {
				t.Errorf("stored %d, want 1", tt.got)
			}
		})
	}
	if h.jsonWhen == nil {
		t.Error("JSONWhen predicate not stored")
	}
}

func TestHandler_HandleConsole(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantCode     int
		wantReported bool
	}{
		{"nil error", nil, 0, false},
		{"error reports", errors.New("command failed"), 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reported := false
			h := NewHandler(WithReporters(NewCallbackReporter(func(error, *ErrorContext) { reported = true })))
			if got := h.HandleConsole(io.Discard, tt.err); got != tt.wantCode {
				t.Errorf("HandleConsole() = %d, want %d", got, tt.wantCode)
			}
			if reported != tt.wantReported {
				t.Errorf("reported = %v, want %v", reported, tt.wantReported)
			}
		})
	}
}
