package exceptions

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestNewExceptionContext(t *testing.T) {
	ctx := NewExceptionContext()

	if ctx == nil {
		t.Fatal("NewExceptionContext returned nil")
	}
	if ctx.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}
	if ctx.Extra == nil {
		t.Error("Extra should be initialized")
	}
}

func TestExceptionContext_WithRequestInfo(t *testing.T) {
	ctx := NewExceptionContext().WithRequestInfo("POST", "/api/users", "192.168.1.1", "Mozilla/5.0")

	if ctx.Method != "POST" {
		t.Errorf("Method = %q, want %q", ctx.Method, "POST")
	}
	if ctx.URL != "/api/users" {
		t.Errorf("URL = %q, want %q", ctx.URL, "/api/users")
	}
	if ctx.IP != "192.168.1.1" {
		t.Errorf("IP = %q, want %q", ctx.IP, "192.168.1.1")
	}
	if ctx.UserAgent != "Mozilla/5.0" {
		t.Errorf("UserAgent = %q, want %q", ctx.UserAgent, "Mozilla/5.0")
	}
}

func TestExceptionContext_WithIDs(t *testing.T) {
	ctx := NewExceptionContext().WithIDs("req-123", "trace-456")

	if ctx.RequestID != "req-123" {
		t.Errorf("RequestID = %q, want %q", ctx.RequestID, "req-123")
	}
	if ctx.TraceID != "trace-456" {
		t.Errorf("TraceID = %q, want %q", ctx.TraceID, "trace-456")
	}
}

func TestExceptionContext_WithUserID(t *testing.T) {
	ctx := NewExceptionContext().WithUserID("user-789")

	if ctx.UserID != "user-789" {
		t.Errorf("UserID = %q, want %q", ctx.UserID, "user-789")
	}
}

func TestExceptionContext_WithStackTrace(t *testing.T) {
	st := CaptureStackTrace(0)
	ctx := NewExceptionContext().WithStackTrace(st)

	if ctx.StackTrace != st {
		t.Error("StackTrace not set correctly")
	}
}

func TestExceptionContext_WithExtra(t *testing.T) {
	ctx := NewExceptionContext().
		WithExtra("key1", "value1").
		WithExtra("key2", 42)

	if ctx.Extra["key1"] != "value1" {
		t.Error("Extra key1 not set")
	}
	if ctx.Extra["key2"] != 42 {
		t.Error("Extra key2 not set")
	}
}

func TestExceptionContext_WithExtra_NilExtra(t *testing.T) {
	ctx := &ExceptionContext{}
	ctx.Extra = nil

	ctx.WithExtra("key", "value")
	if ctx.Extra == nil {
		t.Error("WithExtra should initialize nil Extra")
	}
}

func TestNewLogReporter(t *testing.T) {
	reporter := NewLogReporter()
	if reporter == nil {
		t.Fatal("NewLogReporter returned nil")
	}
	if !reporter.includeCtx {
		t.Error("includeCtx should default to true")
	}
}

func TestNewLogReporter_WithOptions(t *testing.T) {
	mockLogger := &mockLogger{}

	reporter := NewLogReporter(
		WithLogger(mockLogger),
		WithContextKeys("key1", "key2"),
		WithoutContext(),
	)

	if reporter.logger != mockLogger {
		t.Error("Logger not set")
	}
	if len(reporter.contextKeys) != 2 {
		t.Error("contextKeys not set")
	}
	if reporter.includeCtx {
		t.Error("includeCtx should be false after WithoutContext")
	}
}

func TestLogReporter_Report(t *testing.T) {
	mockLogger := &mockLogger{}
	reporter := NewLogReporter(WithLogger(mockLogger))

	err := NewHttpException(500, "test error")
	ctx := NewExceptionContext().
		WithIDs("req-1", "trace-1").
		WithUserID("user-1").
		WithRequestInfo("GET", "/test", "1.2.3.4", "TestAgent").
		WithStackTrace(CaptureStackTrace(0)).
		WithExtra("custom", "value")

	reporter.Report(err, ctx)

	if len(mockLogger.errorCalls) != 1 {
		t.Fatal("Expected one Error call")
	}

	call := mockLogger.errorCalls[0]
	if call.message != "test error" {
		t.Errorf("Message = %q, want %q", call.message, "test error")
	}
}

func TestLogReporter_Report_WithNilContext(t *testing.T) {
	mockLogger := &mockLogger{}
	reporter := NewLogReporter(WithLogger(mockLogger))

	err := errors.New("simple error")
	reporter.Report(err, nil)

	if len(mockLogger.errorCalls) != 1 {
		t.Fatal("Expected one Error call")
	}
}

func TestLogReporter_Report_WithoutContext(t *testing.T) {
	mockLogger := &mockLogger{}
	reporter := NewLogReporter(WithLogger(mockLogger), WithoutContext())

	err := NewHttpException(500, "test")
	ctx := NewExceptionContext().WithIDs("req-1", "trace-1")

	reporter.Report(err, ctx)

	if len(mockLogger.errorCalls) != 1 {
		t.Fatal("Expected one Error call")
	}
}

func TestLogReporter_Report_Exception(t *testing.T) {
	mockLogger := &mockLogger{}
	reporter := NewLogReporter(WithLogger(mockLogger))

	prev := errors.New("previous")
	err := NewBaseException("test", 100).
		WithPrevious(prev).
		WithContext("excKey", "excValue")

	reporter.Report(err, nil)

	if len(mockLogger.errorCalls) != 1 {
		t.Fatal("Expected one Error call")
	}

	// Check that exception fields are included
	fields := mockLogger.errorCalls[0].fields
	found := false
	for i := 0; i < len(fields); i += 2 {
		if fields[i] == "code" && fields[i+1] == 100 {
			found = true
			break
		}
	}
	if !found {
		t.Error("Exception code not in fields")
	}
}

func TestLogReporter_Report_NilLogger(t *testing.T) {
	// This will use the default logger from log package
	reporter := NewLogReporter()
	err := errors.New("test")

	// Should not panic
	reporter.Report(err, nil)
}

func TestNewCallbackReporter(t *testing.T) {
	var called bool
	var capturedErr error
	var capturedCtx *ExceptionContext

	reporter := NewCallbackReporter(func(err error, ctx *ExceptionContext) {
		called = true
		capturedErr = err
		capturedCtx = ctx
	})

	testErr := errors.New("test error")
	testCtx := NewExceptionContext()

	reporter.Report(testErr, testCtx)

	if !called {
		t.Error("Callback was not called")
	}
	if capturedErr != testErr {
		t.Error("Error not passed to callback")
	}
	if capturedCtx != testCtx {
		t.Error("Context not passed to callback")
	}
}

func TestCallbackReporter_NilCallback(t *testing.T) {
	reporter := NewCallbackReporter(nil)

	// Should not panic
	reporter.Report(errors.New("test"), nil)
}

func TestNewMultiReporter(t *testing.T) {
	r1 := NewCallbackReporter(func(err error, ctx *ExceptionContext) {})
	r2 := NewCallbackReporter(func(err error, ctx *ExceptionContext) {})

	multi := NewMultiReporter(r1, r2)

	if len(multi.reporters) != 2 {
		t.Errorf("Expected 2 reporters, got %d", len(multi.reporters))
	}
}

func TestMultiReporter_Report(t *testing.T) {
	callCount := 0
	var mu sync.Mutex

	r1 := NewCallbackReporter(func(err error, ctx *ExceptionContext) {
		mu.Lock()
		callCount++
		mu.Unlock()
	})
	r2 := NewCallbackReporter(func(err error, ctx *ExceptionContext) {
		mu.Lock()
		callCount++
		mu.Unlock()
	})

	multi := NewMultiReporter(r1, r2)
	multi.Report(errors.New("test"), nil)

	if callCount != 2 {
		t.Errorf("Expected 2 calls, got %d", callCount)
	}
}

func TestMultiReporter_AddReporter(t *testing.T) {
	multi := NewMultiReporter()

	if len(multi.reporters) != 0 {
		t.Error("Should start empty")
	}

	r := NewCallbackReporter(func(err error, ctx *ExceptionContext) {})
	multi.AddReporter(r)

	if len(multi.reporters) != 1 {
		t.Error("Should have one reporter after add")
	}
}

// mockLogger for testing
type mockLogger struct {
	errorCalls []mockLogCall
	mu         sync.Mutex
}

type mockLogCall struct {
	message string
	fields  []any
}

func (m *mockLogger) Debug(message string, fields ...any) {}
func (m *mockLogger) Info(message string, fields ...any)  {}
func (m *mockLogger) Warn(message string, fields ...any)  {}
func (m *mockLogger) Error(message string, fields ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errorCalls = append(m.errorCalls, mockLogCall{message: message, fields: fields})
}
func (m *mockLogger) Fatal(message string, fields ...any) {}
func (m *mockLogger) WithFields(fields map[string]any) interface {
	Debug(message string, fields ...any)
	Info(message string, fields ...any)
	Warn(message string, fields ...any)
	Error(message string, fields ...any)
	Fatal(message string, fields ...any)
} {
	return m
}

func TestLogReporter_buildFields_PartialContext(t *testing.T) {
	mockLogger := &mockLogger{}
	reporter := NewLogReporter(WithLogger(mockLogger))

	// Context with only some fields set
	ctx := NewExceptionContext()
	ctx.RequestID = "req-123"
	// Leave other fields empty

	err := errors.New("test")
	reporter.Report(err, ctx)

	if len(mockLogger.errorCalls) != 1 {
		t.Fatal("Expected one Error call")
	}
}

func TestExceptionContext_Chaining(t *testing.T) {
	// Test that all methods return the context for chaining
	ctx := NewExceptionContext().
		WithRequestInfo("GET", "/test", "1.2.3.4", "Agent").
		WithIDs("req", "trace").
		WithUserID("user").
		WithStackTrace(CaptureStackTrace(0)).
		WithExtra("key", "value")

	if ctx.Method != "GET" {
		t.Error("Chaining failed for WithRequestInfo")
	}
	if ctx.RequestID != "req" {
		t.Error("Chaining failed for WithIDs")
	}
	if ctx.UserID != "user" {
		t.Error("Chaining failed for WithUserID")
	}
	if ctx.StackTrace == nil {
		t.Error("Chaining failed for WithStackTrace")
	}
	if ctx.Extra["key"] != "value" {
		t.Error("Chaining failed for WithExtra")
	}
}

func TestExceptionContext_Timestamp(t *testing.T) {
	before := time.Now()
	ctx := NewExceptionContext()
	after := time.Now()

	if ctx.Timestamp.Before(before) || ctx.Timestamp.After(after) {
		t.Error("Timestamp should be within the test execution time")
	}
}
