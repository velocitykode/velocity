package notification

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventmeta"
)

// NotificationSent is dispatched after a notification is delivered
// successfully through one channel.
//
// The event carries what identifies the delivery, not the notification or
// the notifiable themselves, so a queued listener in another process
// rebuilds it from its JSON form. The type labels are diagnostics (the Go
// type, as reflect prints it), not keys to rebuild a value from; each is
// empty for a nil value.
type NotificationSent struct {
	contract.EventMeta
	// NotificationType is the notification's Go type.
	NotificationType string
	// NotificationID is the send's notification ID (see IDFromContext),
	// shared by every channel's delivery of one send. It is empty when the
	// send failed before an ID was assigned.
	NotificationID string
	// NotifiableType is the notifiable's Go type.
	NotifiableType string
	// NotifiableID is the notifiable's own ID when it implements
	// NotifiableIdentifier, and empty otherwise or when that method panics.
	NotifiableID string
	// Channel is the channel the notification went through.
	Channel string
	// Duration is how long the channel's Send ran.
	Duration time.Duration
}

// Name returns the event name.
func (e *NotificationSent) Name() string {
	return "notification.completed"
}

// NotificationFailed is dispatched when a notification fails to deliver
// through one channel. It carries the same identity as NotificationSent
// and the failure. Duration is how long the channel's Send ran before it
// failed; it is zero when the channel could not be resolved or the delivery
// panicked.
type NotificationFailed struct {
	contract.EventMeta
	// NotificationType is the notification's Go type.
	NotificationType string
	// NotificationID is the send's notification ID; empty when the send
	// failed before an ID was assigned.
	NotificationID string
	// NotifiableType is the notifiable's Go type.
	NotifiableType string
	// NotifiableID is the notifiable's own ID (see NotifiableIdentifier),
	// or empty.
	NotifiableID string
	// Channel is the channel the delivery failed on, empty when the send
	// failed before a channel was chosen.
	Channel string
	// Err is the failure. Its JSON form is its text.
	Err      error
	Duration time.Duration
}

// Name returns the event name.
func (e *NotificationFailed) Name() string {
	return "notification.failed"
}

// NotifiableIdentifier is implemented by a notifiable that can name
// itself, so NotificationSent and NotificationFailed carry its ID.
// NotifiableID is called once per event, outside any manager lock; a panic
// in it is contained and leaves the ID empty.
type NotifiableIdentifier interface {
	NotifiableID() string
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
		EventMeta:        eventmeta.Current(ctx),
		NotificationType: typeLabel(n),
		NotificationID:   IDFromContext(ctx),
		NotifiableType:   typeLabel(notifiable),
		NotifiableID:     notifiableID(notifiable),
		Channel:          channel,
		Duration:         duration,
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
		EventMeta:        eventmeta.Current(ctx),
		NotificationType: typeLabel(n),
		NotificationID:   IDFromContext(ctx),
		NotifiableType:   typeLabel(notifiable),
		NotifiableID:     notifiableID(notifiable),
		Channel:          channel,
		Err:              err,
		Duration:         duration,
	})
}

// typeLabel returns v's Go type as reflect prints it, or "" for nil.
func typeLabel(v any) string {
	if v == nil {
		return ""
	}
	return reflect.TypeOf(v).String()
}

// notifiableID returns the notifiable's own ID when it implements
// NotifiableIdentifier. The method is user code: a panic in it is
// contained and gives "".
func notifiableID(notifiable any) (id string) {
	ni, ok := notifiable.(NotifiableIdentifier)
	if !ok {
		return ""
	}
	defer func() {
		if recover() != nil {
			id = ""
		}
	}()
	return ni.NotifiableID()
}
