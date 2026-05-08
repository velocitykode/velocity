package events

import (
	"context"
	"runtime"
	"strings"
)

// ExceptionReported is dispatched when an exception or error is reported to the APM system.
// This event captures error details and stack trace for monitoring and debugging.
type ExceptionReported struct {
	Context    context.Context
	Type       string // Exception/error type name
	Message    string // Error message
	StackTrace string // Stack trace
	TraceID    string // APM trace ID
	SpanID     string // APM span ID
}

// Name returns the event name
func (e *ExceptionReported) Name() string {
	return "exception.reported"
}

// ReportException dispatches an ExceptionReported event for the given error.
// It automatically captures the stack trace and extracts trace context from ctx.
func ReportException(ctx context.Context, d Dispatcher, err error) {
	if err == nil || d == nil {
		return
	}

	traceID, spanID, _ := GetTraceContext(ctx)
	stack := captureStack(3) // Skip ReportException and runtime.Callers

	d.Dispatch(ctx, &ExceptionReported{
		Context:    ctx,
		Type:       getErrorType(err),
		Message:    err.Error(),
		StackTrace: stack,
		TraceID:    traceID,
		SpanID:     spanID,
	})
}

// ReportExceptionWithStack dispatches an ExceptionReported event with a custom stack trace.
// Use this when you already have a captured stack trace (e.g., from panic recovery).
func ReportExceptionWithStack(ctx context.Context, d Dispatcher, err error, stack string) {
	if err == nil || d == nil {
		return
	}

	traceID, spanID, _ := GetTraceContext(ctx)

	d.Dispatch(ctx, &ExceptionReported{
		Context:    ctx,
		Type:       getErrorType(err),
		Message:    err.Error(),
		StackTrace: stack,
		TraceID:    traceID,
		SpanID:     spanID,
	})
}

// ReportPanic dispatches an ExceptionReported event for a recovered panic value.
// This should be called from a defer block after recover().
func ReportPanic(ctx context.Context, d Dispatcher, recovered interface{}, stack string) {
	if recovered == nil || d == nil {
		return
	}

	traceID, spanID, _ := GetTraceContext(ctx)

	var typeName, message string
	switch v := recovered.(type) {
	case error:
		typeName = getErrorType(v)
		message = v.Error()
	case string:
		typeName = "panic"
		message = v
	default:
		typeName = "panic"
		message = toString(v)
	}

	d.Dispatch(ctx, &ExceptionReported{
		Context:    ctx,
		Type:       typeName,
		Message:    message,
		StackTrace: stack,
		TraceID:    traceID,
		SpanID:     spanID,
	})
}

// captureStack captures the current stack trace as a string.
// skip specifies how many stack frames to skip.
func captureStack(skip int) string {
	const maxStackSize = 4096
	buf := make([]byte, maxStackSize)
	n := runtime.Stack(buf, false)

	// Skip the specified number of frames from the beginning
	stack := string(buf[:n])
	lines := strings.Split(stack, "\n")

	// Each stack frame takes 2 lines (function + file:line)
	// Plus the first line is "goroutine N [running]:"
	skipLines := 1 + (skip * 2)
	if skipLines < len(lines) {
		return strings.Join(lines[skipLines:], "\n")
	}
	return stack
}

// getErrorType returns the type name of an error.
func getErrorType(err error) string {
	if err == nil {
		return ""
	}

	// Get the concrete type name
	typeName := strings.TrimPrefix(
		strings.Replace(
			strings.Replace(
				typeNameOf(err),
				"*", "", -1,
			),
			"&", "", -1,
		),
		"main.",
	)

	return typeName
}

// typeNameOf returns the fully qualified type name using reflection-free approach.
func typeNameOf(v interface{}) string {
	if v == nil {
		return "<nil>"
	}
	// Use fmt.Sprintf with %T to get the type name
	return strings.TrimPrefix(
		strings.Replace(typeString(v), "*", "", 1),
		"&",
	)
}

// typeString returns the type as a string without importing reflect.
func typeString(v interface{}) string {
	switch v.(type) {
	case error:
		// For errors, try to get a more specific type
		if e, ok := v.(interface{ Unwrap() error }); ok {
			return typeString(e.Unwrap())
		}
	}
	// Fallback: use a simple type switch for common types
	switch v.(type) {
	case *string, string:
		return "string"
	case *int, int:
		return "int"
	case *bool, bool:
		return "bool"
	default:
		// Use error type name if available
		if err, ok := v.(error); ok {
			// Try to extract type name from error
			msg := err.Error()
			if idx := strings.Index(msg, ":"); idx > 0 {
				return "error"
			}
		}
		return "error"
	}
}

// toString converts an arbitrary value to string.
func toString(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case error:
		return val.Error()
	default:
		return "unknown"
	}
}
