package websocket

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// A Client the server did not connect is not connected: every send
// reports ErrClientNotFound, and closing it is a no-op.
func TestClient_NotConnectedByTheServer(t *testing.T) {
	c := &Client{ID: "hand-built"}
	if err := c.SendMessage(Message{Type: "x"}); !errors.Is(err, ErrClientNotFound) {
		t.Errorf("SendMessage = %v, want ErrClientNotFound", err)
	}
	hostile.Within(t, hostile.Deadline, func() {
		if err := c.SendMessageCtx(context.Background(), Message{Type: "x"}); !errors.Is(err, ErrClientNotFound) {
			t.Errorf("SendMessageCtx = %v, want ErrClientNotFound", err)
		}
	})
	if p := hostile.Within(t, hostile.Deadline, c.closeSend); p != nil {
		t.Fatalf("closeSend panicked: %v", p)
	}
	if p := hostile.Within(t, hostile.Deadline, c.Close); p != nil {
		t.Fatalf("Close panicked: %v", p)
	}
}

// SendMessageCtx waits for room in a full queue: it enqueues once the peer
// drains, returns ctx's error when ctx ends first, and never reports
// ctx's error for a message it enqueued.
func TestClient_SendMessageCtxWaitsForRoom(t *testing.T) {
	h := newPipeHost(t)
	t.Run("drained", func(t *testing.T) {
		c, peer := h.full(t)
		res := parked(t, c, context.Background())
		go func() {
			for {
				if _, _, err := peer.NextReader(); err != nil {
					return
				}
			}
		}()
		hostile.Within(t, hostile.Deadline, func() {
			if err := <-res; err != nil {
				t.Errorf("SendMessageCtx after the peer drained = %v, want nil", err)
			}
		})
	})
	t.Run("ctx ends", func(t *testing.T) {
		c, _ := h.full(t)
		ctx, cancel := context.WithCancel(context.Background())
		res := parked(t, c, ctx)
		cancel()
		hostile.Within(t, hostile.Deadline, func() {
			if err := <-res; !errors.Is(err, context.Canceled) {
				t.Errorf("SendMessageCtx at its ctx = %v, want context.Canceled", err)
			}
		})
	})
}

// A failed write ends the client's writePump, and with it the client's
// send state: a later SendMessage reports the client gone, and a
// SendMessageCtx parked on the full queue wakes with the same error.
func TestClient_WriteErrorEndsSendState(t *testing.T) {
	h := newPipeHost(t)
	c, peer := h.full(t)
	res := parked(t, c, context.Background())
	_ = peer.NetConn().Close() // the blocked write fails
	hostile.Within(t, hostile.Deadline, func() {
		if err := <-res; !errors.Is(err, ErrClientNotFound) {
			t.Errorf("parked SendMessageCtx = %v, want ErrClientNotFound", err)
		}
	})
	if err := c.SendMessage(Message{Type: "late"}); !errors.Is(err, ErrClientNotFound) {
		t.Errorf("SendMessage after the write error = %v, want ErrClientNotFound", err)
	}
}

// Shutdown ends every connected client's send state: once it returns, a
// client the run loop never unregistered takes no more sends.
func TestServer_ShutdownEndsClientSendState(t *testing.T) {
	h := newPipeHost(t)
	c, _ := h.connect(t)
	ctx, cancel := context.WithTimeout(context.Background(), hostile.Deadline)
	defer cancel()
	if err := h.s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := c.SendMessage(Message{Type: "late"}); !errors.Is(err, ErrClientNotFound) {
		t.Errorf("SendMessage after Shutdown = %v, want ErrClientNotFound", err)
	}
}

// closeSend wakes the bounded sends parked on a full queue, waits for them
// to leave and only then closes the queue: each reports the client gone,
// and none meets a closed queue. Run under -race.
func TestClient_CloseSendReleasesParkedSenders(t *testing.T) {
	for range 50 {
		c := &Client{ID: "c", send: make(chan Message, 1)}
		c.send <- Message{Type: "filler"}
		const senders = 8
		res := make(chan error, senders)
		for range senders {
			go func() { res <- c.SendMessageCtx(context.Background(), Message{Type: "x"}) }()
		}
		hostile.Eventually(t, hostile.Deadline, "a send waiting", func() bool {
			c.mu.RLock()
			defer c.mu.RUnlock()
			return c.done != nil
		})
		hostile.Within(t, hostile.Deadline, c.closeSend)
		hostile.Within(t, hostile.Deadline, func() {
			for range senders {
				if err := <-res; !errors.Is(err, ErrClientNotFound) {
					t.Errorf("parked send = %v, want ErrClientNotFound", err)
				}
			}
		})
	}
}

// Closes that race each other and the client's sends (non-blocking and
// bounded) end the send state once: every send either enqueues or reports
// the client gone, none meets a closed queue, and every closer returns.
// Run under -race.
func TestClient_ConcurrentCloseAndSends(t *testing.T) {
	for range 100 {
		c := &Client{ID: "c", send: make(chan Message, 1)}
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 4 {
			wg.Add(3)
			go func() {
				defer wg.Done()
				<-start
				c.closeSend()
			}()
			go func() {
				defer wg.Done()
				<-start
				if err := c.SendMessage(Message{Type: "x"}); err != nil && !errors.Is(err, ErrClientNotFound) && !errors.Is(err, ErrSendChannelFull) {
					t.Errorf("SendMessage = %v", err)
				}
			}()
			go func() {
				defer wg.Done()
				<-start
				if err := c.SendMessageCtx(context.Background(), Message{Type: "x"}); err != nil && !errors.Is(err, ErrClientNotFound) {
					t.Errorf("SendMessageCtx = %v", err)
				}
			}()
		}
		close(start)
		hostile.Within(t, hostile.Deadline, wg.Wait)
	}
}
