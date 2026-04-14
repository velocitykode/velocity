package broadcast

import (
	"testing"
	"time"

	"github.com/velocitykode/velocity/websocket"
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

// Tests for utils.go helper functions

func TestIsPrivateChannel(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		want    bool
	}{
		{"returns true for private channel", "private-user.123", true},
		{"returns true for private channel with dots", "private-user.room.123", true},
		{"returns false for public channel", "news", false},
		{"returns false for presence channel", "presence-room.1", false},
		{"returns false for empty string", "", false},
		{"returns false for channel starting with private without dash", "privateuser", false},
		{"returns true for private- prefix only", "private-", true},
		{"returns false for channel containing private in middle", "my-private-channel", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPrivateChannel(tt.channel)
			if got != tt.want {
				t.Errorf("isPrivateChannel(%q) = %v, want %v", tt.channel, got, tt.want)
			}
		})
	}
}

func TestIsPresenceChannel(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		want    bool
	}{
		{"returns true for presence channel", "presence-room.1", true},
		{"returns true for presence channel with dots", "presence-user.room.123", true},
		{"returns false for public channel", "news", false},
		{"returns false for private channel", "private-user.123", false},
		{"returns false for empty string", "", false},
		{"returns false for channel starting with presence without dash", "presenceroom", false},
		{"returns true for presence- prefix only", "presence-", true},
		{"returns false for channel containing presence in middle", "my-presence-channel", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPresenceChannel(tt.channel)
			if got != tt.want {
				t.Errorf("isPresenceChannel(%q) = %v, want %v", tt.channel, got, tt.want)
			}
		})
	}
}

func TestIsPublicChannel(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		want    bool
	}{
		{"returns true for public channel", "news", true},
		{"returns true for public channel with dots", "news.updates.123", true},
		{"returns false for private channel", "private-user.123", false},
		{"returns false for presence channel", "presence-room.1", false},
		{"returns true for empty string", "", true},
		{"returns true for channel starting with private without dash", "privateuser", true},
		{"returns true for channel starting with presence without dash", "presenceroom", true},
		{"returns true for channel containing private in middle", "my-private-channel", true},
		{"returns true for channel containing presence in middle", "my-presence-channel", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPublicChannel(tt.channel)
			if got != tt.want {
				t.Errorf("isPublicChannel(%q) = %v, want %v", tt.channel, got, tt.want)
			}
		})
	}
}

func TestParseChannelName(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		want    string
	}{
		{"strips private- prefix", "private-user.123", "user.123"},
		{"strips presence- prefix", "presence-room.1", "room.1"},
		{"returns public channel unchanged", "news", "news"},
		{"returns empty string unchanged", "", ""},
		{"handles private- prefix only", "private-", ""},
		{"handles presence- prefix only", "presence-", ""},
		{"does not strip partial private prefix", "privateuser", "privateuser"},
		{"does not strip partial presence prefix", "presenceroom", "presenceroom"},
		{"preserves dots in channel name after stripping private-", "private-user.room.123", "user.room.123"},
		{"preserves dots in channel name after stripping presence-", "presence-user.room.123", "user.room.123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseChannelName(tt.channel)
			if got != tt.want {
				t.Errorf("parseChannelName(%q) = %q, want %q", tt.channel, got, tt.want)
			}
		})
	}
}

// Additional edge case tests for broadcaster

func TestNew(t *testing.T) {
	tests := []struct {
		name   string
		driver Driver
	}{
		{"creates broadcaster with mock driver", NewMockDriver()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := New(tt.driver)
			if b == nil {
				t.Fatal("New() returned nil, want non-nil broadcaster")
			}
			if b.driver != tt.driver {
				t.Error("New() did not set driver correctly")
			}
		})
	}
}

func TestChannelBuilder_EmptyChannels(t *testing.T) {
	tests := []struct {
		name     string
		channels []string
		wantLen  int
	}{
		{"handles empty channel list", []string{}, 0},
		{"handles single channel", []string{"news"}, 1},
		{"handles multiple channels", []string{"a", "b", "c"}, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewMockDriver()
			b := New(driver)

			err := b.Channel(tt.channels...).Emit("event", "data")
			if err != nil {
				t.Errorf("Emit() returned error: %v", err)
			}

			if len(driver.broadcasts) != 1 {
				t.Errorf("expected 1 broadcast call, got %d", len(driver.broadcasts))
				return
			}

			if len(driver.broadcasts[0].Channels) != tt.wantLen {
				t.Errorf("expected %d channels, got %d", tt.wantLen, len(driver.broadcasts[0].Channels))
			}
		})
	}
}

func TestAuth_NoAuthorizer(t *testing.T) {
	tests := []struct {
		name     string
		channel  string
		socketID string
		user     interface{}
		wantErr  bool
	}{
		{"returns nil when no authorizer set", "private-user.123", "socket-1", map[string]interface{}{"id": "123"}, false},
		{"returns nil for presence channel without authorizer", "presence-room.1", "socket-1", nil, false},
		{"returns nil for public channel without authorizer", "news", "socket-1", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewMockDriver()
			b := New(driver)

			result, err := b.Auth(tt.channel, tt.socketID, tt.user)
			if (err != nil) != tt.wantErr {
				t.Errorf("Auth() error = %v, wantErr %v", err, tt.wantErr)
			}
			if result != nil {
				t.Errorf("Auth() result = %v, want nil when no authorizer", result)
			}
		})
	}
}

func TestAuth_PresenceChannelWithoutPresenceFunc(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		user    interface{}
	}{
		{"returns status map for presence channel without presence func", "presence-room.1", map[string]interface{}{"id": "123"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewMockDriver()
			b := New(driver)

			// Set authorizer but not presence data func
			b.SetAuthorizer(func(channel string, user interface{}) bool {
				return true
			})

			result, err := b.Auth(tt.channel, "socket-1", tt.user)
			if err != nil {
				t.Errorf("Auth() error = %v, want nil", err)
			}

			// Should return status map since no presence func is set
			if data, ok := result.(map[string]interface{}); ok {
				if data["status"] != "authorized" {
					t.Errorf("Auth() status = %v, want authorized", data["status"])
				}
			} else {
				t.Error("Auth() should return map with status when presence func not set")
			}
		})
	}
}

func TestLeave(t *testing.T) {
	tests := []struct {
		name     string
		channel  string
		socketID string
		wantErr  bool
	}{
		{"returns nil for leave operation", "presence-room.1", "socket-1", false},
		{"handles empty channel", "", "socket-1", false},
		{"handles empty socket ID", "presence-room.1", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewMockDriver()
			b := New(driver)

			err := b.Leave(tt.channel, tt.socketID)
			if (err != nil) != tt.wantErr {
				t.Errorf("Leave() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestChannelBuilderChaining(t *testing.T) {
	tests := []struct {
		name          string
		setupBuilder  func(*BroadcastManager) *ChannelBuilder
		wantChannels  []string
		wantToOthers  string
		wantCondition bool
	}{
		{
			name: "chains ToOthers and When",
			setupBuilder: func(b *BroadcastManager) *ChannelBuilder {
				return b.Channel("chat").ToOthers("socket-1").When(true)
			},
			wantChannels:  []string{"chat"},
			wantToOthers:  "socket-1",
			wantCondition: true,
		},
		{
			name: "chains When before ToOthers",
			setupBuilder: func(b *BroadcastManager) *ChannelBuilder {
				return b.Channel("chat").When(false).ToOthers("socket-2")
			},
			wantChannels:  []string{"chat"},
			wantToOthers:  "socket-2",
			wantCondition: false,
		},
		{
			name: "private channel with chaining",
			setupBuilder: func(b *BroadcastManager) *ChannelBuilder {
				return b.Private("user.1").ToOthers("socket-3").When(true)
			},
			wantChannels:  []string{"private-user.1"},
			wantToOthers:  "socket-3",
			wantCondition: true,
		},
		{
			name: "presence channel with chaining",
			setupBuilder: func(b *BroadcastManager) *ChannelBuilder {
				return b.Presence("room.1").ToOthers("socket-4").When(true)
			},
			wantChannels:  []string{"presence-room.1"},
			wantToOthers:  "socket-4",
			wantCondition: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewMockDriver()
			b := New(driver)

			cb := tt.setupBuilder(b)

			if len(cb.channels) != len(tt.wantChannels) {
				t.Errorf("channels length = %d, want %d", len(cb.channels), len(tt.wantChannels))
			}

			for i, ch := range cb.channels {
				if ch != tt.wantChannels[i] {
					t.Errorf("channel[%d] = %q, want %q", i, ch, tt.wantChannels[i])
				}
			}

			if cb.toOthers != tt.wantToOthers {
				t.Errorf("toOthers = %q, want %q", cb.toOthers, tt.wantToOthers)
			}

			if cb.condition != tt.wantCondition {
				t.Errorf("condition = %v, want %v", cb.condition, tt.wantCondition)
			}
		})
	}
}

func TestSetAuthorizerAndPresenceData(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"sets authorizer successfully"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewMockDriver()
			b := New(driver)

			// Test SetAuthorizer
			authCalled := false
			b.SetAuthorizer(func(channel string, user interface{}) bool {
				authCalled = true
				return true
			})

			_, _ = b.Auth("test", "socket", nil)
			if !authCalled {
				t.Error("authorizer was not called after SetAuthorizer")
			}

			// Test SetPresenceData
			presenceCalled := false
			b.SetPresenceData(func(channel string, user interface{}) interface{} {
				presenceCalled = true
				return nil
			})

			_, _ = b.Auth("presence-room", "socket", nil)
			if !presenceCalled {
				t.Error("presence func was not called after SetPresenceData")
			}
		})
	}
}
