package notification

import (
	"context"
	"testing"
)

// nopChannel accepts every notification and records nothing.
type nopChannel struct{}

func (nopChannel) Send(context.Context, interface{}, Notification) error { return nil }

// TestManager_NoDispatcherBuildsNoEvent requires a delivery to build no
// NotificationSent event when no event dispatcher is installed. Building
// the event is the only thing that asks the notifiable for its ID, so the
// notifiable is asked once with a dispatcher and never without.
func TestManager_NoDispatcherBuildsNoEvent(t *testing.T) {
	m := NewManager()
	m.SetChannel("nop", nopChannel{})
	n := &testNotification{channels: []string{"nop"}}
	asked := 0
	notifiable := &identifiedNotifiable{identify: func() string { asked++; return "user-7" }}
	deliver := func() { _ = m.sendViaChannel(context.Background(), "nop", notifiable, n, nil) }

	deliver()
	if asked != 0 {
		t.Errorf("a delivery with no dispatcher built its event (NotifiableID asked %d times)", asked)
	}
	var sent int
	m.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		if _, ok := event.(*NotificationSent); ok {
			sent++
		}
		return nil
	})
	deliver()
	if asked != 1 || sent != 1 {
		t.Errorf("with a dispatcher: NotifiableID asked %d times, NotificationSent received %d times, want 1 and 1", asked, sent)
	}
}
