package events

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExceptionReportedEventName(t *testing.T) {
	event := &ExceptionReported{}
	if event.Name() != "exception.reported" {
		t.Errorf("Expected event name 'exception.reported', got '%s'", event.Name())
	}
}

func TestReportException(t *testing.T) {
	Reset()
	fake := Fake()

	ctx := context.Background()
	ctx = WithTrace(ctx, "trace123", "span456")

	testErr := errors.New("test error message")
	ReportException(ctx, testErr)

	// Verify event was dispatched
	err := fake.AssertDispatched(&ExceptionReported{}, func(e interface{}) bool {
		event := e.(*ExceptionReported)
		return event.Message == "test error message" &&
			event.TraceID == "trace123" &&
			event.SpanID == "span456" &&
			strings.Contains(event.StackTrace, "")
	})
	if err != nil {
		t.Error(err)
	}

	Reset()
}

func TestReportExceptionWithNilError(t *testing.T) {
	Reset()
	fake := Fake()

	ctx := context.Background()
	ReportException(ctx, nil)

	// Should not dispatch anything
	err := fake.AssertNothingDispatched()
	if err != nil {
		t.Error(err)
	}

	Reset()
}

func TestReportExceptionWithStack(t *testing.T) {
	Reset()
	fake := Fake()

	ctx := context.Background()
	ctx = WithTrace(ctx, "abc123", "def456")

	testErr := errors.New("custom error")
	customStack := "custom stack trace\nat SomeFunction:123"
	ReportExceptionWithStack(ctx, testErr, customStack)

	err := fake.AssertDispatched(&ExceptionReported{}, func(e interface{}) bool {
		event := e.(*ExceptionReported)
		return event.Message == "custom error" &&
			event.StackTrace == customStack &&
			event.TraceID == "abc123"
	})
	if err != nil {
		t.Error(err)
	}

	Reset()
}

func TestReportPanicWithError(t *testing.T) {
	Reset()
	fake := Fake()

	ctx := context.Background()
	ctx = WithTrace(ctx, "trace789", "span012")

	panicErr := errors.New("panic error")
	stack := "goroutine 1 [running]:\nsome/package.Function()"

	ReportPanic(ctx, panicErr, stack)

	err := fake.AssertDispatched(&ExceptionReported{}, func(e interface{}) bool {
		event := e.(*ExceptionReported)
		return event.Message == "panic error" &&
			event.StackTrace == stack &&
			event.TraceID == "trace789"
	})
	if err != nil {
		t.Error(err)
	}

	Reset()
}

func TestReportPanicWithString(t *testing.T) {
	Reset()
	fake := Fake()

	ctx := context.Background()
	ReportPanic(ctx, "string panic message", "stack")

	err := fake.AssertDispatched(&ExceptionReported{}, func(e interface{}) bool {
		event := e.(*ExceptionReported)
		return event.Message == "string panic message" &&
			event.Type == "panic"
	})
	if err != nil {
		t.Error(err)
	}

	Reset()
}

func TestReportPanicWithNil(t *testing.T) {
	Reset()
	fake := Fake()

	ctx := context.Background()
	ReportPanic(ctx, nil, "stack")

	err := fake.AssertNothingDispatched()
	if err != nil {
		t.Error(err)
	}

	Reset()
}

func TestExceptionReportedCapturesTraceContext(t *testing.T) {
	Reset()
	fake := Fake()

	// Create context with trace information
	ctx := context.Background()
	ctx, traceID, spanID := StartTrace(ctx)

	testErr := errors.New("traced error")
	ReportException(ctx, testErr)

	err := fake.AssertDispatched(&ExceptionReported{}, func(e interface{}) bool {
		event := e.(*ExceptionReported)
		return event.TraceID == traceID && event.SpanID == spanID
	})
	if err != nil {
		t.Errorf("Trace context not captured: %v", err)
	}

	Reset()
}

func TestTraceHelperReexports(t *testing.T) {
	// Test that the events package correctly re-exports trace functions
	ctx := context.Background()

	// Test GenerateTraceID
	traceID := GenerateTraceID()
	if len(traceID) != 32 {
		t.Errorf("Expected trace ID length 32, got %d", len(traceID))
	}

	// Test GenerateSpanID
	spanID := GenerateSpanID()
	if len(spanID) != 16 {
		t.Errorf("Expected span ID length 16, got %d", len(spanID))
	}

	// Test WithTrace and getters
	ctx = WithTrace(ctx, traceID, spanID)
	if got := GetTraceID(ctx); got != traceID {
		t.Errorf("Expected trace ID %s, got %s", traceID, got)
	}
	if got := GetSpanID(ctx); got != spanID {
		t.Errorf("Expected span ID %s, got %s", spanID, got)
	}

	// Test WithSpan
	ctx = WithSpan(ctx, "newspan123456789")
	if got := GetParentID(ctx); got != spanID {
		t.Errorf("Expected parent ID %s, got %s", spanID, got)
	}

	// Test GetTraceContext
	gotTrace, gotSpan, gotParent := GetTraceContext(ctx)
	if gotTrace != traceID {
		t.Errorf("Expected trace %s, got %s", traceID, gotTrace)
	}
	if gotSpan != "newspan123456789" {
		t.Errorf("Expected span newspan123456789, got %s", gotSpan)
	}
	if gotParent != spanID {
		t.Errorf("Expected parent %s, got %s", spanID, gotParent)
	}

	// Test StartTrace
	_, newTrace, newSpan := StartTrace(context.Background())
	if len(newTrace) != 32 || len(newSpan) != 16 {
		t.Error("StartTrace should generate valid IDs")
	}

	// Test ContinueTrace
	_, contSpan := ContinueTrace(ctx)
	if len(contSpan) != 16 {
		t.Errorf("ContinueTrace should generate valid span ID, got %s", contSpan)
	}
}

// customError is a custom error type for testing type extraction
type customError struct {
	msg string
}

func (e *customError) Error() string {
	return e.msg
}

func TestReportExceptionWithCustomErrorType(t *testing.T) {
	Reset()
	fake := Fake()

	ctx := context.Background()
	testErr := &customError{msg: "custom error type"}
	ReportException(ctx, testErr)

	err := fake.AssertDispatched(&ExceptionReported{}, func(e interface{}) bool {
		event := e.(*ExceptionReported)
		return event.Message == "custom error type"
	})
	if err != nil {
		t.Error(err)
	}

	Reset()
}
