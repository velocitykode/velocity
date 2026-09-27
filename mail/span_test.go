package mail

import (
	"context"
	"errors"
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
