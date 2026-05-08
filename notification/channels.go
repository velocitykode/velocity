package notification

import (
	"context"

	"github.com/velocitykode/velocity/driverregistry"
)

// ChannelConfig is the configuration handed to a channel factory. It is
// currently empty because notification channels are constructed without
// per-channel parameters and are wired with their dependencies (mailer,
// db, ...) after construction (see initNotification in factories.go).
//
// Keeping a typed config here (instead of struct{} or any) reserves room
// for future channel-specific configuration without churning every driver
// signature.
type ChannelConfig struct{}

// drivers is the canonical Velocity driver registry for notification
// channels. Channel authors call Drivers().Register("name", factory) from
// an init().
var drivers = driverregistry.New[Channel, ChannelConfig]("notification")

// Drivers returns the registry that channel drivers register themselves
// into. Use this from a channel package's init() to install a factory:
//
//	func init() {
//	    notification.Drivers().Register("slack", func(_ context.Context, _ notification.ChannelConfig) (notification.Channel, error) {
//	        return NewSlackChannel(), nil
//	    })
//	}
func Drivers() *driverregistry.Registry[Channel, ChannelConfig] { return drivers }

// createChannel resolves a channel by name from the registry.
func createChannel(name string) (Channel, error) {
	return drivers.Resolve(context.Background(), name, ChannelConfig{})
}

// RegisteredChannels returns the names of all registered channel drivers.
// Provided for diagnostics and the "did you mean?" hint paths.
func RegisteredChannels() []string {
	return drivers.Names()
}
