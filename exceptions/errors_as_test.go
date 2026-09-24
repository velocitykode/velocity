package exceptions

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// teapotError names a status but carries no headers or message.
type teapotError struct{}

func (teapotError) Error() string      { return "short and stout" }
func (teapotError) GetStatusCode() int { return http.StatusTeapot }

// nilEmbedError embeds a nil *BaseException: errors.As reaches the promoted
// Unwrap and must not panic.
type nilEmbedError struct {
	*BaseException
}

func (nilEmbedError) Error() string { return "nil embed" }

// statusResolutionCases covers direct, wrapped and embedding errors. The
// embedding subtypes (NotFoundHttpException, TooManyRequestsException,
// MethodNotAllowedHttpException, ...) embed *HttpException, which errors.As
// never reaches through BaseException.Unwrap.
var statusResolutionCases = []struct {
	name        string
	err         error
	wantStatus  int
	wantHeaders map[string]string
	absentHdrs  []string
	wantMessage string
}{
	{
		name:        "direct HttpException",
		err:         NewHttpException(http.StatusNotFound, "no such page"),
		wantStatus:  http.StatusNotFound,
		wantMessage: "no such page",
	},
	{
		name:        "direct HttpException with header",
		err:         NewHttpException(http.StatusBadRequest, "bad").WithHeader("X-Reason", "shape"),
		wantStatus:  http.StatusBadRequest,
		wantHeaders: map[string]string{"X-Reason": "shape"},
		wantMessage: "bad",
	},
	{
		name:        "embedding NotFoundHttpException",
		err:         NewNotFoundHttpException("no such user"),
		wantStatus:  http.StatusNotFound,
		wantMessage: "no such user",
	},
	{
		name:        "fmt wrapped NotFoundHttpException",
		err:         fmt.Errorf("load user 42: %w", NewNotFoundHttpException("no such user")),
		wantStatus:  http.StatusNotFound,
		wantMessage: "no such user",
	},
	{
		name:        "BaseException previous chain to NotFoundHttpException",
		err:         NewBaseException("repository failed", 0).WithPrevious(NewNotFoundHttpException()),
		wantStatus:  http.StatusNotFound,
		wantMessage: "Not Found",
	},
	{
		name:        "embedding TooManyRequestsException keeps Retry-After",
		err:         NewTooManyRequestsException(30),
		wantStatus:  http.StatusTooManyRequests,
		wantHeaders: map[string]string{"Retry-After": "30"},
		wantMessage: "Too Many Requests",
	},
	{
		name:        "fmt wrapped TooManyRequestsException keeps Retry-After",
		err:         fmt.Errorf("throttle: %w", NewTooManyRequestsException(30)),
		wantStatus:  http.StatusTooManyRequests,
		wantHeaders: map[string]string{"Retry-After": "30"},
		wantMessage: "Too Many Requests",
	},
	{
		name:        "embedding MethodNotAllowedHttpException keeps Allow",
		err:         NewMethodNotAllowedHttpException([]string{"GET", "POST"}),
		wantStatus:  http.StatusMethodNotAllowed,
		wantHeaders: map[string]string{"Allow": "GET, POST"},
		wantMessage: "Method Not Allowed",
	},
	{
		name:        "fmt wrapped MethodNotAllowedHttpException keeps Allow",
		err:         fmt.Errorf("dispatch: %w", NewMethodNotAllowedHttpException([]string{"GET"})),
		wantStatus:  http.StatusMethodNotAllowed,
		wantHeaders: map[string]string{"Allow": "GET"},
		wantMessage: "Method Not Allowed",
	},
	{
		name:        "embedding ServiceUnavailableException hides message, keeps Retry-After",
		err:         fmt.Errorf("maintenance: %w", NewServiceUnavailableException(120, "db migration running")),
		wantStatus:  http.StatusServiceUnavailable,
		wantHeaders: map[string]string{"Retry-After": "120"},
		wantMessage: http.StatusText(http.StatusServiceUnavailable),
	},
	{
		name:        "wrapped status-only error keeps status",
		err:         fmt.Errorf("brew: %w", teapotError{}),
		wantStatus:  http.StatusTeapot,
		wantMessage: "An error occurred",
	},
	{
		name:        "header with CR or LF is dropped",
		err:         NewHttpException(http.StatusBadRequest, "bad").WithHeader("X-Split", "a\r\nSet-Cookie: x=1").WithHeader("X-Ok", "fine"),
		wantStatus:  http.StatusBadRequest,
		wantHeaders: map[string]string{"X-Ok": "fine"},
		absentHdrs:  []string{"X-Split"},
		wantMessage: "bad",
	},
	{
		name:        "plain error is 500",
		err:         errors.New("db exploded"),
		wantStatus:  http.StatusInternalServerError,
		wantMessage: "An error occurred",
	},
	{
		name:        "type embedding nil BaseException is 500 without panic",
		err:         nilEmbedError{},
		wantStatus:  http.StatusInternalServerError,
		wantMessage: "An error occurred",
	},
}

func TestRenderers_ResolveStatusAndHeaders_ErrorsAs(t *testing.T) {
	renderers := []struct {
		name     string
		renderer Renderer
	}{
		{"json", NewJSONRenderer()},
		{"html", NewHTMLRenderer()},
	}
	for _, rr := range renderers {
		for _, tt := range statusResolutionCases {
			t.Run(rr.name+"/"+tt.name, func(t *testing.T) {
				ctx := &mockRenderContext{headers: make(map[string]string)}

				if err := rr.renderer.Render(ctx, tt.err, nil, false); err != nil {
					t.Fatalf("Render returned %v", err)
				}

				if ctx.statusCode != tt.wantStatus {
					t.Errorf("status = %d, want %d", ctx.statusCode, tt.wantStatus)
				}
				for k, v := range tt.wantHeaders {
					if got := ctx.headers[k]; got != v {
						t.Errorf("header %s = %q, want %q", k, got, v)
					}
				}
				for _, k := range tt.absentHdrs {
					if got, ok := ctx.headers[k]; ok {
						t.Errorf("header %s = %q, want absent", k, got)
					}
				}
			})
		}
	}
}

func TestGetErrorMessage_ErrorsAs(t *testing.T) {
	for _, tt := range statusResolutionCases {
		t.Run(tt.name, func(t *testing.T) {
			if got := getErrorMessage(tt.err, false); got != tt.wantMessage {
				t.Errorf("getErrorMessage = %q, want %q", got, tt.wantMessage)
			}
		})
	}
}

// The AC cases rendered end to end through the exceptions handler, onto a
// real http.ResponseWriter.
func TestErrorHandler_EmbeddingHeaders_ReachResponse(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		accept     string
		wantStatus int
		header     string
		wantValue  string
	}{
		{"TooManyRequests json", NewTooManyRequestsException(30), "application/json", http.StatusTooManyRequests, "Retry-After", "30"},
		{"TooManyRequests html", NewTooManyRequestsException(30), "text/html", http.StatusTooManyRequests, "Retry-After", "30"},
		{"wrapped TooManyRequests json", fmt.Errorf("limit: %w", NewTooManyRequestsException(30)), "application/json", http.StatusTooManyRequests, "Retry-After", "30"},
		{"MethodNotAllowed json", NewMethodNotAllowedHttpException([]string{"GET", "HEAD"}), "application/json", http.StatusMethodNotAllowed, "Allow", "GET, HEAD"},
		{"MethodNotAllowed html", NewMethodNotAllowedHttpException([]string{"GET", "HEAD"}), "text/html", http.StatusMethodNotAllowed, "Allow", "GET, HEAD"},
		{"wrapped MethodNotAllowed html", fmt.Errorf("route: %w", NewMethodNotAllowedHttpException([]string{"PUT"})), "text/html", http.StatusMethodNotAllowed, "Allow", "PUT"},
		{"wrapped NotFound json", fmt.Errorf("load: %w", NewNotFoundHttpException()), "application/json", http.StatusNotFound, "", ""},
		{"wrapped NotFound html", fmt.Errorf("load: %w", NewNotFoundHttpException()), "text/html", http.StatusNotFound, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(WithReporters())
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/thing", nil)
			r.Header.Set("Accept", tt.accept)

			ErrorHandler(h)(w, r, tt.err)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.header != "" {
				if got := w.Header().Get(tt.header); got != tt.wantValue {
					t.Errorf("header %s = %q, want %q", tt.header, got, tt.wantValue)
				}
			}
		})
	}
}

func TestHandler_ShouldReport_ErrorsAs(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"direct NotFound", NewNotFoundHttpException(), false},
		{"wrapped NotFound", fmt.Errorf("load: %w", NewNotFoundHttpException()), false},
		{"wrapped TooManyRequests", fmt.Errorf("limit: %w", NewTooManyRequestsException(5)), false},
		{"wrapped ValidationException", fmt.Errorf("form: %w", NewValidationException(map[string][]string{"name": {"required"}})), false},
		{"wrapped 4xx HttpException", fmt.Errorf("x: %w", NewHttpException(http.StatusBadRequest, "")), false},
		{"wrapped 5xx HttpException", fmt.Errorf("x: %w", NewHttpException(http.StatusBadGateway, "")), true},
		{"wrapped InternalServerError", fmt.Errorf("x: %w", NewInternalServerErrorException()), true},
		{"plain error", errors.New("boom"), true},
		{"wrapped plain error", fmt.Errorf("x: %w", errors.New("boom")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler()
			if got := h.ShouldReport(tt.err); got != tt.want {
				t.Errorf("ShouldReport = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHandler_Render_RenderableErrorsAs(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"direct ValidationException", NewValidationException(map[string][]string{"email": {"invalid"}})},
		{"wrapped ValidationException", fmt.Errorf("form: %w", NewValidationException(map[string][]string{"email": {"invalid"}}))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(WithReporters())
			ctx := &mockRenderContext{headers: make(map[string]string), accept: "text/html"}

			h.Render(ctx, tt.err, nil)

			if ctx.statusCode != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422", ctx.statusCode)
			}
			var body map[string]any
			if err := json.Unmarshal(ctx.written, &body); err != nil {
				t.Fatalf("Renderable body is not its own JSON: %v (%q)", err, ctx.written)
			}
			if _, ok := body["errors"]; !ok {
				t.Errorf("Renderable body missing errors: %v", body)
			}
		})
	}
}

// A panic is a bug: its value never picks the status, headers or renderer,
// even when errors.As could reach an HTTP exception or a Renderable through
// the recovered-panic wrapper.
func TestHandler_HandlePanic_HTTPShapedValueIs500(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{"NotFound value", NewNotFoundHttpException()},
		{"TooManyRequests value", NewTooManyRequestsException(30)},
		{"wrapped MethodNotAllowed value", fmt.Errorf("x: %w", NewMethodNotAllowedHttpException([]string{"GET"}))},
		{"ValidationException value", NewValidationException(map[string][]string{"name": {"required"}})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reported error
			h := NewHandler(WithReporters(NewCallbackReporter(func(err error, _ *ErrorContext) {
				reported = err
			})))
			ctx := &mockRenderContext{headers: make(map[string]string), accept: "application/json"}

			h.HandlePanic(ctx, tt.value)

			if reported == nil {
				t.Error("panic was not reported")
			}
			if ctx.statusCode != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", ctx.statusCode)
			}
			for _, k := range []string{"Retry-After", "Allow"} {
				if got, ok := ctx.headers[k]; ok {
					t.Errorf("header %s = %q leaked from panic value", k, got)
				}
			}
			if strings.Contains(string(ctx.written), `"errors"`) {
				t.Errorf("panic value's Render ran: %q", ctx.written)
			}
		})
	}
}

func TestLogReporter_buildFields_ErrorsAs(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus any
		wantCode   any
	}{
		{"direct HttpException", NewHttpException(http.StatusBadGateway, "x"), http.StatusBadGateway, http.StatusBadGateway},
		{"embedding NotFound", NewNotFoundHttpException(), http.StatusNotFound, http.StatusNotFound},
		{"wrapped TooManyRequests", fmt.Errorf("limit: %w", NewTooManyRequestsException(30)), http.StatusTooManyRequests, http.StatusTooManyRequests},
		{"wrapped BaseException", fmt.Errorf("x: %w", NewBaseException("base", 7)), nil, 7},
		{"plain error", errors.New("boom"), nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := NewLogReporter().buildFields(tt.err, nil)

			if got := fieldValue(fields, "status_code"); got != tt.wantStatus {
				t.Errorf("status_code = %v, want %v", got, tt.wantStatus)
			}
			if got := fieldValue(fields, "code"); got != tt.wantCode {
				t.Errorf("code = %v, want %v", got, tt.wantCode)
			}
		})
	}
}

// fieldValue returns the value logged under key, or nil when absent.
func fieldValue(fields []any, key string) any {
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] == key {
			return fields[i+1]
		}
	}
	return nil
}
