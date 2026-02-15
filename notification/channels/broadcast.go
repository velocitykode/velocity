package channels

import (
	"context"
	"fmt"

	"github.com/velocitykode/velocity/broadcast"
	"github.com/velocitykode/velocity/notification"
)

func init() {
	notification.RegisterChannel("broadcast", func() (notification.Channel, error) {
		return NewBroadcastChannel(), nil
	})
}

// BroadcastChannel delivers notifications via real-time broadcasting (WebSocket).
type BroadcastChannel struct {
	broadcaster *broadcast.BroadcastManager
}

// NewBroadcastChannel creates a new broadcast notification channel.
func NewBroadcastChannel() *BroadcastChannel {
	return &BroadcastChannel{}
}

// SetBroadcaster sets the broadcast manager used to deliver notifications.
func (c *BroadcastChannel) SetBroadcaster(b *broadcast.BroadcastManager) {
	c.broadcaster = b
}

// Send delivers a notification via broadcasting.
func (c *BroadcastChannel) Send(ctx context.Context, notifiable interface{}, n notification.Notification) error {
	bn, ok := n.(notification.BroadcastNotification)
	if !ok {
		return fmt.Errorf("notification: %T does not implement BroadcastNotification", n)
	}

	broadcastMsg := bn.ToBroadcast(notifiable)
	if broadcastMsg == nil {
		return nil
	}

	if c.broadcaster == nil {
		return fmt.Errorf("notification: broadcast channel has no broadcaster configured")
	}

	channels := broadcastMsg.Channels
	if len(channels) == 0 {
		// Default to a private channel for the notifiable
		if nr, ok := notifiable.(notification.Notifiable); ok {
			route := nr.NotificationRoute("broadcast")
			if route != "" {
				channels = []string{"private-" + route}
			}
		}
	}

	if len(channels) == 0 {
		return nil
	}

	return c.broadcaster.Channel(channels...).Emit(broadcastMsg.Event, broadcastMsg.Data)
}
