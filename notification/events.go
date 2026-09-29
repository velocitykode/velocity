package notification

import (
	"context"
	"encoding/json"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventmeta"
)

// NotificationSent is dispatched after a notification is delivered successfully.
type NotificationSent struct {
	contract.EventMeta
	Notifiable   interface{}
	Notification Notification
	Channel      string
	Duration     time.Duration
}

// Name returns the event name.
func (e *NotificationSent) Name() string {
	return "notification.completed"
}

// NotificationFailed is dispatched when a notification fails to deliver.
// Duration is how long the channel's Send ran before it failed; it is zero
// when the channel could not be resolved or the delivery panicked.
type NotificationFailed struct {
	contract.EventMeta
	Notifiable   interface{}
	Notification Notification
	Channel      string
	Err          error
	Duration     time.Duration
}

// Name returns the event name.
func (e *NotificationFailed) Name() string {
	return "notification.failed"
}

// MarshalJSON encodes the event with Err as its text.
func (e NotificationFailed) MarshalJSON() ([]byte, error) {
	type fields NotificationFailed
	return json.Marshal(struct {
		fields
		Err string `json:",omitempty"`
	}{fields(e), eventmeta.ErrorText(e.Err)})
}

// UnmarshalJSON decodes the event's JSON form: Err becomes an error with
// the encoded text.
func (e *NotificationFailed) UnmarshalJSON(data []byte) error {
	type fields NotificationFailed
	v := struct {
		*fields
		Err string `json:",omitempty"`
	}{fields: (*fields)(e)}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	e.Err = eventmeta.TextError(v.Err)
	return nil
}

// dispatchNotificationSent dispatches a NotificationSent event for the
// delivery running under ctx's span. The event is built only when a
// dispatcher is installed.
func (m *Manager) dispatchNotificationSent(ctx context.Context, notifiable interface{}, n Notification, channel string, duration time.Duration) {
	if !m.events.Installed() {
		return
	}
	m.dispatchEvent(ctx, &NotificationSent{
		EventMeta:    eventmeta.Current(ctx),
		Notifiable:   notifiable,
		Notification: n,
		Channel:      channel,
		Duration:     duration,
	})
}

// dispatchNotificationFailed dispatches a NotificationFailed event for the
// delivery running under ctx's span. The event is built only when a
// dispatcher is installed.
func (m *Manager) dispatchNotificationFailed(ctx context.Context, notifiable interface{}, n Notification, channel string, err error, duration time.Duration) {
	if !m.events.Installed() {
		return
	}
	m.dispatchEvent(ctx, &NotificationFailed{
		EventMeta:    eventmeta.Current(ctx),
		Notifiable:   notifiable,
		Notification: n,
		Channel:      channel,
		Err:          err,
		Duration:     duration,
	})
}
