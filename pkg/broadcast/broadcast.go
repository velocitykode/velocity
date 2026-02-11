// Package broadcast provides a high-level broadcasting system for real-time communication
// built on top of the WebSocket package and supporting multiple drivers
package broadcast

import (
	"sync"

	"github.com/velocitykode/velocity/pkg/broadcast/drivers"
	"github.com/velocitykode/velocity/pkg/websocket"
)

// Broadcaster defines the main broadcasting interface
type Broadcaster interface {
	// Channel returns a channel builder for broadcasting
	Channel(names ...string) *ChannelBuilder

	// Private returns a private channel builder
	Private(name string) *ChannelBuilder

	// Presence returns a presence channel builder
	Presence(name string) *ChannelBuilder

	// Auth handles channel authorization
	Auth(channel string, socketID string, user interface{}) (interface{}, error)

	// Leave handles user leaving presence channel
	Leave(channel string, socketID string) error
}

// Event represents a broadcast event
type Event interface {
	BroadcastOn() []string      // Channels to broadcast on
	BroadcastAs() string        // Event name
	BroadcastWith() interface{} // Data to broadcast
	BroadcastWhen() bool        // Conditional broadcasting
}

// ChannelBuilder builds broadcast operations
type ChannelBuilder struct {
	channels    []string
	broadcaster *broadcaster
	toOthers    string // Socket ID to exclude
	condition   bool
}

// broadcaster is the default implementation
type broadcaster struct {
	driver     Driver
	authorizer Authorizer
	presence   PresenceDataFunc
	mu         sync.RWMutex
}

// Driver defines the interface for broadcast drivers
type Driver interface {
	// Broadcast sends an event to channels
	Broadcast(channels []string, event string, data interface{}) error

	// BroadcastExcept broadcasts to all except specified socket
	BroadcastExcept(channels []string, event string, data interface{}, socketID string) error

	// GetClients returns clients in a channel
	GetClients(channel string) []string
}

// Authorizer is a function that authorizes channel access
type Authorizer func(channel string, user interface{}) bool

// PresenceDataFunc returns presence data for a user
type PresenceDataFunc func(channel string, user interface{}) interface{}

var (
	defaultBroadcaster *broadcaster
	once               sync.Once
)

// New creates a new broadcaster with the given driver
func New(driver Driver) *broadcaster {
	return &broadcaster{
		driver: driver,
	}
}

// SetDefault sets the global default broadcaster.
// Used by velocity.Default() to wire the App's broadcaster into the global.
func SetDefault(b *broadcaster) {
	defaultBroadcaster = b
}

// Default returns the default broadcaster instance
func Default() *broadcaster {
	once.Do(func() {
		// Use WebSocket driver by default
		defaultBroadcaster = New(drivers.NewWebSocketDriver(websocket.DefaultConfig()))
	})
	return defaultBroadcaster
}

// Channel returns a channel builder for the given channels
func Channel(names ...string) *ChannelBuilder {
	return Default().Channel(names...)
}

// Private returns a private channel builder
func Private(name string) *ChannelBuilder {
	return Default().Private(name)
}

// Presence returns a presence channel builder
func Presence(name string) *ChannelBuilder {
	return Default().Presence(name)
}

// Channel returns a channel builder for the given channels
func (b *broadcaster) Channel(names ...string) *ChannelBuilder {
	return &ChannelBuilder{
		channels:    names,
		broadcaster: b,
		condition:   true,
	}
}

// Private returns a private channel builder
func (b *broadcaster) Private(name string) *ChannelBuilder {
	return &ChannelBuilder{
		channels:    []string{"private-" + name},
		broadcaster: b,
		condition:   true,
	}
}

// Presence returns a presence channel builder
func (b *broadcaster) Presence(name string) *ChannelBuilder {
	return &ChannelBuilder{
		channels:    []string{"presence-" + name},
		broadcaster: b,
		condition:   true,
	}
}

// ToOthers excludes a socket from broadcast
func (cb *ChannelBuilder) ToOthers(socketID string) *ChannelBuilder {
	cb.toOthers = socketID
	return cb
}

// When adds a condition to the broadcast
func (cb *ChannelBuilder) When(condition bool) *ChannelBuilder {
	cb.condition = condition
	return cb
}

// Emit broadcasts an event to the channels
func (cb *ChannelBuilder) Emit(event string, data interface{}) error {
	if !cb.condition {
		return nil
	}

	if cb.toOthers != "" {
		return cb.broadcaster.driver.BroadcastExcept(cb.channels, event, data, cb.toOthers)
	}

	return cb.broadcaster.driver.Broadcast(cb.channels, event, data)
}

// Auth handles channel authorization
func (b *broadcaster) Auth(channel string, socketID string, user interface{}) (interface{}, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.authorizer == nil {
		return nil, nil
	}

	if !b.authorizer(channel, user) {
		return nil, ErrUnauthorized
	}

	// For presence channels, return user data
	if isPresenceChannel(channel) && b.presence != nil {
		return b.presence(channel, user), nil
	}

	return map[string]interface{}{"status": "authorized"}, nil
}

// Leave handles user leaving presence channel
func (b *broadcaster) Leave(channel string, socketID string) error {
	// Implementation depends on driver
	return nil
}

// SetAuthorizer sets the channel authorizer
func (b *broadcaster) SetAuthorizer(fn Authorizer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.authorizer = fn
}

// SetPresenceData sets the presence data function
func (b *broadcaster) SetPresenceData(fn PresenceDataFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.presence = fn
}

// SetAuthorizer sets the default authorizer
func SetAuthorizer(fn Authorizer) {
	Default().SetAuthorizer(fn)
}

// SetPresenceData sets the default presence data function
func SetPresenceData(fn PresenceDataFunc) {
	Default().SetPresenceData(fn)
}
