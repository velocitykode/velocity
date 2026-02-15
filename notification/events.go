package notification

import (
	"context"
	"time"

	"github.com/velocitykode/velocity/trace"
)

// NotificationSent is dispatched after a notification is delivered successfully.
type NotificationSent struct {
	Context      context.Context
	Notifiable   interface{}
	Notification Notification
	Channel      string
	DurationMs   int64
	TraceID      string
	SpanID       string
	ParentID     string
}

// Name returns the event name.
func (e *NotificationSent) Name() string {
	return "notification.sent"
}

// NotificationFailed is dispatched when a notification fails to deliver.
type NotificationFailed struct {
	Context      context.Context
	Notifiable   interface{}
	Notification Notification
	Channel      string
	Error        string
	TraceID      string
	SpanID       string
	ParentID     string
}

// Name returns the event name.
func (e *NotificationFailed) Name() string {
	return "notification.failed"
}

// buildNotificationSent creates a NotificationSent event.
func buildNotificationSent(ctx context.Context, notifiable interface{}, n Notification, channel string, duration time.Duration) *NotificationSent {
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	return &NotificationSent{
		Context:      ctx,
		Notifiable:   notifiable,
		Notification: n,
		Channel:      channel,
		DurationMs:   duration.Milliseconds(),
		TraceID:      traceID,
		SpanID:       spanID,
		ParentID:     parentID,
	}
}

// buildNotificationFailed creates a NotificationFailed event.
func buildNotificationFailed(ctx context.Context, notifiable interface{}, n Notification, channel string, err error) *NotificationFailed {
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	return &NotificationFailed{
		Context:      ctx,
		Notifiable:   notifiable,
		Notification: n,
		Channel:      channel,
		Error:        errMsg,
		TraceID:      traceID,
		SpanID:       spanID,
		ParentID:     parentID,
	}
}
