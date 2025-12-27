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
	ErrInvalidMessage   = errors.New("invalid message")
)

// generateID generates a unique ID for clients
func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// DefaultConfig returns a default configuration
func DefaultConfig() Config {
	return Config{
		Host:            "0.0.0.0",
		Port:            6001,
		Path:            "/ws",
		AllowedOrigins:  []string{"*"},
		MaxConnections:  10000,
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		MaxMessageSize:  512 * 1024, // 512KB
		PingInterval:    30 * 1e9,   // 30 seconds
		PongTimeout:     60 * 1e9,   // 60 seconds
		WriteTimeout:    10 * 1e9,   // 10 seconds
	}
}
