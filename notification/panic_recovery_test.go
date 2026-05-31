package notification

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// panicChannel panics on Send - used to verify SendMany recovers.
type panicChannel struct{}

func (panicChannel) Send(ctx context.Context, notifiable interface{}, notification Notification) error {
	panic("channel boom")
}

// TestSendMany_RecoversPanic verifies that a panic inside a per-notifiable
// Send call does not tear down the manager and surfaces through a
// NotificationFailed event plus an error return.
func TestSendMany_RecoversPanic(t *testing.T) {
	m := NewManager()
	m.SetChannel("panic", panicChannel{})

	var failedCount atomic.Int32
	m.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		if _, ok := event.(*NotificationFailed); ok {
			failedCount.Add(1)
		}
		return nil
	})

	n := &testNotification{subject: "boom", channels: []string{"panic"}}
	notifiables := []interface{}{
		&testNotifiable{email: "a@example.com"},
		&testNotifiable{email: "b@example.com"},
	}

	err := m.SendMany(context.Background(), notifiables, n)
	if err == nil {
		t.Fatal("expected error from panicking channel, got nil")
	}
	if !strings.Contains(err.Error(), "velocity/notification") && !strings.Contains(err.Error(), "channel") {
		t.Fatalf("expected wrapped error, got %v", err)
	}
	// Manager must stay usable after a panic.
	if _, err := m.Channel("panic"); err != nil {
		t.Fatalf("manager no longer usable after panic: %v", err)
	}

	// Each notifiable should have emitted a NotificationFailed event
	// (either from the explicit dispatchEvent in sendViaChannel or from
	// the panic recovery block in SendMany).
	if failedCount.Load() < 1 {
		t.Fatalf("expected at least one NotificationFailed event, got %d", failedCount.Load())
	}
}

// TestSendMany_RecoversPanic_NoDispatcher verifies recovery works even when
// no event dispatcher is configured.
func TestSendMany_RecoversPanic_NoDispatcher(t *testing.T) {
	m := NewManager()
	m.SetChannel("panic", panicChannel{})

	n := &testNotification{subject: "boom", channels: []string{"panic"}}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Must not panic.
		_ = m.SendMany(context.Background(), []interface{}{&testNotifiable{email: "a@example.com"}}, n)
	}()
	wg.Wait()
}
