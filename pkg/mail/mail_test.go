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

func TestSetDefaultMailer(t *testing.T) {
	mock := &mockMailer{sent: make([]*Message, 0)}
	SetDefaultMailer(mock)

	if GetDefaultMailer() != mock {
		t.Error("Expected default mailer to be set")
	}
}

func TestSend(t *testing.T) {
	mock := &mockMailer{sent: make([]*Message, 0)}
	SetDefaultMailer(mock)

	msg := NewMessage().
		To("test@example.com").
		Subject("Test").
		Body("Hello")

	err := Send(context.Background(), msg)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if len(mock.sent) != 1 {
		t.Errorf("Expected 1 message sent, got %d", len(mock.sent))
	}
}

func TestSendPanicsWithoutMailer(t *testing.T) {
	// Save current mailer
	saved := defaultMailer
	defer func() {
		defaultMailer = saved
	}()

	// Set to nil
	defaultMailer = nil

	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic when mailer not initialized")
		}
	}()

	Send(context.Background(), NewMessage())
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
