package mail

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/trace"
)

// spanCapturingMailer records the trace ids of the ctx each Send runs under.
type spanCapturingMailer struct {
	mu      sync.Mutex
	spanIDs []string
	err     error
}

func (m *spanCapturingMailer) Send(ctx context.Context, msg *Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spanIDs = append(m.spanIDs, trace.GetSpanID(ctx))
	return m.err
}

// TestManagerSend_EventIsChildOfCallerSpan pins that a send is its own
// span: the event carries the caller's trace id, a span id of its own and
// the caller's span as ParentID, and the driver runs inside that span so
// anything it does (an HTTP call to a mail API) parents under the send.
func TestManagerSend_EventIsChildOfCallerSpan(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sendErr error
	}{
		{name: "sent"},
		{name: "failed", sendErr: errors.New("smtp down")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager()
			mailer := &spanCapturingMailer{err: tc.sendErr}
			manager.SetChannel("default", mailer)

			var mu sync.Mutex
			var traceID, spanID, parentID string
			manager.SetEventDispatcher(func(_ context.Context, ev interface{}) error {
				mu.Lock()
				defer mu.Unlock()
				switch e := ev.(type) {
				case *MailSent:
					traceID, spanID, parentID = e.TraceID, e.SpanID, e.ParentID
				case *MailFailed:
					traceID, spanID, parentID = e.TraceID, e.SpanID, e.ParentID
				}
				return nil
			})

			callerTrace, callerSpan := "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
			ctx := trace.WithTrace(context.Background(), callerTrace, callerSpan)
			msg := NewMessage().To("test@example.com").Subject("Span").Body("Hello")
			_ = manager.Send(ctx, "default", msg)

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
			if len(mailer.spanIDs) != 1 || mailer.spanIDs[0] != spanID {
				t.Errorf("driver ran in span %v, want the send's span %q", mailer.spanIDs, spanID)
			}
		})
	}
}

// nestedWorkPanicMailer does nested instrumented work inside the send (a
// child span, the way an HTTP call to a mail API opens one) and then
// panics. It records the ids that nested work got.
type nestedWorkPanicMailer struct {
	mu                    sync.Mutex
	nestedTrace, nestedOf string // the child span's trace and parent
}

func (m *nestedWorkPanicMailer) Send(ctx context.Context, msg *Message) error {
	traceID, _, parentID := trace.ChildSpanIDs(ctx)
	m.mu.Lock()
	m.nestedTrace, m.nestedOf = traceID, parentID
	m.mu.Unlock()
	panic("mailer boom after nested work")
}

// TestManagerBroadcast_PanicReportsTheDeliverySpan pins that a driver panic
// recovered by Broadcast is reported as a failure of the span the send ran
// in: exactly one MailFailed, whose span is the one the driver's nested work
// parented under and whose trace is that work's trace, with and without an
// enclosing trace. The aggregated error is unchanged.
func TestManagerBroadcast_PanicReportsTheDeliverySpan(t *testing.T) {
	callerTrace, callerSpan := "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	for _, tc := range []struct {
		name   string
		traced bool
	}{
		{name: "enclosing trace", traced: true},
		{name: "no enclosing trace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.traced {
				ctx = trace.WithTrace(ctx, callerTrace, callerSpan)
			}
			manager := NewManager()
			mailer := &nestedWorkPanicMailer{}
			manager.SetChannel("nested", mailer)

			var mu sync.Mutex
			var failed []*MailFailed
			manager.SetEventDispatcher(func(_ context.Context, ev interface{}) error {
				if e, ok := ev.(*MailFailed); ok {
					mu.Lock()
					failed = append(failed, e)
					mu.Unlock()
				}
				return nil
			})

			msg := NewMessage().To("test@example.com").Subject("Span").Body("Hello")
			err := manager.Broadcast(ctx, []string{"nested"}, msg)
			if err == nil || !strings.Contains(err.Error(), "velocity/mail: channel nested panic: ") {
				t.Fatalf("Broadcast error = %v, want the channel's panic aggregated", err)
			}

			mu.Lock()
			defer mu.Unlock()
			if len(failed) != 1 {
				t.Fatalf("got %d MailFailed events, want exactly 1", len(failed))
			}
			ev := failed[0]
			mailer.mu.Lock()
			nestedTrace, nestedOf := mailer.nestedTrace, mailer.nestedOf
			mailer.mu.Unlock()
			if ev.SpanID == "" || ev.SpanID != nestedOf {
				t.Errorf("MailFailed.SpanID = %q, want the span the driver's nested work parented under (%q)", ev.SpanID, nestedOf)
			}
			if ev.TraceID != nestedTrace {
				t.Errorf("MailFailed.TraceID = %q, want the nested work's trace %q", ev.TraceID, nestedTrace)
			}
			if tc.traced {
				if ev.TraceID != callerTrace || ev.ParentID != callerSpan {
					t.Errorf("MailFailed trace %q parent %q, want the caller's trace %q and span %q", ev.TraceID, ev.ParentID, callerTrace, callerSpan)
				}
			} else if ev.ParentID != "" {
				t.Errorf("MailFailed.ParentID = %q, want a root span", ev.ParentID)
			}
		})
	}
}
