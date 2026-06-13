package websocket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	testsync "github.com/velocitykode/velocity/testing"
)

// TestHandleMessageNonBlockingWhenSendFull is the B10 regression: handleMessage
// runs on readPump's goroutine. If writePump has died with the Send channel
// full, the old bare `c.Send <- ...` error replies blocked forever, wedging
// readPump (close(c.Send) only fires after readPump returns) and leaking the
// connection. Both the unknown-type reply and the handler-error reply must now
// return promptly when the channel is full and unattended.
func TestHandleMessageNonBlockingWhenSendFull(t *testing.T) {
	cases := []struct {
		name  string
		setup func(s *Server)
		msg   Message
	}{
		{
			name:  "unknown type",
			setup: func(s *Server) {},
			msg:   Message{Type: "no-such-handler"},
		},
		{
			name: "handler error",
			setup: func(s *Server) {
				s.handlers = map[string]MessageHandler{
					"boom": func(*Client, Message) error {
						return context.DeadlineExceeded
					},
				}
			},
			msg: Message{Type: "boom"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{handlers: map[string]MessageHandler{}}
			tc.setup(s)

			c := &Client{
				ID:     "c1",
				Send:   make(chan Message, 1),
				Server: s,
			}
			// Saturate Send so any further send would block. writePump is NOT
			// running, so nothing will ever drain it.
			c.Send <- Message{Type: "filler"}

			done := make(chan struct{})
			go func() {
				c.handleMessage(tc.msg)
				close(done)
			}()

			select {
			case <-done:
				// Returned promptly: non-blocking path worked.
			case <-time.After(2 * time.Second):
				t.Fatal("handleMessage blocked on a full Send channel (B10 regression)")
			}
		})
	}
}

// TestBroadcastMessagesSentCountedOnce verifies a single broadcast to N clients
// increments MessagesSent by exactly N, not 2N. Before the fix every delivered
// message was counted twice (once at enqueue, once at the writePump wire
// write); the enqueue-side counters were removed so writePump is the single
// counting site. Counting is now asynchronous, so the assertion polls.
func TestBroadcastMessagesSentCountedOnce(t *testing.T) {
	const n = 5

	server := New(DefaultConfig())
	server.Start()
	defer server.Shutdown(context.Background())

	ts := httptest.NewServer(http.HandlerFunc(server.HandleConnection))
	defer ts.Close()
	wsURL := strings.Replace(ts.URL, "http", "ws", 1)

	conns := make([]*websocket.Conn, 0, n)
	for i := 0; i < n; i++ {
		ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
		if err != nil {
			t.Fatalf("Failed to connect client %d: %v", i, err)
		}
		defer ws.Close()
		// Drain the welcome message so it is written to the wire and counted.
		var welcome Message
		if err := ws.ReadJSON(&welcome); err != nil {
			t.Fatalf("Failed to read welcome on client %d: %v", i, err)
		}
		conns = append(conns, ws)
	}

	// Wait for all welcomes to be written and counted, then take a baseline.
	testsync.EventuallyEqual(t, func() int64 {
		return server.GetStats().MessagesSent
	}, int64(n), 2*time.Second, "welcome messages counted once each")
	baseline := server.GetStats().MessagesSent

	// One broadcast to all N clients.
	server.Broadcast(Message{Type: "ping"})

	// Read the broadcast on each client so writePump actually writes it.
	for i, ws := range conns {
		var got Message
		if err := ws.ReadJSON(&got); err != nil {
			t.Fatalf("Failed to read broadcast on client %d: %v", i, err)
		}
		if got.Type != "ping" {
			t.Errorf("client %d: expected ping, got %s", i, got.Type)
		}
	}

	// Exactly N more, not 2N.
	testsync.EventuallyEqual(t, func() int64 {
		return server.GetStats().MessagesSent
	}, baseline+int64(n), 2*time.Second, "broadcast counted once per client")
}
