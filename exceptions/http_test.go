package exceptions

import (
	"net/http"
	"strings"
	"testing"
)

func TestNewHttpException(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		message    string
		wantMsg    string
	}{
		{
			name:       "with message",
			statusCode: http.StatusBadRequest,
			message:    "custom message",
			wantMsg:    "custom message",
		},
		{
			name:       "empty message uses status text",
			statusCode: http.StatusNotFound,
			message:    "",
			wantMsg:    "Not Found",
		},
		{
			name:       "internal server error",
			statusCode: http.StatusInternalServerError,
			message:    "",
			wantMsg:    "Internal Server Error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exc := NewHttpException(tt.statusCode, tt.message)

			if exc.GetStatusCode() != tt.statusCode {
				t.Errorf("GetStatusCode() = %d, want %d", exc.GetStatusCode(), tt.statusCode)
			}
			if exc.GetMessage() != tt.wantMsg {
				t.Errorf("GetMessage() = %q, want %q", exc.GetMessage(), tt.wantMsg)
			}
			if exc.Headers == nil {
				t.Error("Headers should be initialized")
			}
		})
	}
}

func TestHttpException_WithHeader(t *testing.T) {
	exc := NewHttpException(http.StatusOK, "").
		WithHeader("X-Custom", "value")

	headers := exc.GetHeaders()
	if headers["X-Custom"] != "value" {
		t.Error("WithHeader did not set header")
	}
}

func TestHttpException_WithHeader_NilHeaders(t *testing.T) {
	exc := NewHttpException(http.StatusOK, "")
	exc.Headers = nil

	exc.WithHeader("Key", "Value")
	if exc.Headers == nil {
		t.Error("WithHeader should initialize nil headers")
	}
}

func TestHttpException_WithHeaders(t *testing.T) {
	exc := NewHttpException(http.StatusOK, "").
		WithHeaders(map[string]string{
			"X-One": "1",
			"X-Two": "2",
		})

	headers := exc.GetHeaders()
	if headers["X-One"] != "1" || headers["X-Two"] != "2" {
		t.Error("WithHeaders did not set headers")
	}
}

func TestHttpException_WithHeaders_NilHeaders(t *testing.T) {
	exc := NewHttpException(http.StatusOK, "")
	exc.Headers = nil

	exc.WithHeaders(map[string]string{"Key": "Value"})
	if exc.Headers == nil {
		t.Error("WithHeaders should initialize nil headers")
	}
}

func TestHttpException_GetHeaders_Nil(t *testing.T) {
	exc := NewHttpException(http.StatusOK, "")
	exc.Headers = nil

	headers := exc.GetHeaders()
	if headers == nil {
		t.Error("GetHeaders should return empty map, not nil")
	}
}

func TestHttpException_ShouldReport(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		want       bool
	}{
		{"4xx error", http.StatusBadRequest, false},
		{"404 not found", http.StatusNotFound, false},
		{"5xx error", http.StatusInternalServerError, true},
		{"502 bad gateway", http.StatusBadGateway, true},
		{"499 client closed", 499, false},
		{"500 exactly", 500, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exc := NewHttpException(tt.statusCode, "")
			if got := exc.ShouldReport(); got != tt.want {
				t.Errorf("ShouldReport() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNotFoundHttpException(t *testing.T) {
	tests := []struct {
		name    string
		message []string
		wantMsg string
	}{
		{"default message", nil, "Not Found"},
		{"custom message", []string{"Page not found"}, "Page not found"},
		{"empty string uses default", []string{""}, "Not Found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exc := NewNotFoundHttpException(tt.message...)

			if exc.StatusCode != http.StatusNotFound {
				t.Errorf("StatusCode = %d, want %d", exc.StatusCode, http.StatusNotFound)
			}
			if exc.GetMessage() != tt.wantMsg {
				t.Errorf("GetMessage() = %q, want %q", exc.GetMessage(), tt.wantMsg)
			}
			if exc.ShouldReport() {
				t.Error("NotFoundHttpException should not report")
			}
		})
	}
}

func TestUnauthorizedHttpException(t *testing.T) {
	exc := NewUnauthorizedHttpException()
	if exc.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want %d", exc.StatusCode, http.StatusUnauthorized)
	}
	if exc.ShouldReport() {
		t.Error("UnauthorizedHttpException should not report")
	}

	excCustom := NewUnauthorizedHttpException("Please log in")
	if excCustom.GetMessage() != "Please log in" {
		t.Error("Custom message not set")
	}
}

func TestForbiddenHttpException(t *testing.T) {
	exc := NewForbiddenHttpException()
	if exc.StatusCode != http.StatusForbidden {
		t.Errorf("StatusCode = %d, want %d", exc.StatusCode, http.StatusForbidden)
	}
	if exc.ShouldReport() {
		t.Error("ForbiddenHttpException should not report")
	}

	excCustom := NewForbiddenHttpException("Access denied")
	if excCustom.GetMessage() != "Access denied" {
		t.Error("Custom message not set")
	}
}

func TestValidationException(t *testing.T) {
	errors := map[string][]string{
		"email": {"Invalid email format"},
		"name":  {"Name is required", "Name too short"},
	}

	exc := NewValidationException(errors)

	if exc.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("StatusCode = %d, want %d", exc.StatusCode, http.StatusUnprocessableEntity)
	}
	if exc.GetMessage() != "The given data was invalid" {
		t.Error("Default message not set")
	}
	if exc.ShouldReport() {
		t.Error("ValidationException should not report")
	}

	validationErrors := exc.GetValidationErrors()
	if len(validationErrors["email"]) != 1 {
		t.Error("Validation errors not stored correctly")
	}
	if len(validationErrors["name"]) != 2 {
		t.Error("Validation errors not stored correctly")
	}

	// Test with custom message
	excCustom := NewValidationException(errors, "Custom validation error")
	if excCustom.GetMessage() != "Custom validation error" {
		t.Error("Custom message not set")
	}
}

func TestValidationException_Render(t *testing.T) {
	errors := map[string][]string{
		"email": {"Invalid email"},
	}
	exc := NewValidationException(errors)

	ctx := &mockRenderContext{headers: make(map[string]string)}
	err := exc.Render(ctx)

	if err != nil {
		t.Errorf("Render() error = %v", err)
	}
	if ctx.statusCode != http.StatusUnprocessableEntity {
		t.Errorf("StatusCode = %d, want %d", ctx.statusCode, http.StatusUnprocessableEntity)
	}
	if ctx.headers["Content-Type"] != "application/json" {
		t.Error("Content-Type not set")
	}
	if ctx.headers["X-Content-Type-Options"] != "nosniff" {
		t.Error("X-Content-Type-Options not set")
	}
	if len(ctx.written) == 0 {
		t.Error("No data written")
	}
}

func TestTooManyRequestsException(t *testing.T) {
	exc := NewTooManyRequestsException(60)

	if exc.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want %d", exc.StatusCode, http.StatusTooManyRequests)
	}
	if exc.RetryAfter != 60 {
		t.Errorf("RetryAfter = %d, want %d", exc.RetryAfter, 60)
	}
	if exc.Headers["Retry-After"] != "60" {
		t.Error("Retry-After header not set")
	}
	if exc.ShouldReport() {
		t.Error("TooManyRequestsException should not report")
	}

	// Test without retry after
	excNoRetry := NewTooManyRequestsException(0)
	if _, ok := excNoRetry.Headers["Retry-After"]; ok {
		t.Error("Retry-After should not be set when 0")
	}

	// Test with custom message
	excCustom := NewTooManyRequestsException(30, "Slow down")
	if excCustom.GetMessage() != "Slow down" {
		t.Error("Custom message not set")
	}
}

func TestServiceUnavailableException(t *testing.T) {
	exc := NewServiceUnavailableException(120)

	if exc.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode = %d, want %d", exc.StatusCode, http.StatusServiceUnavailable)
	}
	if exc.RetryAfter != 120 {
		t.Errorf("RetryAfter = %d, want %d", exc.RetryAfter, 120)
	}
	if exc.Headers["Retry-After"] != "120" {
		t.Error("Retry-After header not set")
	}

	// Test without retry after
	excNoRetry := NewServiceUnavailableException(0)
	if _, ok := excNoRetry.Headers["Retry-After"]; ok {
		t.Error("Retry-After should not be set when 0")
	}

	// Test with custom message
	excCustom := NewServiceUnavailableException(60, "Maintenance")
	if excCustom.GetMessage() != "Maintenance" {
		t.Error("Custom message not set")
	}
}

func TestMethodNotAllowedHttpException(t *testing.T) {
	exc := NewMethodNotAllowedHttpException([]string{"GET", "POST"})

	if exc.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("StatusCode = %d, want %d", exc.StatusCode, http.StatusMethodNotAllowed)
	}
	if exc.Headers["Allow"] != "GET, POST" {
		t.Errorf("Allow header = %q, want %q", exc.Headers["Allow"], "GET, POST")
	}
	if exc.ShouldReport() {
		t.Error("MethodNotAllowedHttpException should not report")
	}

	// Test without allowed methods
	excNoMethods := NewMethodNotAllowedHttpException(nil)
	if _, ok := excNoMethods.Headers["Allow"]; ok {
		t.Error("Allow header should not be set when no methods")
	}

	// Test with custom message
	excCustom := NewMethodNotAllowedHttpException([]string{"GET"}, "Use GET")
	if excCustom.GetMessage() != "Use GET" {
		t.Error("Custom message not set")
	}
}

func TestConflictHttpException(t *testing.T) {
	exc := NewConflictHttpException()
	if exc.StatusCode != http.StatusConflict {
		t.Errorf("StatusCode = %d, want %d", exc.StatusCode, http.StatusConflict)
	}
	if exc.ShouldReport() {
		t.Error("ConflictHttpException should not report")
	}

	excCustom := NewConflictHttpException("Resource conflict")
	if excCustom.GetMessage() != "Resource conflict" {
		t.Error("Custom message not set")
	}
}

func TestGoneHttpException(t *testing.T) {
	exc := NewGoneHttpException()
	if exc.StatusCode != http.StatusGone {
		t.Errorf("StatusCode = %d, want %d", exc.StatusCode, http.StatusGone)
	}
	if exc.ShouldReport() {
		t.Error("GoneHttpException should not report")
	}

	excCustom := NewGoneHttpException("Resource removed")
	if excCustom.GetMessage() != "Resource removed" {
		t.Error("Custom message not set")
	}
}

func TestBadRequestHttpException(t *testing.T) {
	exc := NewBadRequestHttpException()
	if exc.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want %d", exc.StatusCode, http.StatusBadRequest)
	}
	if exc.ShouldReport() {
		t.Error("BadRequestHttpException should not report")
	}

	excCustom := NewBadRequestHttpException("Invalid input")
	if excCustom.GetMessage() != "Invalid input" {
		t.Error("Custom message not set")
	}
}

func TestInternalServerErrorException(t *testing.T) {
	exc := NewInternalServerErrorException()
	if exc.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want %d", exc.StatusCode, http.StatusInternalServerError)
	}

	excCustom := NewInternalServerErrorException("Something went wrong")
	if excCustom.GetMessage() != "Something went wrong" {
		t.Error("Custom message not set")
	}
}

func TestAbort(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		message    []string
		wantMsg    string
	}{
		{"without message", http.StatusNotFound, nil, "Not Found"},
		{"with message", http.StatusBadRequest, []string{"Bad input"}, "Bad input"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exc := Abort(tt.statusCode, tt.message...)
			if exc.StatusCode != tt.statusCode {
				t.Errorf("StatusCode = %d, want %d", exc.StatusCode, tt.statusCode)
			}
			if exc.GetMessage() != tt.wantMsg {
				t.Errorf("GetMessage() = %q, want %q", exc.GetMessage(), tt.wantMsg)
			}
		})
	}
}

func TestAbortIf(t *testing.T) {
	tests := []struct {
		name      string
		condition bool
		wantNil   bool
	}{
		{"condition true", true, false},
		{"condition false", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exc := AbortIf(tt.condition, http.StatusBadRequest)
			if tt.wantNil && exc != nil {
				t.Error("Expected nil exception")
			}
			if !tt.wantNil && exc == nil {
				t.Error("Expected non-nil exception")
			}
		})
	}
}

func TestAbortUnless(t *testing.T) {
	tests := []struct {
		name      string
		condition bool
		wantNil   bool
	}{
		{"condition true", true, true},
		{"condition false", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exc := AbortUnless(tt.condition, http.StatusBadRequest)
			if tt.wantNil && exc != nil {
				t.Error("Expected nil exception")
			}
			if !tt.wantNil && exc == nil {
				t.Error("Expected non-nil exception")
			}
		})
	}
}

// mockRenderContext for testing
type mockRenderContext struct {
	headers      map[string]string
	requestPath  string
	method       string
	accept       string
	contentType  string
	xRequestWith string
	statusCode   int
	written      []byte
}

func (m *mockRenderContext) WriteHeader(statusCode int) {
	m.statusCode = statusCode
}

func (m *mockRenderContext) Write(data []byte) (int, error) {
	m.written = append(m.written, data...)
	return len(data), nil
}

func (m *mockRenderContext) SetHeader(key, value string) {
	if m.headers == nil {
		m.headers = make(map[string]string)
	}
	m.headers[key] = value
}

func (m *mockRenderContext) GetHeader(key string) string {
	switch key {
	case "Accept":
		return m.accept
	case "Content-Type":
		return m.contentType
	case "X-Requested-With":
		return m.xRequestWith
	default:
		return m.headers[key]
	}
}

func (m *mockRenderContext) RequestPath() string {
	return m.requestPath
}

func (m *mockRenderContext) RequestMethod() string {
	return m.method
}

func (m *mockRenderContext) WantsJSON() bool {
	return strings.Contains(m.accept, "application/json") ||
		strings.Contains(m.contentType, "application/json") ||
		m.xRequestWith == "XMLHttpRequest" ||
		strings.HasPrefix(m.requestPath, "/api")
}
