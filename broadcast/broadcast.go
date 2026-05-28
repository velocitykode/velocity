package broadcast

import (
	"context"
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
	ctx         context.Context
}

// BroadcastManager is the default implementation
type BroadcastManager struct {
	driver     Driver
	authorizer Authorizer
	presence   PresenceDataFunc
	authSecret []byte
	mu         sync.RWMutex
}

// Driver defines the interface for broadcast drivers. Methods that fan out
// over a network or a blocking send loop come in pairs: a `Ctx`-suffixed
// variant that threads the caller's context.Context through so a slow client
// cannot pin the request goroutine, and a non-Ctx Deprecated shim that calls
// the Ctx variant with context.Background(). New code MUST call the Ctx
// variants.
//
// Implementations must pass broadcasttest.RunDriverContractTests. See
// broadcasttest for the executable specification.
type Driver interface {
	// BroadcastCtx sends an event to channels. Implementations MUST
	// honour ctx cancellation: a ctx whose Err() is already non-nil at
	// call time MUST return that error before touching the wire.
	// Enforced by broadcasttest.BroadcastCtx_CancelledCtx_ReturnsError.
	BroadcastCtx(ctx context.Context, channels []string, event string, data interface{}) error

	// Deprecated: use BroadcastCtx with a request-scoped context.Context.
	Broadcast(channels []string, event string, data interface{}) error

	// BroadcastExceptCtx broadcasts to all except specified socket. Same
	// ctx-cancellation contract as BroadcastCtx; a pre-cancelled ctx
	// MUST surface ctx.Err() before any send. Enforced by
	// broadcasttest.BroadcastExceptCtx_CancelledCtx_ReturnsError.
	BroadcastExceptCtx(ctx context.Context, channels []string, event string, data interface{}, socketID string) error

	// Deprecated: use BroadcastExceptCtx with a request-scoped context.Context.
	BroadcastExcept(channels []string, event string, data interface{}, socketID string) error

	// GetClients returns clients in a channel. This is a pure in-memory snapshot
	// in built-in drivers, so no Ctx variant is required on the interface;
	// future drivers that perform a cluster lookup may expose their own
	// GetClientsCtx as an optional extension.
	GetClients(channel string) []string
}

// TokenVerifierSetter is an optional driver capability that lets the
// BroadcastManager auto-install an HMAC token verifier on the driver
// whenever an auth secret is configured. Drivers satisfy this interface
// structurally (no explicit declaration needed). When the manager calls
// SetTokenVerifier with a non-nil function, the driver MUST require and
// verify an auth token for every private/presence subscribe; passing nil
// clears the requirement.
//
// Wiring this interface closes the audit H-25 gap where a consumer that
// configured a secret but forgot to call driver.SetTokenVerifier directly
// would silently leave subscribes unauthenticated.
type TokenVerifierSetter interface {
	SetTokenVerifier(fn func(socketID, channel, token string) bool)
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

// WithContext threads the caller's context.Context through Emit so a slow
// broadcast can be cancelled when the request context is cancelled.
func (cb *ChannelBuilder) WithContext(ctx context.Context) *ChannelBuilder {
	cb.ctx = ctx
	return cb
}

// Emit broadcasts an event to the channels.
//
// Deprecated: use EmitCtx with a request-scoped context.Context, or chain
// WithContext on the builder.
func (cb *ChannelBuilder) Emit(event string, data interface{}) error {
	ctx := cb.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return cb.EmitCtx(ctx, event, data)
}

// EmitCtx broadcasts an event to the channels using the provided context.
func (cb *ChannelBuilder) EmitCtx(ctx context.Context, event string, data interface{}) error {
	if !cb.condition {
		return nil
	}

	if cb.toOthers != "" {
		return cb.broadcaster.driver.BroadcastExceptCtx(ctx, cb.channels, event, data, cb.toOthers)
	}

	return cb.broadcaster.driver.BroadcastCtx(ctx, cb.channels, event, data)
}

// Auth handles channel authorization. Private- and presence- channels always
// require an authorizer; the zero-value default denies every request.
// Public channels bypass the authorizer entirely.
//
// For private- and presence- channels, when an auth secret has been installed
// via SetAuthSecret, the response includes an "auth" field carrying
// hex(HMAC-SHA256(socketID ":" channel)). The client is expected to forward
// that value to the WebSocket server when it subscribes, where the driver
// re-verifies the HMAC via VerifyAuthToken (see audit H-25). Without the
// token, a stolen authorizer verdict alone is not sufficient to bind a
// WebSocket connection to a restricted channel.
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

	// Compute the channel-auth signature once if a secret is configured. The
	// signature binds (socketID, channel) so a leaked authorizer verdict
	// cannot be replayed against a different connection.
	var sig string
	if isRestricted && len(b.authSecret) > 0 {
		sig = computeAuthSignature(b.authSecret, socketID, channel)
	}

	// For presence channels with a presence-data func, return the user data
	// alongside the auth token so the client can forward both.
	if isPresenceChannel(channel) && b.presence != nil {
		data := b.presence(channel, user)
		if sig == "" {
			return data, nil
		}
		return map[string]interface{}{
			"auth":         sig,
			"channel_data": data,
		}, nil
	}

	resp := map[string]interface{}{"status": "authorized"}
	if sig != "" {
		resp["auth"] = sig
	}
	return resp, nil
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
//
// When the configured driver implements TokenVerifierSetter (the websocket
// driver does), this also auto-wires the driver's subscribe-time verifier
// to b.VerifyAuthToken. A subsequent call with an empty secret clears the
// verifier so the driver returns to authorizer-only mode. This guarantees
// that any consumer that installs a secret is protected against the
// audit H-25 default-bypass without an extra explicit wiring step.
func (b *BroadcastManager) SetAuthSecret(secret []byte) {
	b.mu.Lock()
	if len(secret) == 0 {
		b.authSecret = nil
	} else {
		cp := make([]byte, len(secret))
		copy(cp, secret)
		b.authSecret = cp
	}
	setter, _ := b.driver.(TokenVerifierSetter)
	hasSecret := len(b.authSecret) > 0
	b.mu.Unlock()

	if setter == nil {
		return
	}
	if hasSecret {
		// The closure delegates to VerifyAuthToken, which reads the
		// current secret under b.mu each call. That lets the secret
		// be rotated via a subsequent SetAuthSecret without rebuilding
		// the wiring.
		setter.SetTokenVerifier(b.VerifyAuthToken)
	} else {
		setter.SetTokenVerifier(nil)
	}
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
