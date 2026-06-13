package websocket

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
)

// Common errors
var (
	ErrServerNotRunning = errors.New("server not running")
	ErrClientNotFound   = errors.New("client not found")
	ErrGroupNotFound    = errors.New("group not found")
	ErrSendChannelFull  = errors.New("send channel full")
	ErrConnectionLimit  = errors.New("connection limit reached")
	// ErrInvalidMessage is reserved for a future message-validation call site;
	// no server method wraps it yet.
	ErrInvalidMessage = errors.New("invalid message")

	// ErrServerClosed is returned by Start when called on a server that has
	// already been shut down. The lifecycle is one-shot: a Server cannot be
	// restarted after Shutdown (the stop channel is closed once and never
	// recreated). Mirrors net/http.ErrServerClosed.
	ErrServerClosed = errors.New("websocket: server closed")

	// ErrServerAlreadyRunning is returned by Start when the server is already
	// running. Start is not idempotent; call it exactly once per lifecycle.
	ErrServerAlreadyRunning = errors.New("websocket: server already running")
)

// DefaultMessageRateLimit is the per-client inbound messages-per-second cap
// applied when Config.MessageRateLimit is unset. Set MessageRateLimit to a
// negative value to explicitly opt out. Audit D-03.
const DefaultMessageRateLimit = 10

// randRead is a seam over crypto/rand.Read so tests can simulate a failing
// randomness source.
var randRead = rand.Read

// generateID generates a unique ID for clients. It fails closed: when the
// randomness source errors, the caller must refuse the connection rather
// than proceed with a predictable ID, since socket IDs bind channel-auth
// signatures.
func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := randRead(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// DefaultConfig returns a default configuration.
// AllowedOrigins defaults to nil (same-origin only).
// Set AllowedOrigins to []string{"*"} to explicitly allow all origins.
func DefaultConfig() Config {
	return Config{
		Host:            "0.0.0.0",
		Port:            6001,
		Path:            "/ws",
		AllowedOrigins:  nil, // same-origin only by default
		MaxConnections:  10000,
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		MaxMessageSize:  512 * 1024, // 512KB
		PingInterval:    30 * 1e9,   // 30 seconds
		PongTimeout:     60 * 1e9,   // 60 seconds
		WriteTimeout:    10 * 1e9,   // 10 seconds
	}
}
