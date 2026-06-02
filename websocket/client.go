package websocket

import (
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// readPump pumps messages from the websocket connection to the server
func (c *Client) readPump() {
	defer func() {
		// Select on stopChan so that during Shutdown, when the run loop
		// has already exited and is no longer draining s.unregister, this
		// goroutine still exits cleanly instead of blocking forever on a
		// full (cap 256) unregister channel. Without the stopChan branch,
		// every readPump past the buffer cap leaks (audit D-02), pinning
		// the *Client, *websocket.Conn, and all per-client buffers.
		select {
		case c.Server.unregister <- c:
		case <-c.Server.stopChan:
		}
		c.Conn.Close()
	}()
	defer func() {
		if r := recover(); r != nil {
			c.Server.logError("websocket readPump panic recovered", "error", panicerr.FromRecovered(r))
		}
	}()

	c.Conn.SetReadLimit(c.Server.config.MaxMessageSize)
	c.Conn.SetReadDeadline(time.Now().Add(c.Server.config.PongTimeout))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(c.Server.config.PongTimeout))
		return nil
	})

	// Rate limiting: track messages per second to prevent flooding
	rateLimit := c.Server.config.MessageRateLimit
	burstSize := c.Server.config.MessageBurstSize
	if burstSize == 0 && rateLimit > 0 {
		burstSize = rateLimit * 2 // default burst = 2x rate limit
	}
	var msgCount int
	var windowStart time.Time
	if rateLimit > 0 {
		windowStart = time.Now()
	}

	for {
		var msg Message
		err := c.Conn.ReadJSON(&msg)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseAbnormalClosure,
				websocket.CloseNormalClosure) {
				c.Server.logError("websocket error", "error", err)
			}
			break
		}

		// Rate limiting: disconnect client if burst threshold exceeded
		if rateLimit > 0 {
			now := time.Now()
			if now.Sub(windowStart) >= time.Second {
				msgCount = 0
				windowStart = now
			}
			msgCount++
			if msgCount > burstSize {
				c.Server.logWarn("websocket client exceeded rate limit, disconnecting", "client_id", c.ID, "rate_limit", rateLimit, "burst_size", burstSize)
				c.SendMessage(Message{
					Type: "error",
					Data: map[string]interface{}{"message": "rate limit exceeded"},
				})
				break
			}
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
	defer func() {
		if r := recover(); r != nil {
			c.Server.logError("websocket writePump panic recovered", "error", panicerr.FromRecovered(r))
		}
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

		case <-c.Server.stopChan:
			// Server is shutting down — send close frame and exit
			// without waiting for the ping ticker.
			c.Conn.SetWriteDeadline(time.Now().Add(c.Server.config.WriteTimeout))
			c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
			return
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
		if p := c.Server.onError.Load(); p != nil {
			(*p)(c, err)
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
