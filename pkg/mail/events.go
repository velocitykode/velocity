package mail

import (
	"context"
	"time"

	"github.com/velocitykode/velocity/pkg/trace"
)

// MailSent is dispatched after an email is sent successfully
type MailSent struct {
	Context    context.Context
	To         []string
	Subject    string
	Channel    string
	DurationMs int64
	TraceID    string
	SpanID     string
	ParentID   string
}

// Name returns the event name
func (e *MailSent) Name() string {
	return "mail.sent"
}

// MailFailed is dispatched when an email fails to send
type MailFailed struct {
	Context    context.Context
	To         []string
	Subject    string
	Channel    string
	Error      string
	DurationMs int64
	TraceID    string
	SpanID     string
	ParentID   string
}

// Name returns the event name
func (e *MailFailed) Name() string {
	return "mail.failed"
}

// dispatchMailSent dispatches a MailSent event
func dispatchMailSent(dispatch func(interface{}), ctx context.Context, to []string, subject, channel string, duration time.Duration) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	dispatch(&MailSent{
		Context:    ctx,
		To:         to,
		Subject:    subject,
		Channel:    channel,
		DurationMs: duration.Milliseconds(),
		TraceID:    traceID,
		SpanID:     spanID,
		ParentID:   parentID,
	})
}

// dispatchMailFailed dispatches a MailFailed event
func dispatchMailFailed(dispatch func(interface{}), ctx context.Context, to []string, subject, channel string, err error, duration time.Duration) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	dispatch(&MailFailed{
		Context:    ctx,
		To:         to,
		Subject:    subject,
		Channel:    channel,
		Error:      errMsg,
		DurationMs: duration.Milliseconds(),
		TraceID:    traceID,
		SpanID:     spanID,
		ParentID:   parentID,
	})
}
