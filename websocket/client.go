package websocket

import (
	"log"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// readPump pumps messages from the websocket connection to the server
func (c *Client) readPump() {
	defer func() {
		c.Server.unregister <- c
		c.Conn.Close()
	}()

	c.Conn.SetReadLimit(c.Server.config.MaxMessageSize)
	c.Conn.SetReadDeadline(time.Now().Add(c.Server.config.PongTimeout))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(c.Server.config.PongTimeout))
		return nil
	})

	for {
		var msg Message
		err := c.Conn.ReadJSON(&msg)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseAbnormalClosure,
				websocket.CloseNormalClosure) {
				log.Printf("websocket error: %v", err)
			}
			break
		}

		// Update stats
		atomic.AddInt64(&c.Server.stats.MessagesReceived, 1)

		// Set the sender
		msg.From = c.ID

		// Handle the message
		c.handleMessage(msg)
	}
}

// writePump pumps messages from the server to the websocket connection
func (c *Client) writePump() {
	ticker := time.NewTicker(c.Server.config.PingInterval)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(c.Server.config.WriteTimeout))
			if !ok {
				// The server closed the channel
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			// Send the message
			if err := c.Conn.WriteJSON(message); err != nil {
				return
			}

			// Update stats
			atomic.AddInt64(&c.Server.stats.MessagesSent, 1)

			// Send any queued messages
			n := len(c.Send)
			for i := 0; i < n; i++ {
				if err := c.Conn.WriteJSON(<-c.Send); err != nil {
					return
				}
				atomic.AddInt64(&c.Server.stats.MessagesSent, 1)
			}

		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(c.Server.config.WriteTimeout))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// handleMessage processes an incoming message
func (c *Client) handleMessage(msg Message) {
	// Check for handler
	c.Server.mu.RLock()
	handler, ok := c.Server.handlers[msg.Type]
	c.Server.mu.RUnlock()

	if !ok {
		// No handler registered — use generic error to avoid reflecting user input
		c.Send <- Message{
			Type: "error",
			Data: map[string]interface{}{
				"message": "unknown message type",
			},
		}
		return
	}

	// Execute handler
	if err := handler(c, msg); err != nil {
		// Handle error
		if c.Server.onError != nil {
			c.Server.onError(c, err)
		} else {
			// Send generic error — avoid leaking internal error details to clients
			c.Send <- Message{
				Type: "error",
				Data: map[string]interface{}{
					"message": "internal error",
				},
			}
		}
	}
}

// SendMessage sends a message to the client
func (c *Client) SendMessage(msg Message) error {
	select {
	case c.Send <- msg:
		return nil
	default:
		return ErrSendChannelFull
	}
}

// SendJSON sends a JSON message to the client
func (c *Client) SendJSON(messageType string, data interface{}) error {
	return c.SendMessage(Message{
		Type: messageType,
		Data: data,
	})
}

// GetMetadata returns metadata value
func (c *Client) GetMetadata(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	val, ok := c.Metadata[key]
	return val, ok
}

// SetMetadata sets metadata value
func (c *Client) SetMetadata(key string, value interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Metadata[key] = value
}

// IsInGroup checks if client is in a group
func (c *Client) IsInGroup(group string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Groups[group]
}

// Close closes the client connection
func (c *Client) Close() {
	c.Conn.Close()
}
