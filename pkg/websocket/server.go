package websocket

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// sanitizeForLog strips control characters and newlines from a string for safe logging.
func sanitizeForLog(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= 32 && r != 127 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Server manages WebSocket connections
type Server struct {
	config     Config
	upgrader   *websocket.Upgrader
	clients    map[string]*Client
	groups     map[string]map[string]*Client
	broadcast  chan Message
	register   chan *Client
	unregister chan *Client
	handlers   map[string]MessageHandler
	middleware []Middleware

	// Callbacks
	onConnect    func(*Client)
	onDisconnect DisconnectFunc
	onError      func(*Client, error)

	// Stats
	stats Stats

	mu       sync.RWMutex
	running  bool
	stopChan chan struct{}
}

// New creates a new WebSocket server
func New(config Config) *Server {
	// Set defaults
	if config.MaxConnections == 0 {
		config.MaxConnections = 10000
	}
	if config.ReadBufferSize == 0 {
		config.ReadBufferSize = 1024
	}
	if config.WriteBufferSize == 0 {
		config.WriteBufferSize = 1024
	}
	if config.MaxMessageSize == 0 {
		config.MaxMessageSize = 512 * 1024 // 512KB
	}
	if config.PingInterval == 0 {
		config.PingInterval = 30 * time.Second
	}
	if config.PongTimeout == 0 {
		config.PongTimeout = 60 * time.Second
	}
	if config.WriteTimeout == 0 {
		config.WriteTimeout = 10 * time.Second
	}

	s := &Server{
		config:     config,
		clients:    make(map[string]*Client),
		groups:     make(map[string]map[string]*Client),
		broadcast:  make(chan Message, 256),
		register:   make(chan *Client, 256),
		unregister: make(chan *Client, 256),
		handlers:   make(map[string]MessageHandler),
		stopChan:   make(chan struct{}),
	}

	s.upgrader = &websocket.Upgrader{
		ReadBufferSize:  config.ReadBufferSize,
		WriteBufferSize: config.WriteBufferSize,
		CheckOrigin:     s.checkOrigin,
	}

	return s
}

// Start begins processing WebSocket connections
func (s *Server) Start() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("server already running")
	}
	s.running = true
	s.mu.Unlock()

	log.Printf("WebSocket server starting on %s:%d%s", s.config.Host, s.config.Port, s.config.Path)

	go s.run()
	return nil
}

// Stop gracefully shuts down the server
func (s *Server) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.mu.Unlock()

	close(s.stopChan)

	// Close all client connections
	s.mu.RLock()
	for _, client := range s.clients {
		client.Conn.Close()
	}
	s.mu.RUnlock()
}

// run is the main event loop
func (s *Server) run() {
	for {
		select {
		case client := <-s.register:
			s.handleRegister(client)

		case client := <-s.unregister:
			s.handleUnregister(client)

		case message := <-s.broadcast:
			s.handleBroadcast(message)

		case <-s.stopChan:
			return
		}
	}
}

// HandleConnection upgrades HTTP connection to WebSocket
func (s *Server) HandleConnection(w http.ResponseWriter, r *http.Request) {
	// Check if server is running
	s.mu.RLock()
	if !s.running {
		s.mu.RUnlock()
		http.Error(w, "Server not running", http.StatusServiceUnavailable)
		return
	}

	// Check connection limit
	if len(s.clients) >= s.config.MaxConnections {
		s.mu.RUnlock()
		http.Error(w, "Connection limit reached", http.StatusServiceUnavailable)
		return
	}
	s.mu.RUnlock()

	// Upgrade connection
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade connection: %v", err)
		return
	}

	// Create client
	client := &Client{
		ID:       generateID(),
		Conn:     conn,
		Send:     make(chan Message, 256),
		Server:   s,
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}

	// Register client
	s.register <- client

	// Start client goroutines
	go client.writePump()
	go client.readPump()

	// Call connect callback
	if s.onConnect != nil {
		s.onConnect(client)
	}

	// Update stats
	atomic.AddInt64(&s.stats.ConnectedClients, 1)
}

// handleRegister adds a new client
func (s *Server) handleRegister(client *Client) {
	s.mu.Lock()
	s.clients[client.ID] = client
	s.mu.Unlock()

	log.Printf("Client connected: %s", client.ID)

	// Send welcome message
	client.Send <- Message{
		Type: "welcome",
		Data: map[string]interface{}{
			"id":      client.ID,
			"version": "1.0.0",
		},
	}
}

// handleUnregister removes a client
func (s *Server) handleUnregister(client *Client) {
	s.mu.Lock()
	if _, ok := s.clients[client.ID]; ok {
		delete(s.clients, client.ID)

		// Remove from all groups
		for groupName := range client.Groups {
			if group, ok := s.groups[groupName]; ok {
				delete(group, client.ID)
				if len(group) == 0 {
					delete(s.groups, groupName)
				}
			}
		}

		close(client.Send)
	}
	s.mu.Unlock()

	log.Printf("Client disconnected: %s", client.ID)

	// Call disconnect callback
	if s.onDisconnect != nil {
		s.onDisconnect(client)
	}

	// Update stats
	atomic.AddInt64(&s.stats.ConnectedClients, -1)
}

// handleBroadcast sends message to all clients
func (s *Server) handleBroadcast(message Message) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, client := range s.clients {
		select {
		case client.Send <- message:
		default:
			// Client's send channel is full, skip
			log.Printf("Client %s send channel full, skipping message", client.ID)
		}
	}

	atomic.AddInt64(&s.stats.MessagesSent, int64(len(s.clients)))
}

// checkOrigin validates the origin of the connection.
// If no AllowedOrigins are configured, only same-origin requests are accepted.
// Use AllowedOrigins: []string{"*"} to explicitly allow all origins.
func (s *Server) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")

	// If no allowed origins specified, only allow same-origin
	if len(s.config.AllowedOrigins) == 0 {
		if origin == "" {
			return true
		}
		// Compare origin to the Host header (same-origin check)
		host := r.Host
		return origin == "http://"+host || origin == "https://"+host
	}

	// Check if origin is in allowed list
	for _, allowed := range s.config.AllowedOrigins {
		if allowed == "*" {
			return true
		}
		if allowed == origin {
			return true
		}
	}

	return false
}

// On registers a message handler
func (s *Server) On(messageType string, handler MessageHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Apply middleware
	finalHandler := handler
	for i := len(s.middleware) - 1; i >= 0; i-- {
		finalHandler = s.middleware[i](finalHandler)
	}

	s.handlers[messageType] = finalHandler
}

// Use adds middleware
func (s *Server) Use(middleware Middleware) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.middleware = append(s.middleware, middleware)
}

// OnConnect sets the connect callback
func (s *Server) OnConnect(fn func(*Client)) {
	s.onConnect = fn
}

// OnDisconnect sets the disconnect callback
func (s *Server) OnDisconnect(fn DisconnectFunc) {
	s.onDisconnect = fn
}

// OnError sets the error callback
func (s *Server) OnError(fn func(*Client, error)) {
	s.onError = fn
}

// Broadcast sends a message to all connected clients
func (s *Server) Broadcast(message Message) {
	s.broadcast <- message
}

// SendToClient sends a message to a specific client
func (s *Server) SendToClient(clientID string, message Message) error {
	s.mu.RLock()
	client, ok := s.clients[clientID]
	s.mu.RUnlock()

	if !ok {
		return fmt.Errorf("client %s not found", clientID)
	}

	select {
	case client.Send <- message:
		atomic.AddInt64(&s.stats.MessagesSent, 1)
	default:
		return fmt.Errorf("client %s send channel full", clientID)
	}

	return nil
}

// GetStats returns server statistics
func (s *Server) GetStats() Stats {
	return Stats{
		ConnectedClients: atomic.LoadInt64(&s.stats.ConnectedClients),
		MessagesSent:     atomic.LoadInt64(&s.stats.MessagesSent),
		MessagesReceived: atomic.LoadInt64(&s.stats.MessagesReceived),
		BytesSent:        atomic.LoadInt64(&s.stats.BytesSent),
		BytesReceived:    atomic.LoadInt64(&s.stats.BytesReceived),
	}
}

// GetClient returns a client by ID
func (s *Server) GetClient(id string) (*Client, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	client, ok := s.clients[id]
	return client, ok
}

// HandleRaw upgrades the HTTP connection to a WebSocket and returns the raw
// connection. Unlike HandleConnection, it does NOT register a managed Client,
// does NOT start readPump/writePump goroutines, and does NOT enter the
// message routing system. The caller owns the connection and is responsible
// for reading, writing, and closing it.
func (s *Server) HandleRaw(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, fmt.Errorf("websocket upgrade: %w", err)
	}
	return conn, nil
}

// GetClients returns all connected clients
func (s *Server) GetClients() map[string]*Client {
	s.mu.RLock()
	defer s.mu.RUnlock()

	clients := make(map[string]*Client)
	for id, client := range s.clients {
		clients[id] = client
	}
	return clients
}
