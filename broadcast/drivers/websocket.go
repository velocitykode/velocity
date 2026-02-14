package drivers

import (
	"fmt"
	"sync"

	"github.com/velocitykode/velocity/websocket"
)

// WebSocketDriver adapts the existing WebSocket server for broadcasting
type WebSocketDriver struct {
	server   *websocket.Server
	channels map[string]map[string]*websocket.Client // channel -> socketID -> client
	mu       sync.RWMutex
}

// NewWebSocketDriver creates a new WebSocket driver
func NewWebSocketDriver(config websocket.Config) *WebSocketDriver {
	driver := &WebSocketDriver{
		channels: make(map[string]map[string]*websocket.Client),
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

// Broadcast sends an event to channels
func (d *WebSocketDriver) Broadcast(channels []string, event string, data interface{}) error {
	d.mu.RLock()
	defer d.mu.RUnlock()

	for _, channel := range channels {
		if clients, exists := d.channels[channel]; exists {
			for _, client := range clients {
				// Send message through client's channel
				select {
				case client.Send <- websocket.Message{
					Type: event,
					Data: data,
				}:
					// Message sent
				default:
					// Channel full, skip this client
					fmt.Printf("Failed to send to client %s: channel full\n", client.ID)
				}
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
					// Send message through client's channel
					select {
					case client.Send <- websocket.Message{
						Type: event,
						Data: data,
					}:
						// Message sent
					default:
						// Channel full, skip this client
						fmt.Printf("Failed to send to client %s: channel full\n", client.ID)
					}
				}
			}
		}
	}

	return nil
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

// GetServer returns the underlying WebSocket server
func (d *WebSocketDriver) GetServer() *websocket.Server {
	return d.server
}
