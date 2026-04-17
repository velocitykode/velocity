package drivers

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/websocket"
)

// ChannelAuthorizer checks if a WebSocket client is allowed to join a channel.
// Must be set for private- and presence- channels to be accessible.
type ChannelAuthorizer func(client *websocket.Client, channel string) bool

// denyAllChannelAuthorizer is the secure default: deny every subscription to
// a private- or presence- channel. Applications must explicitly install an
// authorizer via SetAuthorizer.
func denyAllChannelAuthorizer(client *websocket.Client, channel string) bool {
	return false
}

// WebSocketDriver adapts the existing WebSocket server for broadcasting
type WebSocketDriver struct {
	server         *websocket.Server
	channels       map[string]map[string]*websocket.Client // channel -> socketID -> client
	authorizer     ChannelAuthorizer
	mu             sync.RWMutex
	droppedCount   atomic.Uint64
	blockingSendTO time.Duration // 0 means non-blocking (drop on full)
	onDrop         func(clientID, channel, event string)
}

// DriverOption configures a WebSocketDriver.
type DriverOption func(*WebSocketDriver)

// WithBlockingSend returns an option that makes Broadcast and BroadcastExcept
// block for up to the given duration when a client's send buffer is full,
// rather than dropping immediately. A zero or negative duration disables
// blocking and restores the drop-on-full default.
func WithBlockingSend(timeout time.Duration) DriverOption {
	return func(d *WebSocketDriver) {
		d.blockingSendTO = timeout
	}
}

// WithOnDrop installs a callback invoked whenever a message is dropped because
// a client's Send buffer was full. Intended for metric/event dispatching; the
// callback must not block the send path.
func WithOnDrop(fn func(clientID, channel, event string)) DriverOption {
	return func(d *WebSocketDriver) {
		d.onDrop = fn
	}
}

// NewWebSocketDriver creates a new WebSocket driver.
// The default authorizer denies all requests to private- and presence-
// channels. Callers must install an authorizer via SetAuthorizer to grant
// access.
func NewWebSocketDriver(config websocket.Config, opts ...DriverOption) *WebSocketDriver {
	driver := &WebSocketDriver{
		channels:   make(map[string]map[string]*websocket.Client),
		authorizer: denyAllChannelAuthorizer,
	}

	for _, opt := range opts {
		opt(driver)
	}

	// Create WebSocket server
	server := websocket.New(config)
	driver.server = server

	// Register channel handlers
	server.On("subscribe", driver.handleSubscribe)
	server.On("unsubscribe", driver.handleUnsubscribe)
	server.On("client-event", driver.handleClientEvent)

	// Start the server
	server.Start()

	return driver
}

// Broadcast sends an event to channels. If a client's Send buffer is full,
// the message is either dropped (default) or the call blocks for up to
// blockingSendTO (configured via WithBlockingSend). Dropped messages are
// counted and the onDrop callback (if any) is invoked.
func (d *WebSocketDriver) Broadcast(channels []string, event string, data interface{}) error {
	d.mu.RLock()
	defer d.mu.RUnlock()

	for _, channel := range channels {
		if clients, exists := d.channels[channel]; exists {
			for _, client := range clients {
				d.sendOrDrop(client, channel, event, data)
			}
		}
	}

	return nil
}

// BroadcastExcept broadcasts to all except specified socket
func (d *WebSocketDriver) BroadcastExcept(channels []string, event string, data interface{}, socketID string) error {
	d.mu.RLock()
	defer d.mu.RUnlock()

	for _, channel := range channels {
		if clients, exists := d.channels[channel]; exists {
			for id, client := range clients {
				if id != socketID {
					d.sendOrDrop(client, channel, event, data)
				}
			}
		}
	}

	return nil
}

// sendOrDrop attempts to deliver a message to client's Send channel. When a
// blocking-send timeout is configured it waits up to that duration; otherwise
// it drops immediately on full buffer. Drops increment droppedCount and
// trigger the onDrop callback (if set).
func (d *WebSocketDriver) sendOrDrop(client *websocket.Client, channel, event string, data interface{}) {
	msg := websocket.Message{Type: event, Data: data}

	if d.blockingSendTO <= 0 {
		select {
		case client.Send <- msg:
			return
		default:
			d.recordDrop(client.ID, channel, event)
			return
		}
	}

	// Blocking path with timeout — uses a timer rather than time.After so the
	// underlying resources are released promptly when the send succeeds.
	t := time.NewTimer(d.blockingSendTO)
	defer t.Stop()

	select {
	case client.Send <- msg:
		return
	case <-t.C:
		d.recordDrop(client.ID, channel, event)
	}
}

func (d *WebSocketDriver) recordDrop(clientID, channel, event string) {
	d.droppedCount.Add(1)
	if d.onDrop != nil {
		d.onDrop(clientID, channel, event)
		return
	}
	fmt.Printf("velocity/broadcast: dropped message to client %s on channel %s (event=%s)\n", clientID, channel, event)
}

// DroppedCount returns the total number of messages dropped due to full send
// buffers across the lifetime of the driver. It is safe to call concurrently.
func (d *WebSocketDriver) DroppedCount() uint64 {
	return d.droppedCount.Load()
}

// GetClients returns clients in a channel
func (d *WebSocketDriver) GetClients(channel string) []string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var clientIDs []string
	if clients, exists := d.channels[channel]; exists {
		for id := range clients {
			clientIDs = append(clientIDs, id)
		}
	}

	return clientIDs
}

// Subscribe adds a client to a channel
func (d *WebSocketDriver) Subscribe(channel string, client *websocket.Client) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.channels[channel] == nil {
		d.channels[channel] = make(map[string]*websocket.Client)
	}

	d.channels[channel][client.ID] = client
	return nil
}

// Unsubscribe removes a client from a channel
func (d *WebSocketDriver) Unsubscribe(channel string, clientID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if clients, exists := d.channels[channel]; exists {
		delete(clients, clientID)

		// Clean up empty channels
		if len(clients) == 0 {
			delete(d.channels, channel)
		}
	}

	return nil
}

// handleSubscribe handles channel subscription requests
func (d *WebSocketDriver) handleSubscribe(client *websocket.Client, msg websocket.Message) error {
	data, ok := msg.Data.(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid subscribe data")
	}

	channel, ok := data["channel"].(string)
	if !ok {
		return fmt.Errorf("channel not specified")
	}

	// Authorize private and presence channels. The default authorizer is
	// deny-all, so a missing setup fails closed rather than silently allowing.
	if strings.HasPrefix(channel, "private-") || strings.HasPrefix(channel, "presence-") {
		d.mu.RLock()
		auth := d.authorizer
		d.mu.RUnlock()
		if auth == nil {
			auth = denyAllChannelAuthorizer
		}
		if !auth(client, channel) {
			return fmt.Errorf("velocity/broadcast: unauthorized to subscribe to channel %s", channel)
		}
	}

	// Add client to channel
	if err := d.Subscribe(channel, client); err != nil {
		return err
	}

	// Send subscription confirmation
	return client.SendJSON("subscription_succeeded", map[string]interface{}{
		"channel": channel,
	})
}

// handleUnsubscribe handles channel unsubscription requests
func (d *WebSocketDriver) handleUnsubscribe(client *websocket.Client, msg websocket.Message) error {
	data, ok := msg.Data.(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid unsubscribe data")
	}

	channel, ok := data["channel"].(string)
	if !ok {
		return fmt.Errorf("channel not specified")
	}

	// Remove client from channel
	if err := d.Unsubscribe(channel, client.ID); err != nil {
		return err
	}

	// Send unsubscription confirmation
	return client.SendJSON("unsubscription_succeeded", map[string]interface{}{
		"channel": channel,
	})
}

// handleClientEvent handles client-to-client events
func (d *WebSocketDriver) handleClientEvent(client *websocket.Client, msg websocket.Message) error {
	data, ok := msg.Data.(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid client event data")
	}

	channel, ok := data["channel"].(string)
	if !ok {
		return fmt.Errorf("channel not specified")
	}

	event, ok := data["event"].(string)
	if !ok {
		return fmt.Errorf("event not specified")
	}

	// Broadcast to channel except sender
	return d.BroadcastExcept([]string{channel}, "client-"+event, data["data"], client.ID)
}

// SetAuthorizer sets the channel authorizer for private/presence channels.
// Without an authorizer, subscriptions to private- and presence- channels are rejected.
func (d *WebSocketDriver) SetAuthorizer(fn ChannelAuthorizer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.authorizer = fn
}

// GetServer returns the underlying WebSocket server
func (d *WebSocketDriver) GetServer() *websocket.Server {
	return d.server
}
