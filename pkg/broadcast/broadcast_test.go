package broadcast

import (
	"testing"
	"time"

	"github.com/velocitykode/velocity/pkg/websocket"
)

// MockDriver is a test driver for broadcasting
type MockDriver struct {
	broadcasts []BroadcastCall
	clients    map[string][]string
}

type BroadcastCall struct {
	Channels []string
	Event    string
	Data     interface{}
	Exclude  string
}

func NewMockDriver() *MockDriver {
	return &MockDriver{
		broadcasts: []BroadcastCall{},
		clients:    make(map[string][]string),
	}
}

func (m *MockDriver) Broadcast(channels []string, event string, data interface{}) error {
	m.broadcasts = append(m.broadcasts, BroadcastCall{
		Channels: channels,
		Event:    event,
		Data:     data,
	})
	return nil
}

func (m *MockDriver) BroadcastExcept(channels []string, event string, data interface{}, socketID string) error {
	m.broadcasts = append(m.broadcasts, BroadcastCall{
		Channels: channels,
		Event:    event,
		Data:     data,
		Exclude:  socketID,
	})
	return nil
}

func (m *MockDriver) GetClients(channel string) []string {
	return m.clients[channel]
}

func TestBroadcaster(t *testing.T) {
	driver := NewMockDriver()
	b := New(driver)

	// Test basic channel broadcast
	err := b.Channel("news").Emit("article", map[string]string{"title": "Test"})
	if err != nil {
		t.Fatalf("Failed to emit: %v", err)
	}

	if len(driver.broadcasts) != 1 {
		t.Errorf("Expected 1 broadcast, got %d", len(driver.broadcasts))
	}

	call := driver.broadcasts[0]
	if call.Event != "article" {
		t.Errorf("Expected event 'article', got %s", call.Event)
	}
	if len(call.Channels) != 1 || call.Channels[0] != "news" {
		t.Errorf("Expected channel 'news', got %v", call.Channels)
	}
}

func TestPrivateChannel(t *testing.T) {
	driver := NewMockDriver()
	b := New(driver)

	// Test private channel broadcast
	err := b.Private("user.123").Emit("update", map[string]string{"status": "online"})
	if err != nil {
		t.Fatalf("Failed to emit: %v", err)
	}

	call := driver.broadcasts[0]
	if call.Channels[0] != "private-user.123" {
		t.Errorf("Expected channel 'private-user.123', got %s", call.Channels[0])
	}
}

func TestPresenceChannel(t *testing.T) {
	driver := NewMockDriver()
	b := New(driver)

	// Test presence channel broadcast
	err := b.Presence("room.1").Emit("message", map[string]string{"text": "Hello"})
	if err != nil {
		t.Fatalf("Failed to emit: %v", err)
	}

	call := driver.broadcasts[0]
	if call.Channels[0] != "presence-room.1" {
		t.Errorf("Expected channel 'presence-room.1', got %s", call.Channels[0])
	}
}

func TestMultipleChannels(t *testing.T) {
	driver := NewMockDriver()
	b := New(driver)

	// Test broadcasting to multiple channels
	err := b.Channel("news", "updates", "alerts").Emit("notification", "Test message")
	if err != nil {
		t.Fatalf("Failed to emit: %v", err)
	}

	call := driver.broadcasts[0]
	if len(call.Channels) != 3 {
		t.Errorf("Expected 3 channels, got %d", len(call.Channels))
	}
}

func TestToOthers(t *testing.T) {
	driver := NewMockDriver()
	b := New(driver)

	// Test excluding sender
	err := b.Channel("chat").ToOthers("socket-123").Emit("typing", "User is typing")
	if err != nil {
		t.Fatalf("Failed to emit: %v", err)
	}

	call := driver.broadcasts[0]
	if call.Exclude != "socket-123" {
		t.Errorf("Expected exclude 'socket-123', got %s", call.Exclude)
	}
}

func TestWhenCondition(t *testing.T) {
	driver := NewMockDriver()
	b := New(driver)

	// Test conditional broadcast (false condition)
	err := b.Channel("test").When(false).Emit("event", "data")
	if err != nil {
		t.Fatalf("Failed to emit: %v", err)
	}

	if len(driver.broadcasts) != 0 {
		t.Error("Expected no broadcasts with false condition")
	}

	// Test conditional broadcast (true condition)
	err = b.Channel("test").When(true).Emit("event", "data")
	if err != nil {
		t.Fatalf("Failed to emit: %v", err)
	}

	if len(driver.broadcasts) != 1 {
		t.Error("Expected 1 broadcast with true condition")
	}
}

func TestAuthorization(t *testing.T) {
	driver := NewMockDriver()
	b := New(driver)

	// Set up authorizer
	b.SetAuthorizer(func(channel string, user interface{}) bool {
		// Only allow user 123 to access private-user.123
		if channel == "private-user.123" {
			if u, ok := user.(map[string]interface{}); ok {
				return u["id"] == "123"
			}
		}
		return false
	})

	// Test authorized access
	user := map[string]interface{}{"id": "123"}
	result, err := b.Auth("private-user.123", "socket-1", user)
	if err != nil {
		t.Errorf("Expected successful auth, got error: %v", err)
	}
	if result == nil {
		t.Error("Expected auth result, got nil")
	}

	// Test unauthorized access
	user2 := map[string]interface{}{"id": "456"}
	_, err = b.Auth("private-user.123", "socket-2", user2)
	if err != ErrUnauthorized {
		t.Errorf("Expected ErrUnauthorized, got %v", err)
	}
}

func TestPresenceData(t *testing.T) {
	driver := NewMockDriver()
	b := New(driver)

	// Set up presence data function
	b.SetPresenceData(func(channel string, user interface{}) interface{} {
		if u, ok := user.(map[string]interface{}); ok {
			return map[string]interface{}{
				"id":   u["id"],
				"name": u["name"],
			}
		}
		return nil
	})

	// Set up authorizer for presence channel
	b.SetAuthorizer(func(channel string, user interface{}) bool {
		return isPresenceChannel(channel)
	})

	// Test presence channel auth
	user := map[string]interface{}{"id": "123", "name": "John"}
	result, err := b.Auth("presence-room.1", "socket-1", user)
	if err != nil {
		t.Errorf("Expected successful auth, got error: %v", err)
	}

	if data, ok := result.(map[string]interface{}); ok {
		if data["id"] != "123" || data["name"] != "John" {
			t.Errorf("Expected presence data with id and name, got %v", data)
		}
	} else {
		t.Error("Expected presence data to be a map")
	}
}

func TestDefaultInstance(t *testing.T) {
	// Test that default instance is created
	b1 := Default()
	b2 := Default()

	if b1 != b2 {
		t.Error("Expected same default instance")
	}
}

func TestPackageLevelFunctions(t *testing.T) {
	// Replace default with mock for testing
	mockDriver := NewMockDriver()
	defaultBroadcaster = New(mockDriver)

	// Test package-level Channel function
	err := Channel("test").Emit("event", "data")
	if err != nil {
		t.Fatalf("Failed to emit: %v", err)
	}

	if len(mockDriver.broadcasts) != 1 {
		t.Error("Expected 1 broadcast from package-level function")
	}

	// Test package-level Private function
	mockDriver.broadcasts = []BroadcastCall{}
	err = Private("user.1").Emit("update", "data")
	if err != nil {
		t.Fatalf("Failed to emit: %v", err)
	}

	if mockDriver.broadcasts[0].Channels[0] != "private-user.1" {
		t.Errorf("Expected private channel, got %s", mockDriver.broadcasts[0].Channels[0])
	}

	// Test package-level Presence function
	mockDriver.broadcasts = []BroadcastCall{}
	err = Presence("room.1").Emit("join", "data")
	if err != nil {
		t.Fatalf("Failed to emit: %v", err)
	}

	if mockDriver.broadcasts[0].Channels[0] != "presence-room.1" {
		t.Errorf("Expected presence channel, got %s", mockDriver.broadcasts[0].Channels[0])
	}
}

func TestWebSocketDriverIntegration(t *testing.T) {
	// This test would require a running WebSocket server
	// Skipping for unit tests, but included for completeness
	t.Skip("Integration test requires WebSocket server")

	config := websocket.DefaultConfig()
	config.Port = 6002 // Use different port for testing

	// Would need to import drivers package for real test
	// driver := drivers.NewWebSocketDriver(config)
	// b := New(driver)

	// Test would continue with actual WebSocket connections
	// err := b.Channel("test").Emit("message", "Hello World")
	// if err != nil {
	// 	t.Fatalf("Failed to broadcast: %v", err)
	// }

	// Give some time for message to be processed
	time.Sleep(100 * time.Millisecond)
}
