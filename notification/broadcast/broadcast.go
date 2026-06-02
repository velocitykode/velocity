package broadcast

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	velbroadcast "github.com/velocitykode/velocity/broadcast"
	"github.com/velocitykode/velocity/notification"
)

func init() {
	notification.Drivers().Register("broadcast", func(_ context.Context, _ notification.ChannelConfig) (notification.Channel, error) {
		return NewBroadcastChannel(), nil
	})
}

// BroadcastChannelAuthorizer is the per-channel-name outbound gate.
// Notification authors may compute broadcast channel names from less-
// trusted state (request input, multi-tenant identifiers, ...); without
// an authorizer, a notification fired by tenant A is free to land on a
// channel name owned by tenant B. Operators install an authorizer that
// returns true only when the (notifiable, channel) pair is consistent
// with their tenancy model.
//
// Authorize is called once per channel name on every Send. Returning
// false skips the broadcast for that channel; other channels in the
// list are evaluated independently so a misconfigured single channel
// does not block the whole notification.
type BroadcastChannelAuthorizer interface {
	Authorize(notifiable interface{}, channel string) bool
}

// BroadcastChannelAuthorizerFunc adapts a plain function to the
// BroadcastChannelAuthorizer interface.
type BroadcastChannelAuthorizerFunc func(notifiable interface{}, channel string) bool

// Authorize delegates to the wrapped function.
func (f BroadcastChannelAuthorizerFunc) Authorize(notifiable interface{}, channel string) bool {
	return f(notifiable, channel)
}

// BroadcastChannel delivers notifications via real-time broadcasting (WebSocket).
type BroadcastChannel struct {
	broadcaster *velbroadcast.BroadcastManager

	authMu     sync.RWMutex
	authorizer BroadcastChannelAuthorizer
	// warnedOnce makes the missing-authorizer WARN log fire at most one
	// time per channel instance so multi-tenant operators see the gap
	// without spamming the log on every Send.
	warnedOnce sync.Once
}

// NewBroadcastChannel creates a new broadcast notification channel.
func NewBroadcastChannel() *BroadcastChannel {
	return &BroadcastChannel{}
}

// SetBroadcaster sets the broadcast manager used to deliver notifications.
func (c *BroadcastChannel) SetBroadcaster(b *velbroadcast.BroadcastManager) {
	c.broadcaster = b
}

// SetAuthorizer installs the outbound channel authorizer. Passing nil
// reverts to the default open-but-warn behaviour. Multi-tenant
// deployments MUST install an authorizer; the default exists only so
// single-tenant apps keep working out of the box.
func (c *BroadcastChannel) SetAuthorizer(a BroadcastChannelAuthorizer) {
	c.authMu.Lock()
	c.authorizer = a
	c.authMu.Unlock()
}

// authorizerOrWarn returns the configured authorizer, or nil after
// emitting a one-shot WARN line that flags the missing gate.
func (c *BroadcastChannel) authorizerOrWarn() BroadcastChannelAuthorizer {
	c.authMu.RLock()
	a := c.authorizer
	c.authMu.RUnlock()
	if a != nil {
		return a
	}
	c.warnedOnce.Do(func() {
		slog.Default().Warn(
			"notification.broadcast: no BroadcastChannelAuthorizer installed; outbound channel names are not authorized against the notifiable",
		)
	})
	return nil
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

	// Run the outbound authorizer per channel name. The authorizer is
	// the only line of defence against a notification author routing
	// onto a foreign tenant's channel; without it, broadcastMsg.On(...)
	// has full trust.
	if auth := c.authorizerOrWarn(); auth != nil {
		filtered := make([]string, 0, len(channels))
		var denied []string
		for _, name := range channels {
			if auth.Authorize(notifiable, name) {
				filtered = append(filtered, name)
				continue
			}
			denied = append(denied, name)
		}
		if len(denied) > 0 {
			// Visible in the application log so operators can audit
			// misrouted notifications during onboarding.
			slog.Default().Warn(
				"notification.broadcast: authorizer rejected channel(s)",
				slog.Any("denied", denied),
			)
		}
		if len(filtered) == 0 {
			return fmt.Errorf("velocity/notification: broadcast: every channel rejected by authorizer")
		}
		channels = filtered
	}

	return c.broadcaster.Channel(channels...).Emit(broadcastMsg.Event, broadcastMsg.Data)
}
