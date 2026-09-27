package notification

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/trace"
)

// spanChannel records the span of the ctx each Send runs under.
type spanChannel struct {
	mu      sync.Mutex
	spanIDs []string
	err     error
}

func (c *spanChannel) Send(ctx context.Context, notifiable interface{}, n Notification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.spanIDs = append(c.spanIDs, trace.GetSpanID(ctx))
	return c.err
}

// TestSend_EventIsChildOfCallerSpan pins that delivering through one channel
// is its own span: the event carries the caller's trace id, a span id of its
// own and the caller's span as ParentID, and the channel runs inside that
// span.
func TestSend_EventIsChildOfCallerSpan(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sendErr error
	}{
		{name: "sent"},
		{name: "failed", sendErr: errors.New("channel down")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager()
			ch := &spanChannel{err: tc.sendErr}
			m.SetChannel("span", ch)

			var mu sync.Mutex
			var traceID, spanID, parentID string
			m.SetEventDispatcher(func(_ context.Context, ev interface{}) error {
				mu.Lock()
				defer mu.Unlock()
				switch e := ev.(type) {
				case *NotificationSent:
					traceID, spanID, parentID = e.TraceID, e.SpanID, e.ParentID
				case *NotificationFailed:
					traceID, spanID, parentID = e.TraceID, e.SpanID, e.ParentID
				}
				return nil
			})

			callerTrace, callerSpan := "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
			ctx := trace.WithTrace(context.Background(), callerTrace, callerSpan)
			_ = m.Send(ctx, &testNotifiable{email: "a@example.com", id: "1"}, &testNotification{subject: "s", channels: []string{"span"}})

			mu.Lock()
			defer mu.Unlock()
			if traceID != callerTrace {
				t.Errorf("TraceID = %q, want the caller's %q", traceID, callerTrace)
			}
			if spanID == "" || spanID == callerSpan {
				t.Errorf("SpanID = %q, want its own span (caller span %q)", spanID, callerSpan)
			}
			if parentID != callerSpan {
				t.Errorf("ParentID = %q, want the caller's span %q", parentID, callerSpan)
			}
			ch.mu.Lock()
			defer ch.mu.Unlock()
			if len(ch.spanIDs) != 1 || ch.spanIDs[0] != spanID {
				t.Errorf("channel ran in span %v, want the delivery's span %q", ch.spanIDs, spanID)
			}
		})
	}
}
