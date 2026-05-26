package websocket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	testsync "github.com/velocitykode/velocity/testing"
)

func TestNewServer(t *testing.T) {
	config := DefaultConfig()
	s := New(config)

	if s == nil {
		t.Fatal("Expected server to be created")
	}

	if s.config.Host != "0.0.0.0" {
		t.Errorf("Expected host to be 0.0.0.0, got %s", s.config.Host)
	}

	if s.config.Port != 6001 {
		t.Errorf("Expected port to be 6001, got %d", s.config.Port)
	}

	if s.config.Path != "/ws" {
		t.Errorf("Expected path to be /ws, got %s", s.config.Path)
	}
}

func TestServerStartStop(t *testing.T) {
	s := New(DefaultConfig())

	err := s.Start()
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}

	if !s.running {
		t.Error("Expected server to be running")
	}

	// Try starting again (should fail)
	err = s.Start()
	if err == nil {
		t.Error("Expected error when starting already running server")
	}

	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	if s.running {
		t.Error("Expected server to be stopped")
	}
}

func TestHandleConnection(t *testing.T) {
	s := New(DefaultConfig())
	s.Start()
	defer s.Shutdown(context.Background())

	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	// Connect to WebSocket
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Failed to connect to WebSocket: %v", err)
	}
	defer ws.Close()

	// Wait for welcome message
	var msg Message
	err = ws.ReadJSON(&msg)
	if err != nil {
		t.Fatalf("Failed to read welcome message: %v", err)
	}

	if msg.Type != "welcome" {
		t.Errorf("Expected welcome message, got %s", msg.Type)
	}

	// Check stats
	stats := s.GetStats()
	if stats.ConnectedClients != 1 {
		t.Errorf("Expected 1 connected client, got %d", stats.ConnectedClients)
	}
}

func TestMessageHandler(t *testing.T) {
	s := New(DefaultConfig())
	s.Start()
	defer s.Shutdown(context.Background())

	// Register echo handler
	s.On("echo", func(client *Client, msg Message) error {
		return client.SendMessage(Message{
			Type: "echo_response",
			Data: msg.Data,
		})
	})

	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	// Connect
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer ws.Close()

	// Skip welcome message
	var welcome Message
	ws.ReadJSON(&welcome)

	// Send echo message
	err = ws.WriteJSON(Message{
		Type: "echo",
		Data: "hello",
	})
	if err != nil {
		t.Fatalf("Failed to send message: %v", err)
	}

	// Read response
	var response Message
	err = ws.ReadJSON(&response)
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	if response.Type != "echo_response" {
		t.Errorf("Expected echo_response, got %s", response.Type)
	}

	if response.Data != "hello" {
		t.Errorf("Expected data to be hello, got %v", response.Data)
	}
}

func TestGroups(t *testing.T) {
	s := New(DefaultConfig())
	s.Start()
	defer s.Shutdown(context.Background())

	// Create test clients
	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Connect client 1
	ws1, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Failed to connect client 1: %v", err)
	}
	defer ws1.Close()

	// Connect client 2
	ws2, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Failed to connect client 2: %v", err)
	}
	defer ws2.Close()

	// Poll until both clients are registered on the server side
	testsync.Eventually(t, func() bool { return s.GetStats().ConnectedClients == 2 }, 2*time.Second, "both clients registered")

	// Get client IDs from welcome messages
	var welcome1, welcome2 Message
	ws1.ReadJSON(&welcome1)
	ws2.ReadJSON(&welcome2)

	client1ID := welcome1.Data.(map[string]interface{})["id"].(string)
	client2ID := welcome2.Data.(map[string]interface{})["id"].(string)

	// Join both clients to a group
	err = s.JoinGroup(client1ID, "test-group")
	if err != nil {
		t.Fatalf("Failed to join client 1 to group: %v", err)
	}

	err = s.JoinGroup(client2ID, "test-group")
	if err != nil {
		t.Fatalf("Failed to join client 2 to group: %v", err)
	}

	// Check group members
	members := s.GetGroupMemberIDs("test-group")
	if len(members) != 2 {
		t.Errorf("Expected 2 members in group, got %d", len(members))
	}

	// Broadcast to group
	err = s.BroadcastToGroup("test-group", Message{
		Type: "group_message",
		Data: "hello group",
	})
	if err != nil {
		t.Fatalf("Failed to broadcast to group: %v", err)
	}

	// Both clients should receive the message
	var msg1, msg2 Message
	err = ws1.ReadJSON(&msg1)
	if err != nil {
		t.Fatalf("Client 1 failed to receive group message: %v", err)
	}

	err = ws2.ReadJSON(&msg2)
	if err != nil {
		t.Fatalf("Client 2 failed to receive group message: %v", err)
	}

	if msg1.Type != "group_message" || msg2.Type != "group_message" {
		t.Error("Expected both clients to receive group_message")
	}

	// Leave group
	err = s.LeaveGroup(client1ID, "test-group")
	if err != nil {
		t.Fatalf("Failed to leave group: %v", err)
	}

	members = s.GetGroupMemberIDs("test-group")
	if len(members) != 1 {
		t.Errorf("Expected 1 member in group after leave, got %d", len(members))
	}
}

func TestBroadcast(t *testing.T) {
	s := New(DefaultConfig())
	s.Start()
	defer s.Shutdown(context.Background())

	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Connect multiple clients
	clients := make([]*websocket.Conn, 3)
	for i := range clients {
		ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
		if err != nil {
			t.Fatalf("Failed to connect client %d: %v", i, err)
		}
		defer ws.Close()
		clients[i] = ws

		// Skip welcome message
		var welcome Message
		ws.ReadJSON(&welcome)
	}

	// Poll until all 3 clients are registered
	testsync.Eventually(t, func() bool { return s.GetStats().ConnectedClients == 3 }, 2*time.Second, "all clients registered")

	// Broadcast message
	s.Broadcast(Message{
		Type: "broadcast",
		Data: "hello everyone",
	})

	// All clients should receive the message
	for i, ws := range clients {
		var msg Message
		err := ws.ReadJSON(&msg)
		if err != nil {
			t.Fatalf("Client %d failed to receive broadcast: %v", i, err)
		}

		if msg.Type != "broadcast" {
			t.Errorf("Client %d expected broadcast message, got %s", i, msg.Type)
		}

		if msg.Data != "hello everyone" {
			t.Errorf("Client %d expected data 'hello everyone', got %v", i, msg.Data)
		}
	}
}

func TestHandleRaw(t *testing.T) {
	s := New(DefaultConfig())

	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := s.HandleRaw(w, r)
		if err != nil {
			t.Errorf("HandleRaw failed: %v", err)
			return
		}
		defer conn.Close()

		// Echo raw messages back
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	}))
	defer ts.Close()

	// Connect to WebSocket
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Failed to connect to WebSocket: %v", err)
	}
	defer ws.Close()

	// Send a text message
	err = ws.WriteMessage(websocket.TextMessage, []byte("hello raw"))
	if err != nil {
		t.Fatalf("Failed to send message: %v", err)
	}

	// Read echoed response
	mt, msg, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read message: %v", err)
	}

	if mt != websocket.TextMessage {
		t.Errorf("Expected text message type, got %d", mt)
	}
	if string(msg) != "hello raw" {
		t.Errorf("Expected 'hello raw', got '%s'", string(msg))
	}

	// Send a binary message
	binaryData := []byte{0x00, 0x01, 0x02, 0x03}
	err = ws.WriteMessage(websocket.BinaryMessage, binaryData)
	if err != nil {
		t.Fatalf("Failed to send binary message: %v", err)
	}

	mt, msg, err = ws.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read binary message: %v", err)
	}

	if mt != websocket.BinaryMessage {
		t.Errorf("Expected binary message type, got %d", mt)
	}
	if string(msg) != string(binaryData) {
		t.Errorf("Binary data mismatch")
	}
}

func TestHandleRaw_DoesNotRegisterClient(t *testing.T) {
	s := New(DefaultConfig())
	s.Start()
	defer s.Shutdown(context.Background())

	// Create test server using HandleRaw
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := s.HandleRaw(w, r)
		if err != nil {
			t.Errorf("HandleRaw failed: %v", err)
			return
		}
		defer conn.Close()

		// Keep connection open briefly
		time.Sleep(200 * time.Millisecond)
	}))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer ws.Close()

	// Raw connections bypass client management entirely — HandleRaw never
	// adds to s.clients or bumps ConnectedClients — so this assertion
	// holds regardless of timing. No sleep needed.
	stats := s.GetStats()
	if stats.ConnectedClients != 0 {
		t.Errorf("Expected 0 connected clients for raw connection, got %d", stats.ConnectedClients)
	}

	clients := s.GetClients()
	if len(clients) != 0 {
		t.Errorf("Expected 0 managed clients for raw connection, got %d", len(clients))
	}
}

func TestHandleRaw_RespectsOriginCheck(t *testing.T) {
	config := DefaultConfig()
	config.AllowedOrigins = []string{"https://allowed.example.com"}
	s := New(config)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := s.HandleRaw(w, r)
		if err != nil {
			return
		}
		conn.Close()
	}))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Connection with disallowed origin should fail
	header := http.Header{}
	header.Set("Origin", "https://evil.example.com")
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		t.Fatal("Expected connection with disallowed origin to fail")
	}
	if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Errorf("Expected status 403, got %d", resp.StatusCode)
	}
}

func TestConnectionLimit(t *testing.T) {
	config := DefaultConfig()
	config.MaxConnections = 2
	s := New(config)
	s.Start()
	defer s.Shutdown(context.Background())

	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Connect first client (should succeed)
	ws1, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Failed to connect client 1: %v", err)
	}
	defer ws1.Close()

	// Connect second client (should succeed)
	ws2, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Failed to connect client 2: %v", err)
	}
	defer ws2.Close()

	// Poll until both clients are registered so the limit check is meaningful
	testsync.Eventually(t, func() bool { return s.GetStats().ConnectedClients == 2 }, 2*time.Second, "two clients registered before testing limit")

	// Connect third client (should fail due to limit)
	ws3, resp, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err == nil {
		ws3.Close()
		t.Fatal("Expected connection to fail due to limit")
	}

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", resp.StatusCode)
	}
}

// TestServer_ShutdownWaitsForRunGoroutine verifies that Shutdown drains the
// run-loop goroutine before returning and leaves no goroutine behind.
func TestServer_ShutdownWaitsForRunGoroutine(t *testing.T) {
	s := New(DefaultConfig())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Baseline after Start so runtime/test infra goroutines are already
	// accounted for; only the run loop should still be outstanding.
	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	// After Shutdown the run goroutine must be gone. Poll briefly because
	// the scheduler may not have parked the returning goroutine yet.
	testsync.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= baseline-1
	}, 2*time.Second, "run goroutine exited")

	// Second call is a no-op and must return nil.
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown returned error: %v", err)
	}
}

// TestServer_ShutdownWaitsForClientPumps verifies that Shutdown waits for
// every per-client read/write pump to exit before returning.
func TestServer_ShutdownWaitsForClientPumps(t *testing.T) {
	s := New(DefaultConfig())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Two long-lived clients, each with a read/write pump on the server.
	ws1, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	defer ws1.Close()
	ws2, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer ws2.Close()

	testsync.Eventually(t, func() bool { return s.GetStats().ConnectedClients == 2 },
		2*time.Second, "both clients registered")

	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	// run + 2 read + 2 write = 5 tracked goroutines must have exited.
	testsync.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= baseline-5
	}, 2*time.Second, "all tracked goroutines exited")
}

// TestServer_ShutdownRespectsCtxDeadline verifies that Shutdown honours the
// caller's deadline when a tracked goroutine refuses to exit. We register a
// synthetic goroutine on the server WaitGroup that blocks until the test
// releases it, then assert Shutdown returns context.DeadlineExceeded rather
// than hanging.
func TestServer_ShutdownRespectsCtxDeadline(t *testing.T) {
	s := New(DefaultConfig())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Simulate an uncooperative tracked goroutine. Because server_test.go
	// lives in package websocket, we can register directly on s.wg.
	release := make(chan struct{})
	var released sync.WaitGroup
	released.Add(1)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer released.Done()
		<-release
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := s.Shutdown(ctx)
	elapsed := time.Since(start)

	if err != context.DeadlineExceeded {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Shutdown hung for %s; should have returned near the 50ms deadline", elapsed)
	}

	// Release the stuck goroutine so the test exits cleanly.
	close(release)
	released.Wait()
}

// TestCheckOrigin_SameOriginDefault exercises the same-origin gate that runs
// whenever AllowedOrigins is empty. Three properties matter:
//
//  1. An empty Origin header is rejected by default (browsers always send
//     Origin on WS upgrades, so a missing one signals a non-browser caller
//     and must not be silently trusted).
//  2. A case-mismatched Origin host is accepted (Host headers and Origin
//     values often differ in case for IDN/punycode and ALL-CAPS environments).
//  3. A mismatched host is rejected.
//
// The opt-in AllowEmptyOrigin flag flips behaviour 1 back to accept.
func TestCheckOrigin_SameOriginDefault(t *testing.T) {
	tests := []struct {
		name             string
		origin           string
		host             string
		allowEmptyOrigin bool
		want             bool
	}{
		{
			name:   "missing Origin header is rejected by default",
			origin: "",
			host:   "example.com",
			want:   false,
		},
		{
			name:             "missing Origin header is accepted when opted in",
			origin:           "",
			host:             "example.com",
			allowEmptyOrigin: true,
			want:             true,
		},
		{
			name:   "case-mismatched Origin host accepted",
			origin: "https://Example.COM",
			host:   "example.com",
			want:   true,
		},
		{
			name:   "matching origin accepted",
			origin: "https://example.com",
			host:   "example.com",
			want:   true,
		},
		{
			name:   "mismatched host rejected",
			origin: "https://attacker.example.net",
			host:   "example.com",
			want:   false,
		},
		{
			name:   "malformed Origin rejected",
			origin: "::::not-a-url",
			host:   "example.com",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := DefaultConfig()
			config.AllowedOrigins = nil // same-origin only
			config.AllowEmptyOrigin = tt.allowEmptyOrigin
			s := New(config)

			req := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/ws", nil)
			req.Host = tt.host
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}

			got := s.checkOrigin(req)
			if got != tt.want {
				t.Errorf("checkOrigin(origin=%q host=%q allowEmpty=%v) = %v, want %v", tt.origin, tt.host, tt.allowEmptyOrigin, got, tt.want)
			}
		})
	}
}
