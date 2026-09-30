package websocket

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/velocitykode/velocity/internal/errchain"
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
		// writePump is the queue's only reader: once it exits, whatever
		// ended it (server shutdown, a failed write), the client takes no
		// more sends.
		c.closeSend()
	}()
	defer func() {
		if r := recover(); r != nil {
			c.Server.logError("websocket writePump panic recovered", "error", panicerr.FromRecovered(r))
		}
	}()

	for {
		select {
		case message, ok := <-c.send:
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
			n := len(c.send)
			for i := 0; i < n; i++ {
				if err := c.Conn.WriteJSON(<-c.send); err != nil {
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
		// No handler registered: use generic error to avoid reflecting user input.
		// Non-blocking send: handleMessage runs on readPump's goroutine, and
		// a reply to a full queue is dropped rather than holding the read
		// loop. A writePump that has exited ended the client's send state,
		// so the reply reports ErrClientNotFound instead of waiting.
		if err := c.SendMessage(Message{
			Type: "error",
			Data: map[string]interface{}{
				"message": "unknown message type",
			},
		}); err != nil {
			c.Server.logWarn("websocket dropping unknown-type error reply", "client_id", c.ID, "error", err)
		}
		return
	}

	// Execute handler
	if err := handler(c, msg); err != nil {
		// Handle error
		if p := c.Server.onError.Load(); p != nil {
			(*p)(c, err)
		} else {
			// Send generic error, avoid leaking internal error details to clients.
			// Non-blocking for the same readPump-wedge reason as the unknown-type
			// reply above.
			if err := c.SendMessage(Message{
				Type: "error",
				Data: map[string]interface{}{
					"message": "internal error",
				},
			}); err != nil {
				c.Server.logWarn("websocket dropping handler-error reply", "client_id", c.ID, "error", err)
			}
		}
	}
}

// trySend attempts a non-blocking enqueue onto the send queue. It holds mu,
// which closeSend also holds to mark the client closed, so it never
// enqueues after the queue closed. The hold is exclusive, not shared:
// shared holds let many concurrent senders hammer the queue at once and
// starve its one reader. It reports whether the message was
// queued and, when not, whether the client was closed (vs its queue merely
// full) so the caller can log the right reason.
func (c *Client) trySend(msg Message) (queued, closed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.send == nil {
		return false, true
	}
	select {
	case c.send <- msg:
		return true, false
	default:
		return false, false
	}
}

// closeSend ends the client's send state: it marks the client closed and
// wakes every bounded send parked on the queue, then, with mu released,
// waits for those sends to leave and closes the queue, which ends
// writePump. Idempotent: a later call is a no-op, and so is a call on a
// Client the server did not connect. Only the first call waits for the
// parked sends; a concurrent later call returns at once, so a return is
// not a sign that the queue is closed yet.
func (c *Client) closeSend() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	if c.done != nil {
		close(c.done)
	}
	c.mu.Unlock()
	c.senders.Wait()
	if c.send != nil {
		close(c.send)
	}
}

// errClientClosed is the error a send to a closed client returns.
func (c *Client) errClientClosed() error {
	return errchain.Errorf("client %s disconnected: %w", sanitizeForLog(c.ID), ErrClientNotFound)
}

// SendMessage enqueues msg for the client without blocking. A client that
// has disconnected reports ErrClientNotFound, and a full queue
// ErrSendChannelFull.
func (c *Client) SendMessage(msg Message) error {
	queued, closed := c.trySend(msg)
	switch {
	case closed:
		return c.errClientClosed()
	case !queued:
		return ErrSendChannelFull
	}
	return nil
}

// SendMessageCtx enqueues msg for the client, waiting for room in its queue
// until ctx is done. A nil error means msg was enqueued. A client that has
// disconnected, or disconnects while the call waits, reports
// ErrClientNotFound; when ctx ends first it returns ctx.Err(). The wait
// gives none of the three priority: a call whose ctx is already done may
// still enqueue when the queue has room, and then returns nil, and a call
// already waiting when the client closes may still enqueue if the queue
// has room at that moment, and then returns nil (writePump or the close
// discards the message).
func (c *Client) SendMessageCtx(ctx context.Context, msg Message) error {
	c.mu.Lock()
	if c.closed || c.send == nil {
		c.mu.Unlock()
		return c.errClientClosed()
	}
	if c.done == nil {
		c.done = make(chan struct{})
	}
	done := c.done
	c.senders.Add(1)
	c.mu.Unlock()
	defer c.senders.Done()
	select {
	case c.send <- msg:
		return nil
	case <-done:
		return c.errClientClosed()
	case <-ctx.Done():
		return ctx.Err()
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

// Close closes the client connection. On a Client the server did not
// connect it is a no-op.
func (c *Client) Close() {
	if c.Conn == nil {
		return
	}
	c.Conn.Close()
}
