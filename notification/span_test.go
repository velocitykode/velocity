package notification

import (
	"context"
	"errors"
	"strings"
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

// nestedWorkPanicChannel does nested instrumented work inside the delivery
// (a child span, the way an HTTP call to a provider opens one) and then
// panics. It records the ids that nested work got.
type nestedWorkPanicChannel struct {
	mu                    sync.Mutex
	nestedTrace, nestedOf string // the child span's trace and parent
}

func (c *nestedWorkPanicChannel) Send(ctx context.Context, notifiable interface{}, n Notification) error {
	traceID, _, parentID := trace.ChildSpanIDs(ctx)
	c.mu.Lock()
	c.nestedTrace, c.nestedOf = traceID, parentID
	c.mu.Unlock()
	panic("channel boom after nested work")
}

// TestSendMany_PanicReportsTheDeliverySpan pins that a channel panic
// recovered by SendMany is reported as a failure of the span that channel's
// delivery ran in: exactly one NotificationFailed, whose span is the one the
// channel's nested work parented under and whose trace is that work's trace,
// with and without an enclosing trace, and not the span of a channel that
// delivered before it. The aggregated error is unchanged.
func TestSendMany_PanicReportsTheDeliverySpan(t *testing.T) {
	callerTrace, callerSpan := "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	for _, tc := range []struct {
		name     string
		traced   bool
		channels []string
	}{
		{name: "enclosing trace", traced: true, channels: []string{"nested"}},
		{name: "no enclosing trace", channels: []string{"nested"}},
		{name: "after another channel delivered", traced: true, channels: []string{"ok", "nested"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.traced {
				ctx = trace.WithTrace(ctx, callerTrace, callerSpan)
			}
			m := NewManager()
			ch := &nestedWorkPanicChannel{}
			m.SetChannel("nested", ch)
			m.SetChannel("ok", &spanChannel{})

			var mu sync.Mutex
			var failed []*NotificationFailed
			var sent []*NotificationSent
			m.SetEventDispatcher(func(_ context.Context, ev interface{}) error {
				mu.Lock()
				defer mu.Unlock()
				switch e := ev.(type) {
				case *NotificationFailed:
					failed = append(failed, e)
				case *NotificationSent:
					sent = append(sent, e)
				}
				return nil
			})

			n := &testNotification{subject: "s", channels: tc.channels}
			err := m.SendMany(ctx, []interface{}{&testNotifiable{email: "a@example.com", id: "1"}}, n)
			if err == nil || !strings.Contains(err.Error(), "velocity/notification: 1 of 1 sends failed: velocity/notification: send many panic: ") {
				t.Fatalf("SendMany error = %v, want the panic aggregated", err)
			}

			mu.Lock()
			defer mu.Unlock()
			if len(failed) != 1 {
				t.Fatalf("got %d NotificationFailed events, want exactly 1", len(failed))
			}
			ev := failed[0]
			ch.mu.Lock()
			nestedTrace, nestedOf := ch.nestedTrace, ch.nestedOf
			ch.mu.Unlock()
			if ev.SpanID == "" || ev.SpanID != nestedOf {
				t.Errorf("NotificationFailed.SpanID = %q, want the span the channel's nested work parented under (%q)", ev.SpanID, nestedOf)
			}
			if ev.TraceID != nestedTrace {
				t.Errorf("NotificationFailed.TraceID = %q, want the nested work's trace %q", ev.TraceID, nestedTrace)
			}
			if tc.traced {
				if ev.TraceID != callerTrace || ev.ParentID != callerSpan {
					t.Errorf("NotificationFailed trace %q parent %q, want the caller's trace %q and span %q", ev.TraceID, ev.ParentID, callerTrace, callerSpan)
				}
			} else if ev.ParentID != "" {
				t.Errorf("NotificationFailed.ParentID = %q, want a root span", ev.ParentID)
			}
			for _, s := range sent {
				if s.SpanID == ev.SpanID {
					t.Errorf("NotificationFailed.SpanID %q is the span of a channel that delivered", ev.SpanID)
				}
			}
		})
	}
}
