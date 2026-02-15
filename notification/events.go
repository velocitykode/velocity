package notification

import (
	"context"
	"fmt"
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

// dispatchNotificationSent dispatches a NotificationSent event.
func dispatchNotificationSent(dispatch func(interface{}), ctx context.Context, notifiable interface{}, n Notification, channel string, duration time.Duration) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	dispatch(&NotificationSent{
		Context:      ctx,
		Notifiable:   notifiable,
		Notification: n,
		Channel:      channel,
		DurationMs:   duration.Milliseconds(),
		TraceID:      traceID,
		SpanID:       spanID,
		ParentID:     parentID,
	})
}

// dispatchNotificationFailed dispatches a NotificationFailed event.
func dispatchNotificationFailed(dispatch func(interface{}), ctx context.Context, notifiable interface{}, n Notification, channel string, err error) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	dispatch(&NotificationFailed{
		Context:      ctx,
		Notifiable:   notifiable,
		Notification: n,
		Channel:      channel,
		Error:        errMsg,
		TraceID:      traceID,
		SpanID:       spanID,
		ParentID:     parentID,
	})
}

// notificationTypeName returns a human-readable type name for a notification.
// Used internally for logging and event identification.
func notificationTypeName(n Notification) string {
	return fmt.Sprintf("%T", n)
}
