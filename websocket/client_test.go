package websocket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	testsync "github.com/velocitykode/velocity/testing"
)

func TestClientConnection(t *testing.T) {
	config := DefaultConfig()
	server := New(config)
	server.Start()
	defer server.Stop()

	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(server.HandleConnection))
	defer ts.Close()

	wsURL := strings.Replace(ts.URL, "http", "ws", 1)
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer ws.Close()

	// Read welcome message
	var msg Message
	err = ws.ReadJSON(&msg)
	if err != nil {
		t.Fatalf("Failed to read welcome message: %v", err)
	}

	if msg.Type != "welcome" {
		t.Errorf("Expected welcome message, got %s", msg.Type)
	}

	// Check client is registered
	stats := server.GetStats()
	if stats.ConnectedClients != 1 {
		t.Errorf("Expected 1 connected client, got %d", stats.ConnectedClients)
	}
}

func TestClientDisconnection(t *testing.T) {
	config := DefaultConfig()
	server := New(config)
	server.Start()
	defer server.Stop()

	ts := httptest.NewServer(http.HandlerFunc(server.HandleConnection))
	defer ts.Close()

	wsURL := strings.Replace(ts.URL, "http", "ws", 1)
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	// Close connection
	ws.Close()

	testsync.Eventually(t, func() bool {
		return server.GetStats().ConnectedClients == 0
	}, 2*time.Second, "server observes client disconnect")
}

func TestClientSendJSON(t *testing.T) {
	config := DefaultConfig()
	server := New(config)

	// Add echo handler
	server.On("echo", func(client *Client, msg Message) error {
		return client.SendJSON("echo_response", msg.Data)
	})

	server.Start()
	defer server.Stop()

	ts := httptest.NewServer(http.HandlerFunc(server.HandleConnection))
	defer ts.Close()

	wsURL := strings.Replace(ts.URL, "http", "ws", 1)
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer ws.Close()

	// Skip welcome
	var welcome Message
	ws.ReadJSON(&welcome)

	// Send echo request
	echoMsg := Message{
		Type: "echo",
		Data: map[string]interface{}{
			"test": "data",
		},
	}
	err = ws.WriteJSON(echoMsg)
	if err != nil {
		t.Fatalf("Failed to send echo: %v", err)
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
}

func TestConcurrentClientMessages(t *testing.T) {
	config := DefaultConfig()
	server := New(config)

	var messageCount atomic.Int32
	server.On("test", func(client *Client, msg Message) error {
		messageCount.Add(1)
		return nil
	})

	server.Start()
	defer server.Stop()

	ts := httptest.NewServer(http.HandlerFunc(server.HandleConnection))
	defer ts.Close()

	wsURL := strings.Replace(ts.URL, "http", "ws", 1)
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer ws.Close()

	// Skip welcome
	var welcome Message
	ws.ReadJSON(&welcome)

	// Send messages sequentially (gorilla/websocket forbids concurrent writes
	// on one conn). "Concurrent" here refers to the server dispatching each
	// message through its handler goroutine while the test reads back.
	numMessages := 10
	for i := 0; i < numMessages; i++ {
		msg := Message{
			Type: "test",
			Data: i,
		}
		if err := ws.WriteJSON(msg); err != nil {
			t.Fatalf("Failed to send message %d: %v", i, err)
		}
	}

	testsync.EventuallyEqual(t, messageCount.Load, int32(numMessages), 2*time.Second, "all messages processed")
}

func TestClientIDGeneration(t *testing.T) {
	config := DefaultConfig()
	server := New(config)
	server.Start()
	defer server.Stop()

	ts := httptest.NewServer(http.HandlerFunc(server.HandleConnection))
	defer ts.Close()

	wsURL := strings.Replace(ts.URL, "http", "ws", 1)

	// Connect two clients and get their IDs
	ws1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect client 1: %v", err)
	}
	defer ws1.Close()

	ws2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect client 2: %v", err)
	}
	defer ws2.Close()

	// Read welcome messages
	var welcome1, welcome2 Message
	ws1.ReadJSON(&welcome1)
	ws2.ReadJSON(&welcome2)

	data1 := welcome1.Data.(map[string]interface{})
	data2 := welcome2.Data.(map[string]interface{})
	id1 := data1["id"].(string)
	id2 := data2["id"].(string)

	// IDs should be unique
	if id1 == id2 {
		t.Error("Client IDs should be unique")
	}

	// IDs should be 32 characters (MD5 hex)
	if len(id1) != 32 || len(id2) != 32 {
		t.Errorf("Client IDs should be 32 characters, got %d and %d", len(id1), len(id2))
	}
}
