package mail

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestNewManager(t *testing.T) {
	manager := NewManager()
	if manager == nil {
		t.Fatal("Expected manager to be created")
	}

	if manager.channels == nil {
		t.Error("Expected channels map to be initialized")
	}
}

func TestManagerChannel(t *testing.T) {
	manager := NewManager()

	// Get channel (should auto-create)
	mailer := manager.Channel("test")
	if mailer == nil {
		t.Error("Expected mailer to be created")
	}

	// Get same channel again (should return same instance)
	mailer2 := manager.Channel("test")
	if mailer != mailer2 {
		t.Error("Expected same mailer instance for same channel")
	}
}

func TestManagerSetChannel(t *testing.T) {
	manager := NewManager()
	mock := &mockMailer{sent: make([]*Message, 0)}

	manager.SetChannel("custom", mock)

	mailer := manager.Channel("custom")
	if mailer != mock {
		t.Error("Expected custom mailer to be returned")
	}
}

func TestManagerHasChannel(t *testing.T) {
	manager := NewManager()

	if manager.HasChannel("test") {
		t.Error("Expected channel to not exist")
	}

	manager.Channel("test")

	if !manager.HasChannel("test") {
		t.Error("Expected channel to exist after creation")
	}
}

func TestManagerGetChannels(t *testing.T) {
	manager := NewManager()

	manager.Channel("channel1")
	manager.Channel("channel2")
	manager.Channel("channel3")

	channels := manager.GetChannels()
	if len(channels) != 3 {
		t.Errorf("Expected 3 channels, got %d", len(channels))
	}
}

func TestManagerSend(t *testing.T) {
	manager := NewManager()
	mock := &mockMailer{sent: make([]*Message, 0)}
	manager.SetChannel("test", mock)

	msg := NewMessage().To("test@example.com").Subject("Test")
	err := manager.Send(context.Background(), "test", msg)

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if len(mock.sent) != 1 {
		t.Errorf("Expected 1 message sent, got %d", len(mock.sent))
	}
}

func TestManagerBroadcast(t *testing.T) {
	manager := NewManager()

	mock1 := &mockMailer{sent: make([]*Message, 0)}
	mock2 := &mockMailer{sent: make([]*Message, 0)}
	mock3 := &mockMailer{sent: make([]*Message, 0)}

	manager.SetChannel("channel1", mock1)
	manager.SetChannel("channel2", mock2)
	manager.SetChannel("channel3", mock3)

	msg := NewMessage().To("test@example.com").Subject("Broadcast")
	channels := []string{"channel1", "channel2", "channel3"}

	err := manager.Broadcast(context.Background(), channels, msg)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if len(mock1.sent) != 1 || len(mock2.sent) != 1 || len(mock3.sent) != 1 {
		t.Error("Expected all channels to receive message")
	}
}

type errorMailer struct{}

func (m *errorMailer) Send(ctx context.Context, msg *Message) error {
	return fmt.Errorf("send error")
}

func TestManagerBroadcastError(t *testing.T) {
	manager := NewManager()

	// Set up channels with error mailer
	manager.SetChannel("error1", &errorMailer{})
	manager.SetChannel("error2", &errorMailer{})

	msg := NewMessage().To("test@example.com").Subject("Broadcast")

	channels := []string{"error1", "error2"}
	err := manager.Broadcast(context.Background(), channels, msg)

	// Should get errors from both channels
	if err == nil {
		t.Error("Expected error from broadcast with failing mailers")
	}
}

func TestManagerRemoveChannel(t *testing.T) {
	manager := NewManager()
	manager.Channel("test")

	if !manager.HasChannel("test") {
		t.Error("Expected channel to exist")
	}

	manager.RemoveChannel("test")

	if manager.HasChannel("test") {
		t.Error("Expected channel to be removed")
	}
}

func TestManagerClearChannels(t *testing.T) {
	manager := NewManager()
	manager.Channel("channel1")
	manager.Channel("channel2")
	manager.Channel("channel3")

	if len(manager.GetChannels()) != 3 {
		t.Error("Expected 3 channels before clear")
	}

	manager.ClearChannels()

	if len(manager.GetChannels()) != 0 {
		t.Error("Expected 0 channels after clear")
	}
}

func TestNewManagerReturnsDistinctInstances(t *testing.T) {
	manager1 := NewManager()
	manager2 := NewManager()

	if manager1 == manager2 {
		t.Error("Expected distinct manager instances from NewManager()")
	}
}

func TestManagerConcurrency(t *testing.T) {
	manager := NewManager()
	var wg sync.WaitGroup

	// Concurrent channel access
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			manager.Channel("concurrent")
		}(i)
	}

	wg.Wait()

	// Should only have created one channel
	channels := manager.GetChannels()
	if len(channels) != 1 {
		t.Errorf("Expected 1 channel, got %d", len(channels))
	}
}

func TestManagerConcurrentBroadcast(t *testing.T) {
	manager := NewManager()
	mock1 := &mockMailer{sent: make([]*Message, 0)}
	mock2 := &mockMailer{sent: make([]*Message, 0)}

	manager.SetChannel("ch1", mock1)
	manager.SetChannel("ch2", mock2)

	var wg sync.WaitGroup
	channels := []string{"ch1", "ch2"}

	// Send 10 broadcasts concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msg := NewMessage().To("test@example.com").Subject("Concurrent")
			manager.Broadcast(context.Background(), channels, msg)
		}()
	}

	wg.Wait()

	// Each mock should have received 10 messages
	if count := mock1.SentCount(); count != 10 {
		t.Errorf("Expected 10 messages in ch1, got %d", count)
	}
	if count := mock2.SentCount(); count != 10 {
		t.Errorf("Expected 10 messages in ch2, got %d", count)
	}
}
