package eventmeta

import (
	"context"
	"errors"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/trace"
)

// Current returns the envelope of an event recording work that runs under
// ctx's current span: ctx's trace, span and parent ids, stamped now. A nil
// ctx is treated as context.Background.
func Current(ctx context.Context) contract.EventMeta {
	if ctx == nil {
		ctx = context.Background()
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	return contract.EventMeta{Context: ctx, TraceID: traceID, SpanID: spanID, ParentID: parentID, At: time.Now()}
}

// Child returns the envelope of an event recording an operation that runs
// as a span of its own under ctx's current span (trace.ChildSpanIDs), a
// root span when ctx carries no trace, stamped now. A nil ctx is treated
// as context.Background.
func Child(ctx context.Context) contract.EventMeta {
	if ctx == nil {
		ctx = context.Background()
	}
	traceID, spanID, parentID := trace.ChildSpanIDs(ctx)
	return contract.EventMeta{Context: ctx, TraceID: traceID, SpanID: spanID, ParentID: parentID, At: time.Now()}
}

// ErrorText returns err's text, or "" for a nil error: the JSON form of a
// framework event's error field.
func ErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// TextError returns an error with text, or nil for "": an error field
// decoded from a framework event's JSON form.
func TextError(text string) error {
	if text == "" {
		return nil
	}
	return errors.New(text)
}
