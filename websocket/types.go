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

// Client represents a WebSocket connection. The server makes a Client for
// each connection it accepts. A Client the server did not connect is not
// connected: every send returns ErrClientNotFound and closing it is a
// no-op.
type Client struct {
	ID       string
	Conn     *websocket.Conn
	Server   *Server
	Groups   map[string]bool
	Metadata map[string]interface{}
	mu       sync.RWMutex

	// send is the client's outbound queue, drained by writePump; nil on a
	// Client the server did not connect. Every enqueue goes through the
	// Client's send methods, and only closeSend closes it, so a send never
	// meets a closed queue.
	send chan Message
	// closed is set, under mu, by the closeSend that ends the queue; a send
	// checks it under mu before it enqueues.
	closed bool
	// done is closed with closed set, so a bounded send parked on a full
	// queue leaves when the client closes. Made lazily, under mu, by the
	// first bounded send.
	done chan struct{}
	// senders counts the bounded sends between their closed check and
	// their return; closeSend waits for them before it closes send.
	senders sync.WaitGroup
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
	//
	// This applies uniformly: an empty Origin is governed solely by
	// AllowEmptyOrigin regardless of whether AllowedOrigins is set. In
	// particular AllowedOrigins []string{"*"} no longer accepts a missing
	// Origin header on its own - it also requires AllowEmptyOrigin=true.
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

// DisconnectFunc handles client disconnection
type DisconnectFunc func(client *Client)
