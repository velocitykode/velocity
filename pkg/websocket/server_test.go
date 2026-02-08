package websocket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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

	s.Stop()

	if s.running {
		t.Error("Expected server to be stopped")
	}
}

func TestHandleConnection(t *testing.T) {
	s := New(DefaultConfig())
	s.Start()
	defer s.Stop()

	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	// Connect to WebSocket
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
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
	defer s.Stop()

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
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
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
	defer s.Stop()

	// Create test clients
	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Connect client 1
	ws1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect client 1: %v", err)
	}
	defer ws1.Close()

	// Connect client 2
	ws2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect client 2: %v", err)
	}
	defer ws2.Close()

	// Wait for both clients to be registered
	time.Sleep(100 * time.Millisecond)

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
	defer s.Stop()

	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Connect multiple clients
	clients := make([]*websocket.Conn, 3)
	for i := range clients {
		ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("Failed to connect client %d: %v", i, err)
		}
		defer ws.Close()
		clients[i] = ws

		// Skip welcome message
		var welcome Message
		ws.ReadJSON(&welcome)
	}

	// Wait for all clients to be registered
	time.Sleep(100 * time.Millisecond)

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
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
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
	defer s.Stop()

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
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer ws.Close()

	// Wait for connection to be established
	time.Sleep(50 * time.Millisecond)

	// Raw connections should NOT appear in managed clients or stats
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
	defer s.Stop()

	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Connect first client (should succeed)
	ws1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect client 1: %v", err)
	}
	defer ws1.Close()

	// Connect second client (should succeed)
	ws2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect client 2: %v", err)
	}
	defer ws2.Close()

	// Wait for registrations
	time.Sleep(100 * time.Millisecond)

	// Connect third client (should fail due to limit)
	ws3, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		ws3.Close()
		t.Fatal("Expected connection to fail due to limit")
	}

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", resp.StatusCode)
	}
}
