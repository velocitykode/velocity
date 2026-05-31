package contract

import "context"

// Notification defines a notification that can be sent through one or more channels.
type Notification interface {
	// Via returns the channel names this notification should be delivered through.
	// Channel names correspond to registered NotificationChannel drivers (e.g., "mail", "database", "broadcast", "slack").
	Via(notifiable interface{}) []string
}

// NotificationChannel is a notification delivery mechanism.
// Each channel knows how to deliver a notification through its transport (mail, DB, etc.).
type NotificationChannel interface {
	// Send delivers the notification to the notifiable via this channel.
	Send(ctx context.Context, notifiable interface{}, notification Notification) error
}

// Notifier is the interface satisfied by the notification manager. It covers the
// methods used through app.Services and router.Context for sending notifications.
type Notifier interface {
	Send(ctx context.Context, notifiable interface{}, notification Notification) error
	SendMany(ctx context.Context, notifiables []interface{}, notification Notification) error
	Channel(name string) (NotificationChannel, error)
	SetChannel(name string, ch NotificationChannel)
	SetEventDispatcher(fn func(ctx context.Context, event interface{}) error)
	Shutdown(ctx context.Context) error
}
