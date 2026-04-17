// Package broadcast provides a high-level broadcasting system for real-time communication
// built on top of the WebSocket package and supporting multiple drivers
package broadcast

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"sync"
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
	broadcaster *BroadcastManager
	toOthers    string // Socket ID to exclude
	condition   bool
}

// BroadcastManager is the default implementation
type BroadcastManager struct {
	driver     Driver
	authorizer Authorizer
	presence   PresenceDataFunc
	authSecret []byte
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

// New creates a new broadcaster with the given driver.
// The default authorizer denies all requests — callers must install one via
// SetAuthorizer to enable access to private- or presence- channels.
func New(driver Driver) *BroadcastManager {
	return &BroadcastManager{
		driver:     driver,
		authorizer: denyAllAuthorizer,
	}
}

// denyAllAuthorizer is the secure default authorizer — rejects every request.
// Applications must explicitly install an authorizer via SetAuthorizer to
// permit access to private- or presence- channels.
func denyAllAuthorizer(channel string, user interface{}) bool {
	return false
}

// Channel returns a channel builder for the given channels
func (b *BroadcastManager) Channel(names ...string) *ChannelBuilder {
	return &ChannelBuilder{
		channels:    names,
		broadcaster: b,
		condition:   true,
	}
}

// Private returns a private channel builder
func (b *BroadcastManager) Private(name string) *ChannelBuilder {
	return &ChannelBuilder{
		channels:    []string{"private-" + name},
		broadcaster: b,
		condition:   true,
	}
}

// Presence returns a presence channel builder
func (b *BroadcastManager) Presence(name string) *ChannelBuilder {
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

// Auth handles channel authorization. Private- and presence- channels always
// require an authorizer; the zero-value default denies every request.
// Public channels bypass the authorizer entirely.
func (b *BroadcastManager) Auth(channel string, socketID string, user interface{}) (interface{}, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	isRestricted := isPrivateChannel(channel) || isPresenceChannel(channel)

	if isRestricted {
		if b.authorizer == nil {
			return nil, ErrUnauthorized
		}
		if !b.authorizer(channel, user) {
			return nil, ErrUnauthorized
		}
	}

	// For presence channels, return user data
	if isPresenceChannel(channel) && b.presence != nil {
		return b.presence(channel, user), nil
	}

	return map[string]interface{}{"status": "authorized"}, nil
}

// Leave handles user leaving presence channel
func (b *BroadcastManager) Leave(channel string, socketID string) error {
	// Implementation depends on driver
	return nil
}

// SetAuthorizer sets the channel authorizer
func (b *BroadcastManager) SetAuthorizer(fn Authorizer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.authorizer = fn
}

// SetPresenceData sets the presence data function
func (b *BroadcastManager) SetPresenceData(fn PresenceDataFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.presence = fn
}

// SetAuthSecret installs the HMAC secret used to sign and verify private- and
// presence- channel auth tokens. A copy of the secret is kept so subsequent
// mutations to the input do not affect the manager.
func (b *BroadcastManager) SetAuthSecret(secret []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(secret) == 0 {
		b.authSecret = nil
		return
	}
	cp := make([]byte, len(secret))
	copy(cp, secret)
	b.authSecret = cp
}

// SignAuthToken returns the HMAC-SHA256 signature for (socketID:channel) encoded
// as hex. Returns ErrUnauthorized if the auth secret has not been configured.
func (b *BroadcastManager) SignAuthToken(socketID, channel string) (string, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(b.authSecret) == 0 {
		return "", ErrUnauthorized
	}
	return computeAuthSignature(b.authSecret, socketID, channel), nil
}

// VerifyAuthToken checks a caller-supplied auth token for (socketID:channel).
// The comparison is performed in constant time via crypto/subtle to avoid
// timing side-channels that would leak the signature byte-by-byte.
func (b *BroadcastManager) VerifyAuthToken(socketID, channel, token string) bool {
	b.mu.RLock()
	secret := b.authSecret
	b.mu.RUnlock()

	if len(secret) == 0 {
		return false
	}
	expected := computeAuthSignature(secret, socketID, channel)
	// subtle.ConstantTimeCompare returns 1 iff lengths match AND bytes are equal.
	return subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

// computeAuthSignature returns hex(HMAC-SHA256(secret, socketID ":" channel)).
func computeAuthSignature(secret []byte, socketID, channel string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(socketID))
	mac.Write([]byte{':'})
	mac.Write([]byte(channel))
	return hex.EncodeToString(mac.Sum(nil))
}
