package drivers

import (
	"sync"
	"testing"

	"github.com/velocitykode/velocity/pkg/websocket"
)

// createTestClient creates a mock client with buffered Send channel for testing
func createTestClient(id string) *websocket.Client {
	return &websocket.Client{
		ID:       id,
		Send:     make(chan websocket.Message, 10),
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}
}

// drainMessages reads all messages from client's Send channel
func drainMessages(client *websocket.Client) []websocket.Message {
	var messages []websocket.Message
	for {
		select {
		case msg := <-client.Send:
			messages = append(messages, msg)
		default:
			return messages
		}
	}
}

func TestNewWebSocketDriverInitialization(t *testing.T) {
	tests := []struct {
		name   string
		config websocket.Config
	}{
		{
			name:   "creates driver with default config",
			config: websocket.DefaultConfig(),
		},
		{
			name: "creates driver with custom config",
			config: websocket.Config{
				Host: "localhost",
				Port: 8080,
				Path: "/ws",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewWebSocketDriver(tt.config)

			if driver == nil {
				t.Fatal("expected driver to be created, got nil")
			}

			if driver.channels == nil {
				t.Error("expected channels map to be initialized")
			}

			if driver.server == nil {
				t.Error("expected server to be initialized")
			}

			// Clean up
			driver.server.Stop()
		})
	}
}

func TestWebSocketDriver_Subscribe(t *testing.T) {
	tests := []struct {
		name          string
		setupChannels map[string][]string // channel -> client IDs to pre-add
		subscribeOps  []struct{ channel, clientID string }
		wantChannels  map[string][]string
		wantErr       bool
	}{
		{
			name:          "creates new channel when subscribing first client",
			setupChannels: nil,
			subscribeOps: []struct{ channel, clientID string }{
				{"test-channel", "client-1"},
			},
			wantChannels: map[string][]string{
				"test-channel": {"client-1"},
			},
			wantErr: false,
		},
		{
			name: "adds client to existing channel",
			setupChannels: map[string][]string{
				"test-channel": {"client-1"},
			},
			subscribeOps: []struct{ channel, clientID string }{
				{"test-channel", "client-2"},
			},
			wantChannels: map[string][]string{
				"test-channel": {"client-1", "client-2"},
			},
			wantErr: false,
		},
		{
			name:          "subscribes same client to multiple channels",
			setupChannels: nil,
			subscribeOps: []struct{ channel, clientID string }{
				{"channel-a", "client-1"},
				{"channel-b", "client-1"},
			},
			wantChannels: map[string][]string{
				"channel-a": {"client-1"},
				"channel-b": {"client-1"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := &WebSocketDriver{
				channels: make(map[string]map[string]*websocket.Client),
			}

			// Setup existing channels
			for channel, clientIDs := range tt.setupChannels {
				driver.channels[channel] = make(map[string]*websocket.Client)
				for _, id := range clientIDs {
					driver.channels[channel][id] = createTestClient(id)
				}
			}

			// Execute subscribe operations
			for _, op := range tt.subscribeOps {
				client := createTestClient(op.clientID)
				err := driver.Subscribe(op.channel, client)
				if (err != nil) != tt.wantErr {
					t.Errorf("Subscribe() error = %v, wantErr %v", err, tt.wantErr)
				}
			}

			// Verify channels
			for channel, wantClients := range tt.wantChannels {
				clients, exists := driver.channels[channel]
				if !exists {
					t.Errorf("expected channel %q to exist", channel)
					continue
				}

				if len(clients) != len(wantClients) {
					t.Errorf("channel %q: got %d clients, want %d", channel, len(clients), len(wantClients))
				}

				for _, clientID := range wantClients {
					if _, ok := clients[clientID]; !ok {
						t.Errorf("channel %q: expected client %q to be subscribed", channel, clientID)
					}
				}
			}
		})
	}
}

func TestWebSocketDriver_Unsubscribe(t *testing.T) {
	tests := []struct {
		name           string
		setupChannels  map[string][]string
		unsubscribeOps []struct{ channel, clientID string }
		wantChannels   map[string][]string // nil means channel should not exist
		wantErr        bool
	}{
		{
			name: "removes client from channel",
			setupChannels: map[string][]string{
				"test-channel": {"client-1", "client-2"},
			},
			unsubscribeOps: []struct{ channel, clientID string }{
				{"test-channel", "client-1"},
			},
			wantChannels: map[string][]string{
				"test-channel": {"client-2"},
			},
			wantErr: false,
		},
		{
			name: "cleans up empty channel after last client unsubscribes",
			setupChannels: map[string][]string{
				"test-channel": {"client-1"},
			},
			unsubscribeOps: []struct{ channel, clientID string }{
				{"test-channel", "client-1"},
			},
			wantChannels: map[string][]string{},
			wantErr:      false,
		},
		{
			name:          "handles unsubscribe from non-existent channel",
			setupChannels: nil,
			unsubscribeOps: []struct{ channel, clientID string }{
				{"non-existent", "client-1"},
			},
			wantChannels: map[string][]string{},
			wantErr:      false,
		},
		{
			name: "handles unsubscribe of non-existent client",
			setupChannels: map[string][]string{
				"test-channel": {"client-1"},
			},
			unsubscribeOps: []struct{ channel, clientID string }{
				{"test-channel", "client-999"},
			},
			wantChannels: map[string][]string{
				"test-channel": {"client-1"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := &WebSocketDriver{
				channels: make(map[string]map[string]*websocket.Client),
			}

			// Setup existing channels
			for channel, clientIDs := range tt.setupChannels {
				driver.channels[channel] = make(map[string]*websocket.Client)
				for _, id := range clientIDs {
					driver.channels[channel][id] = createTestClient(id)
				}
			}

			// Execute unsubscribe operations
			for _, op := range tt.unsubscribeOps {
				err := driver.Unsubscribe(op.channel, op.clientID)
				if (err != nil) != tt.wantErr {
					t.Errorf("Unsubscribe() error = %v, wantErr %v", err, tt.wantErr)
				}
			}

			// Verify channels
			if len(tt.wantChannels) == 0 {
				if len(driver.channels) != 0 {
					t.Errorf("expected no channels, got %d", len(driver.channels))
				}
				return
			}

			for channel, wantClients := range tt.wantChannels {
				clients, exists := driver.channels[channel]
				if !exists {
					t.Errorf("expected channel %q to exist", channel)
					continue
				}

				if len(clients) != len(wantClients) {
					t.Errorf("channel %q: got %d clients, want %d", channel, len(clients), len(wantClients))
				}

				for _, clientID := range wantClients {
					if _, ok := clients[clientID]; !ok {
						t.Errorf("channel %q: expected client %q to be subscribed", channel, clientID)
					}
				}
			}
		})
	}
}

func TestWebSocketDriver_Broadcast(t *testing.T) {
	tests := []struct {
		name          string
		setupChannels map[string][]string
		channels      []string
		event         string
		data          interface{}
		wantMessages  map[string][]websocket.Message // clientID -> expected messages
		wantErr       bool
	}{
		{
			name: "broadcasts to single channel with one client",
			setupChannels: map[string][]string{
				"news": {"client-1"},
			},
			channels: []string{"news"},
			event:    "update",
			data:     map[string]string{"title": "Test"},
			wantMessages: map[string][]websocket.Message{
				"client-1": {{Type: "update", Data: map[string]string{"title": "Test"}}},
			},
			wantErr: false,
		},
		{
			name: "broadcasts to single channel with multiple clients",
			setupChannels: map[string][]string{
				"news": {"client-1", "client-2", "client-3"},
			},
			channels: []string{"news"},
			event:    "update",
			data:     "test data",
			wantMessages: map[string][]websocket.Message{
				"client-1": {{Type: "update", Data: "test data"}},
				"client-2": {{Type: "update", Data: "test data"}},
				"client-3": {{Type: "update", Data: "test data"}},
			},
			wantErr: false,
		},
		{
			name: "broadcasts to multiple channels",
			setupChannels: map[string][]string{
				"channel-a": {"client-1"},
				"channel-b": {"client-2"},
			},
			channels: []string{"channel-a", "channel-b"},
			event:    "notification",
			data:     "hello",
			wantMessages: map[string][]websocket.Message{
				"client-1": {{Type: "notification", Data: "hello"}},
				"client-2": {{Type: "notification", Data: "hello"}},
			},
			wantErr: false,
		},
		{
			name: "client in multiple channels receives message once per channel",
			setupChannels: map[string][]string{
				"channel-a": {"client-1"},
				"channel-b": {"client-1"},
			},
			channels: []string{"channel-a", "channel-b"},
			event:    "notification",
			data:     "hello",
			wantMessages: map[string][]websocket.Message{
				"client-1": {
					{Type: "notification", Data: "hello"},
					{Type: "notification", Data: "hello"},
				},
			},
			wantErr: false,
		},
		{
			name:          "broadcasts to empty channel list",
			setupChannels: map[string][]string{},
			channels:      []string{},
			event:         "update",
			data:          "test",
			wantMessages:  map[string][]websocket.Message{},
			wantErr:       false,
		},
		{
			name: "broadcasts to non-existent channel",
			setupChannels: map[string][]string{
				"existing": {"client-1"},
			},
			channels:     []string{"non-existent"},
			event:        "update",
			data:         "test",
			wantMessages: map[string][]websocket.Message{},
			wantErr:      false,
		},
		{
			name: "broadcasts to channel with no clients",
			setupChannels: map[string][]string{
				"empty-channel": {},
			},
			channels:     []string{"empty-channel"},
			event:        "update",
			data:         "test",
			wantMessages: map[string][]websocket.Message{},
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := &WebSocketDriver{
				channels: make(map[string]map[string]*websocket.Client),
			}

			// Track all clients for verification
			allClients := make(map[string]*websocket.Client)

			// Setup channels
			for channel, clientIDs := range tt.setupChannels {
				driver.channels[channel] = make(map[string]*websocket.Client)
				for _, id := range clientIDs {
					if _, exists := allClients[id]; !exists {
						allClients[id] = createTestClient(id)
					}
					driver.channels[channel][id] = allClients[id]
				}
			}

			// Execute broadcast
			err := driver.Broadcast(tt.channels, tt.event, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("Broadcast() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Verify messages
			for clientID, wantMsgs := range tt.wantMessages {
				client, exists := allClients[clientID]
				if !exists {
					t.Errorf("test setup error: client %q not found", clientID)
					continue
				}

				gotMsgs := drainMessages(client)
				if len(gotMsgs) != len(wantMsgs) {
					t.Errorf("client %q: got %d messages, want %d", clientID, len(gotMsgs), len(wantMsgs))
					continue
				}

				for i, got := range gotMsgs {
					want := wantMsgs[i]
					if got.Type != want.Type {
						t.Errorf("client %q message %d: got type %q, want %q", clientID, i, got.Type, want.Type)
					}
				}
			}

			// Verify clients not in wantMessages got no messages
			for clientID, client := range allClients {
				if _, expected := tt.wantMessages[clientID]; !expected {
					msgs := drainMessages(client)
					if len(msgs) != 0 {
						t.Errorf("client %q: got %d unexpected messages", clientID, len(msgs))
					}
				}
			}
		})
	}
}

func TestWebSocketDriver_BroadcastExcept(t *testing.T) {
	tests := []struct {
		name          string
		setupChannels map[string][]string
		channels      []string
		event         string
		data          interface{}
		excludeID     string
		wantMessages  map[string][]websocket.Message
		wantErr       bool
	}{
		{
			name: "excludes specified client from broadcast",
			setupChannels: map[string][]string{
				"chat": {"client-1", "client-2", "client-3"},
			},
			channels:  []string{"chat"},
			event:     "message",
			data:      "hello",
			excludeID: "client-2",
			wantMessages: map[string][]websocket.Message{
				"client-1": {{Type: "message", Data: "hello"}},
				"client-3": {{Type: "message", Data: "hello"}},
			},
			wantErr: false,
		},
		{
			name: "excludes client across multiple channels",
			setupChannels: map[string][]string{
				"channel-a": {"client-1", "client-2"},
				"channel-b": {"client-2", "client-3"},
			},
			channels:  []string{"channel-a", "channel-b"},
			event:     "update",
			data:      "test",
			excludeID: "client-2",
			wantMessages: map[string][]websocket.Message{
				"client-1": {{Type: "update", Data: "test"}},
				"client-3": {{Type: "update", Data: "test"}},
			},
			wantErr: false,
		},
		{
			name: "broadcasts to all when excluded client not in channel",
			setupChannels: map[string][]string{
				"chat": {"client-1", "client-2"},
			},
			channels:  []string{"chat"},
			event:     "message",
			data:      "hello",
			excludeID: "client-999",
			wantMessages: map[string][]websocket.Message{
				"client-1": {{Type: "message", Data: "hello"}},
				"client-2": {{Type: "message", Data: "hello"}},
			},
			wantErr: false,
		},
		{
			name: "handles channel with only excluded client",
			setupChannels: map[string][]string{
				"solo": {"client-1"},
			},
			channels:     []string{"solo"},
			event:        "message",
			data:         "hello",
			excludeID:    "client-1",
			wantMessages: map[string][]websocket.Message{},
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := &WebSocketDriver{
				channels: make(map[string]map[string]*websocket.Client),
			}

			allClients := make(map[string]*websocket.Client)

			// Setup channels
			for channel, clientIDs := range tt.setupChannels {
				driver.channels[channel] = make(map[string]*websocket.Client)
				for _, id := range clientIDs {
					if _, exists := allClients[id]; !exists {
						allClients[id] = createTestClient(id)
					}
					driver.channels[channel][id] = allClients[id]
				}
			}

			// Execute broadcast except
			err := driver.BroadcastExcept(tt.channels, tt.event, tt.data, tt.excludeID)
			if (err != nil) != tt.wantErr {
				t.Errorf("BroadcastExcept() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Verify expected clients received messages
			for clientID, wantMsgs := range tt.wantMessages {
				client, exists := allClients[clientID]
				if !exists {
					t.Errorf("test setup error: client %q not found", clientID)
					continue
				}

				gotMsgs := drainMessages(client)
				if len(gotMsgs) != len(wantMsgs) {
					t.Errorf("client %q: got %d messages, want %d", clientID, len(gotMsgs), len(wantMsgs))
				}
			}

			// Verify excluded client received no messages
			if excludedClient, exists := allClients[tt.excludeID]; exists {
				msgs := drainMessages(excludedClient)
				if len(msgs) != 0 {
					t.Errorf("excluded client %q received %d messages, want 0", tt.excludeID, len(msgs))
				}
			}
		})
	}
}

func TestWebSocketDriver_handleSubscribe(t *testing.T) {
	tests := []struct {
		name         string
		msgData      interface{}
		wantErr      bool
		wantErrMsg   string
		wantChannel  string
		wantClientID string
	}{
		{
			name: "subscribes client with valid data",
			msgData: map[string]interface{}{
				"channel": "test-channel",
			},
			wantErr:      false,
			wantChannel:  "test-channel",
			wantClientID: "client-1",
		},
		{
			name:       "returns error when data is not a map",
			msgData:    "invalid",
			wantErr:    true,
			wantErrMsg: "invalid subscribe data",
		},
		{
			name:       "returns error when data is nil",
			msgData:    nil,
			wantErr:    true,
			wantErrMsg: "invalid subscribe data",
		},
		{
			name: "returns error when channel not specified",
			msgData: map[string]interface{}{
				"other": "value",
			},
			wantErr:    true,
			wantErrMsg: "channel not specified",
		},
		{
			name: "returns error when channel is not a string",
			msgData: map[string]interface{}{
				"channel": 123,
			},
			wantErr:    true,
			wantErrMsg: "channel not specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := &WebSocketDriver{
				channels: make(map[string]map[string]*websocket.Client),
			}

			client := createTestClient("client-1")
			msg := websocket.Message{
				Type: "subscribe",
				Data: tt.msgData,
			}

			err := driver.handleSubscribe(client, msg)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
					return
				}
				if err.Error() != tt.wantErrMsg {
					t.Errorf("got error %q, want %q", err.Error(), tt.wantErrMsg)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			// Verify subscription
			clients, exists := driver.channels[tt.wantChannel]
			if !exists {
				t.Errorf("expected channel %q to exist", tt.wantChannel)
				return
			}

			if _, ok := clients[tt.wantClientID]; !ok {
				t.Errorf("expected client %q to be subscribed to channel %q", tt.wantClientID, tt.wantChannel)
			}

			// Verify confirmation message was sent
			msgs := drainMessages(client)
			if len(msgs) != 1 {
				t.Errorf("expected 1 confirmation message, got %d", len(msgs))
				return
			}

			if msgs[0].Type != "subscription_succeeded" {
				t.Errorf("got message type %q, want %q", msgs[0].Type, "subscription_succeeded")
			}
		})
	}
}

func TestWebSocketDriver_handleUnsubscribe(t *testing.T) {
	tests := []struct {
		name          string
		setupChannels map[string][]string
		msgData       interface{}
		wantErr       bool
		wantErrMsg    string
		wantRemoved   bool
	}{
		{
			name: "unsubscribes client with valid data",
			setupChannels: map[string][]string{
				"test-channel": {"client-1"},
			},
			msgData: map[string]interface{}{
				"channel": "test-channel",
			},
			wantErr:     false,
			wantRemoved: true,
		},
		{
			name:          "handles unsubscribe from non-existent channel",
			setupChannels: nil,
			msgData: map[string]interface{}{
				"channel": "non-existent",
			},
			wantErr:     false,
			wantRemoved: false,
		},
		{
			name:       "returns error when data is not a map",
			msgData:    "invalid",
			wantErr:    true,
			wantErrMsg: "invalid unsubscribe data",
		},
		{
			name: "returns error when channel not specified",
			msgData: map[string]interface{}{
				"other": "value",
			},
			wantErr:    true,
			wantErrMsg: "channel not specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := &WebSocketDriver{
				channels: make(map[string]map[string]*websocket.Client),
			}

			client := createTestClient("client-1")

			// Setup channels
			for channel, clientIDs := range tt.setupChannels {
				driver.channels[channel] = make(map[string]*websocket.Client)
				for _, id := range clientIDs {
					if id == "client-1" {
						driver.channels[channel][id] = client
					} else {
						driver.channels[channel][id] = createTestClient(id)
					}
				}
			}

			msg := websocket.Message{
				Type: "unsubscribe",
				Data: tt.msgData,
			}

			err := driver.handleUnsubscribe(client, msg)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
					return
				}
				if err.Error() != tt.wantErrMsg {
					t.Errorf("got error %q, want %q", err.Error(), tt.wantErrMsg)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			// Verify confirmation message was sent
			msgs := drainMessages(client)
			if len(msgs) != 1 {
				t.Errorf("expected 1 confirmation message, got %d", len(msgs))
				return
			}

			if msgs[0].Type != "unsubscription_succeeded" {
				t.Errorf("got message type %q, want %q", msgs[0].Type, "unsubscription_succeeded")
			}
		})
	}
}

func TestWebSocketDriver_handleClientEvent(t *testing.T) {
	tests := []struct {
		name          string
		setupChannels map[string][]string
		clientID      string
		msgData       interface{}
		wantErr       bool
		wantErrMsg    string
		wantEvent     string
		wantReceivers []string
	}{
		{
			name: "broadcasts client event to other clients in channel",
			setupChannels: map[string][]string{
				"chat": {"client-1", "client-2", "client-3"},
			},
			clientID: "client-1",
			msgData: map[string]interface{}{
				"channel": "chat",
				"event":   "typing",
				"data":    "user is typing",
			},
			wantErr:       false,
			wantEvent:     "client-typing",
			wantReceivers: []string{"client-2", "client-3"},
		},
		{
			name:       "returns error when data is not a map",
			clientID:   "client-1",
			msgData:    "invalid",
			wantErr:    true,
			wantErrMsg: "invalid client event data",
		},
		{
			name:     "returns error when channel not specified",
			clientID: "client-1",
			msgData: map[string]interface{}{
				"event": "typing",
			},
			wantErr:    true,
			wantErrMsg: "channel not specified",
		},
		{
			name:     "returns error when event not specified",
			clientID: "client-1",
			msgData: map[string]interface{}{
				"channel": "chat",
			},
			wantErr:    true,
			wantErrMsg: "event not specified",
		},
		{
			name: "handles client event when sender is only one in channel",
			setupChannels: map[string][]string{
				"solo": {"client-1"},
			},
			clientID: "client-1",
			msgData: map[string]interface{}{
				"channel": "solo",
				"event":   "typing",
			},
			wantErr:       false,
			wantEvent:     "client-typing",
			wantReceivers: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := &WebSocketDriver{
				channels: make(map[string]map[string]*websocket.Client),
			}

			allClients := make(map[string]*websocket.Client)
			senderClient := createTestClient(tt.clientID)
			allClients[tt.clientID] = senderClient

			// Setup channels
			for channel, clientIDs := range tt.setupChannels {
				driver.channels[channel] = make(map[string]*websocket.Client)
				for _, id := range clientIDs {
					if id == tt.clientID {
						driver.channels[channel][id] = senderClient
					} else {
						if _, exists := allClients[id]; !exists {
							allClients[id] = createTestClient(id)
						}
						driver.channels[channel][id] = allClients[id]
					}
				}
			}

			msg := websocket.Message{
				Type: "client-event",
				Data: tt.msgData,
			}

			err := driver.handleClientEvent(senderClient, msg)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
					return
				}
				if err.Error() != tt.wantErrMsg {
					t.Errorf("got error %q, want %q", err.Error(), tt.wantErrMsg)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			// Verify sender did not receive the message
			senderMsgs := drainMessages(senderClient)
			if len(senderMsgs) != 0 {
				t.Errorf("sender received %d messages, want 0", len(senderMsgs))
			}

			// Verify receivers got the message with prefixed event name
			for _, receiverID := range tt.wantReceivers {
				receiver, exists := allClients[receiverID]
				if !exists {
					t.Errorf("test setup error: receiver %q not found", receiverID)
					continue
				}

				msgs := drainMessages(receiver)
				if len(msgs) != 1 {
					t.Errorf("receiver %q: got %d messages, want 1", receiverID, len(msgs))
					continue
				}

				if msgs[0].Type != tt.wantEvent {
					t.Errorf("receiver %q: got event %q, want %q", receiverID, msgs[0].Type, tt.wantEvent)
				}
			}
		})
	}
}

func TestWebSocketDriver_GetClients(t *testing.T) {
	tests := []struct {
		name          string
		setupChannels map[string][]string
		channel       string
		wantClientIDs []string
	}{
		{
			name: "returns client IDs for channel with clients",
			setupChannels: map[string][]string{
				"chat": {"client-1", "client-2", "client-3"},
			},
			channel:       "chat",
			wantClientIDs: []string{"client-1", "client-2", "client-3"},
		},
		{
			name: "returns empty slice for channel with no clients",
			setupChannels: map[string][]string{
				"empty": {},
			},
			channel:       "empty",
			wantClientIDs: nil,
		},
		{
			name:          "returns nil for non-existent channel",
			setupChannels: nil,
			channel:       "non-existent",
			wantClientIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := &WebSocketDriver{
				channels: make(map[string]map[string]*websocket.Client),
			}

			// Setup channels
			for channel, clientIDs := range tt.setupChannels {
				driver.channels[channel] = make(map[string]*websocket.Client)
				for _, id := range clientIDs {
					driver.channels[channel][id] = createTestClient(id)
				}
			}

			got := driver.GetClients(tt.channel)

			if len(got) != len(tt.wantClientIDs) {
				t.Errorf("GetClients() returned %d clients, want %d", len(got), len(tt.wantClientIDs))
				return
			}

			// Verify all expected client IDs are present (order may vary)
			gotMap := make(map[string]bool)
			for _, id := range got {
				gotMap[id] = true
			}

			for _, wantID := range tt.wantClientIDs {
				if !gotMap[wantID] {
					t.Errorf("GetClients() missing client ID %q", wantID)
				}
			}
		})
	}
}

func TestWebSocketDriver_GetServer(t *testing.T) {
	tests := []struct {
		name string
	}{
		{
			name: "returns the underlying websocket server",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := websocket.DefaultConfig()
			driver := NewWebSocketDriver(config)
			defer driver.server.Stop()

			server := driver.GetServer()

			if server == nil {
				t.Error("GetServer() returned nil, want non-nil server")
			}

			if server != driver.server {
				t.Error("GetServer() returned different server instance")
			}
		})
	}
}

func TestWebSocketDriver_ConcurrentAccess(t *testing.T) {
	driver := &WebSocketDriver{
		channels: make(map[string]map[string]*websocket.Client),
	}

	var wg sync.WaitGroup
	numGoroutines := 10
	numOperations := 100

	// Concurrent subscribes
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				client := createTestClient("client-" + string(rune('a'+id)))
				driver.Subscribe("test-channel", client)
			}
		}(i)
	}

	// Concurrent broadcasts
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				driver.Broadcast([]string{"test-channel"}, "event", "data")
			}
		}()
	}

	// Concurrent GetClients
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				driver.GetClients("test-channel")
			}
		}()
	}

	wg.Wait()

	// If we get here without race detector complaints, the test passes
}

func TestWebSocketDriver_BroadcastWithFullChannel(t *testing.T) {
	driver := &WebSocketDriver{
		channels: make(map[string]map[string]*websocket.Client),
	}

	// Create a client with a small buffer that we'll fill
	client := &websocket.Client{
		ID:       "slow-client",
		Send:     make(chan websocket.Message, 1), // Very small buffer
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}

	driver.channels["test"] = map[string]*websocket.Client{
		"slow-client": client,
	}

	// Fill the channel
	client.Send <- websocket.Message{Type: "filler", Data: "data"}

	// This should not block - message should be dropped
	err := driver.Broadcast([]string{"test"}, "event", "data")
	if err != nil {
		t.Errorf("Broadcast() should not return error when channel is full, got %v", err)
	}

	// Drain and verify only the filler message is there
	msgs := drainMessages(client)
	if len(msgs) != 1 {
		t.Errorf("expected 1 message (the filler), got %d", len(msgs))
	}
	if msgs[0].Type != "filler" {
		t.Errorf("expected filler message, got %q", msgs[0].Type)
	}
}
