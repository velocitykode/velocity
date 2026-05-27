package websocket

import (
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Message represents a WebSocket message
type Message struct {
	Type   string      `json:"type"`
	Data   interface{} `json:"data"`
	Target string      `json:"target,omitempty"` // For targeted messages
	From   string      `json:"from,omitempty"`   // Client ID
}

// Client represents a WebSocket connection
type Client struct {
	ID       string
	Conn     *websocket.Conn
	Send     chan Message
	Server   *Server
	Groups   map[string]bool
	Metadata map[string]interface{}
	mu       sync.RWMutex
}

// Config holds WebSocket server configuration
type Config struct {
	Host            string
	Port            int
	Path            string
	AllowedOrigins  []string
	MaxConnections  int
	ReadBufferSize  int
	WriteBufferSize int
	MaxMessageSize  int64
	PingInterval    time.Duration
	PongTimeout     time.Duration
	WriteTimeout    time.Duration
	// MessageRateLimit caps the number of inbound messages per second per
	// client. The zero value installs the secure default
	// (DefaultMessageRateLimit) so unconfigured deployments are not silently
	// unrate-limited. To explicitly opt out and run with no rate limit, set
	// this field to a negative value (e.g. -1). Audit D-03.
	MessageRateLimit int
	// MessageBurstSize is the maximum burst above MessageRateLimit before a
	// flooding client is disconnected. Zero defaults to 2x MessageRateLimit
	// once the rate limit default is applied.
	MessageBurstSize int
	AuthFunc         func(r *http.Request) error // Pre-upgrade authentication; return non-nil to reject

	// AllowEmptyOrigin opts in to accepting upgrade requests that arrive with
	// no Origin header. Browsers always send Origin on WebSocket upgrades, so
	// missing Origin only happens with non-browser clients (curl, custom Go
	// or Python clients). The secure default is to reject such requests; set
	// to true only for trusted non-browser integrations.
	AllowEmptyOrigin bool
}

// Stats holds server statistics
type Stats struct {
	ConnectedClients int64
	MessagesSent     int64
	MessagesReceived int64
	BytesSent        int64
	BytesReceived    int64
}

// MessageHandler processes incoming messages
type MessageHandler func(client *Client, message Message) error

// Middleware wraps message handlers
type Middleware func(next MessageHandler) MessageHandler

// AuthFunc handles client authentication
type AuthFunc func(client *Client, data interface{}) error

// DisconnectFunc handles client disconnection
type DisconnectFunc func(client *Client)
