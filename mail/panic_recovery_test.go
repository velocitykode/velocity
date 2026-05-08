package mail

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

// panicMailer panics on Send — used to verify Broadcast recovers.
type panicMailer struct{}

func (panicMailer) Send(ctx context.Context, msg *Message) error {
	panic("mailer boom")
}

// TestManagerBroadcast_RecoversPanic verifies that a panic inside one of
// the fan-out goroutines in Broadcast does not tear down the manager,
// surfaces through a MailFailed event, and returns an aggregated error.
func TestManagerBroadcast_RecoversPanic(t *testing.T) {
	m := NewManager()
	m.SetChannel("panic", panicMailer{})
	m.SetChannel("ok", &mockMailer{sent: make([]*Message, 0)})

	var failedCount atomic.Int32
	m.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		if _, ok := event.(*MailFailed); ok {
			failedCount.Add(1)
		}
		return nil
	})

	msg := NewMessage().To("user@example.com").Subject("hello")

	err := m.Broadcast(context.Background(), []string{"panic", "ok"}, msg)
	if err == nil {
		t.Fatal("expected aggregated error from panicking channel, got nil")
	}
	if !strings.Contains(err.Error(), "broadcast failed") {
		t.Fatalf("expected aggregated error prefix, got %v", err)
	}
	if failedCount.Load() < 1 {
		t.Fatalf("expected at least one MailFailed event, got %d", failedCount.Load())
	}

	// Manager stays alive — the ok channel must still send.
	if err := m.Send(context.Background(), "ok", msg); err != nil {
		t.Fatalf("manager no longer usable after panic: %v", err)
	}
}
