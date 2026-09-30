package eventmeta

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/trace"
)

func TestCurrent_ReadsTheContextSpan(t *testing.T) {
	ctx := trace.WithFullContext(context.Background(), "t1", "s1", "p1")
	before := time.Now()
	m := Current(ctx)
	if m.Context != ctx || m.TraceID != "t1" || m.SpanID != "s1" || m.ParentID != "p1" {
		t.Errorf("Current = %+v, want ctx's ids t1/s1/p1", m)
	}
	if m.At.Before(before) || m.At.After(time.Now()) {
		t.Errorf("Current.At = %v, want now", m.At)
	}
}

func TestChild_StartsASpanUnderTheContextSpan(t *testing.T) {
	ctx := trace.WithFullContext(context.Background(), "t1", "s1", "p1")
	m := Child(ctx)
	if m.TraceID != "t1" || m.ParentID != "s1" || m.SpanID == "" || m.SpanID == "s1" {
		t.Errorf("Child = %+v, want trace t1, a fresh span, parent s1", m)
	}
	root := Child(context.Background())
	if root.TraceID == "" || root.SpanID == "" || root.ParentID != "" {
		t.Errorf("Child(no trace) = %+v, want a root span", root)
	}
}

func TestNilContext_IsBackground(t *testing.T) {
	var nilCtx context.Context
	if Current(nilCtx).Context == nil || Child(nilCtx).Context == nil {
		t.Error("a nil ctx left the envelope's Context nil, want context.Background")
	}
}

func TestErrorText_RoundTrips(t *testing.T) {
	if ErrorText(nil) != "" || TextError("") != nil {
		t.Error("a nil error and the empty text are not each other's form")
	}
	if got := TextError(ErrorText(errors.New("boom"))); got == nil || got.Error() != "boom" {
		t.Errorf("TextError(ErrorText(boom)) = %v, want boom", got)
	}
}

// textPanics is an error whose Error panics.
type textPanics struct{}

func (textPanics) Error() string { panic("Error broke") }

// An event's error field whose Error panics reads as the fixed text: the
// event is still built and dispatched.
func TestErrorText_UnreadableError(t *testing.T) {
	var got string
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("ErrorText panicked: %v", p)
			}
		}()
		got = ErrorText(textPanics{})
	}()
	if got != errchain.Unreadable {
		t.Fatalf("ErrorText = %q, want the fixed text", got)
	}
}
