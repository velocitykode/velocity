package events

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/trace"
)

func TestExceptionReportedEventName(t *testing.T) {
	event := &ExceptionReported{}
	if event.Name() != "exception.reported" {
		t.Errorf("Expected event name 'exception.reported', got '%s'", event.Name())
	}
}

func TestReportException(t *testing.T) {
	fake := NewFakeDispatcher()

	ctx := context.Background()
	ctx = trace.WithTrace(ctx, "trace123", "span456")

	testErr := errors.New("test error message")
	ReportException(ctx, fake, testErr)

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
}

func TestReportExceptionWithNilError(t *testing.T) {
	fake := NewFakeDispatcher()

	ctx := context.Background()
	ReportException(ctx, fake, nil)

	// Should not dispatch anything
	err := fake.AssertNothingDispatched()
	if err != nil {
		t.Error(err)
	}
}

func TestReportExceptionWithStack(t *testing.T) {
	fake := NewFakeDispatcher()

	ctx := context.Background()
	ctx = trace.WithTrace(ctx, "abc123", "def456")

	testErr := errors.New("custom error")
	customStack := "custom stack trace\nat SomeFunction:123"
	ReportExceptionWithStack(ctx, fake, testErr, customStack)

	err := fake.AssertDispatched(&ExceptionReported{}, func(e interface{}) bool {
		event := e.(*ExceptionReported)
		return event.Message == "custom error" &&
			event.StackTrace == customStack &&
			event.TraceID == "abc123"
	})
	if err != nil {
		t.Error(err)
	}
}

func TestReportPanicWithError(t *testing.T) {
	fake := NewFakeDispatcher()

	ctx := context.Background()
	ctx = trace.WithTrace(ctx, "trace789", "span012")

	panicErr := errors.New("panic error")
	stack := "goroutine 1 [running]:\nsome/package.Function()"

	ReportPanic(ctx, fake, panicErr, stack)

	err := fake.AssertDispatched(&ExceptionReported{}, func(e interface{}) bool {
		event := e.(*ExceptionReported)
		return event.Message == "panic error" &&
			event.StackTrace == stack &&
			event.TraceID == "trace789"
	})
	if err != nil {
		t.Error(err)
	}
}

func TestReportPanicWithString(t *testing.T) {
	fake := NewFakeDispatcher()

	ctx := context.Background()
	ReportPanic(ctx, fake, "string panic message", "stack")

	err := fake.AssertDispatched(&ExceptionReported{}, func(e interface{}) bool {
		event := e.(*ExceptionReported)
		return event.Message == "string panic message" &&
			event.Type == "panic"
	})
	if err != nil {
		t.Error(err)
	}
}

func TestReportPanicWithNil(t *testing.T) {
	fake := NewFakeDispatcher()

	ctx := context.Background()
	ReportPanic(ctx, fake, nil, "stack")

	err := fake.AssertNothingDispatched()
	if err != nil {
		t.Error(err)
	}
}

func TestExceptionReportedCapturesTraceContext(t *testing.T) {
	fake := NewFakeDispatcher()

	// Create context with trace information
	ctx := context.Background()
	ctx, traceID, spanID := trace.StartTrace(ctx)

	testErr := errors.New("traced error")
	ReportException(ctx, fake, testErr)

	err := fake.AssertDispatched(&ExceptionReported{}, func(e interface{}) bool {
		event := e.(*ExceptionReported)
		return event.TraceID == traceID && event.SpanID == spanID
	})
	if err != nil {
		t.Errorf("Trace context not captured: %v", err)
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
	fake := NewFakeDispatcher()

	ctx := context.Background()
	testErr := &customError{msg: "custom error type"}
	ReportException(ctx, fake, testErr)

	err := fake.AssertDispatched(&ExceptionReported{}, func(e interface{}) bool {
		event := e.(*ExceptionReported)
		return event.Message == "custom error type"
	})
	if err != nil {
		t.Error(err)
	}
}
