package notification

import (
	"context"
	"testing"
)

// nopChannel accepts every notification and records nothing.
type nopChannel struct{}

func (nopChannel) Send(context.Context, interface{}, Notification) error { return nil }

// TestManager_NoDispatcherBuildsNoEvent requires a delivery to build no
// NotificationSent event when no event dispatcher is installed: it
// allocates less than the same delivery with a dispatcher that discards
// every event.
func TestManager_NoDispatcherBuildsNoEvent(t *testing.T) {
	m := NewManager()
	m.SetChannel("nop", nopChannel{})
	n := &testNotification{channels: []string{"nop"}}
	ctx := context.Background()
	deliver := func() { _ = m.sendViaChannel(ctx, "nop", "user", n, nil) }

	without := testing.AllocsPerRun(100, deliver)
	m.SetEventDispatcher(func(context.Context, interface{}) error { return nil })
	with := testing.AllocsPerRun(100, deliver)
	if without >= with {
		t.Errorf("delivery allocated %.0f times with no dispatcher and %.0f with one, want fewer without", without, with)
	}
}
