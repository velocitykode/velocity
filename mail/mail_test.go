package mail

import (
	"context"
	"sync"
	"testing"
)

// Mock mailer for testing
type mockMailer struct {
	mu   sync.Mutex
	sent []*Message
}

func (m *mockMailer) Send(ctx context.Context, msg *Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func (m *mockMailer) SentCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

func TestManagerSetAndGetChannel(t *testing.T) {
	manager := NewManager()
	mock := &mockMailer{sent: make([]*Message, 0)}
	manager.SetChannel("default", mock)

	mailer, _ := manager.Channel("default")
	if mailer != mock {
		t.Error("Expected channel mailer to be the mock")
	}
}

func TestManagerSendViaChannel(t *testing.T) {
	manager := NewManager()
	mock := &mockMailer{sent: make([]*Message, 0)}
	manager.SetChannel("default", mock)

	msg := NewMessage().
		To("test@example.com").
		Subject("Test").
		Body("Hello")

	err := manager.Send(context.Background(), "default", msg)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if len(mock.sent) != 1 {
		t.Errorf("Expected 1 message sent, got %d", len(mock.sent))
	}
}

func TestDirectMailerSend(t *testing.T) {
	mock := &mockMailer{sent: make([]*Message, 0)}

	msg := NewMessage().
		To("test@example.com").
		Subject("Test").
		Body("Hello")

	err := mock.Send(context.Background(), msg)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if len(mock.sent) != 1 {
		t.Errorf("Expected 1 message sent, got %d", len(mock.sent))
	}
}

func TestAddress(t *testing.T) {
	t.Run("email only", func(t *testing.T) {
		addr := Address{Email: "test@example.com"}
		if addr.String() != "test@example.com" {
			t.Errorf("Expected 'test@example.com', got '%s'", addr.String())
		}
	})

	t.Run("email with name", func(t *testing.T) {
		addr := Address{Email: "test@example.com", Name: "Test User"}
		expected := "Test User <test@example.com>"
		if addr.String() != expected {
			t.Errorf("Expected '%s', got '%s'", expected, addr.String())
		}
	})
}

func TestPriority(t *testing.T) {
	if LowPriority >= NormalPriority {
		t.Error("LowPriority should be less than NormalPriority")
	}
	if NormalPriority >= HighPriority {
		t.Error("NormalPriority should be less than HighPriority")
	}
}
