package drivers

import (
	"testing"

	"github.com/velocitykode/velocity/broadcast"
	"github.com/velocitykode/velocity/broadcast/broadcasttest"
	"github.com/velocitykode/velocity/websocket"
)

// TestWebSocketDriver_Contract runs the broadcasttest spec against the
// WebSocket driver. The factory builds the driver WITHOUT calling
// NewWebSocketDriver because that constructor starts the underlying
// websocket server; the contract runner exercises only the surface
// observable without an active subscriber, so the bare struct suffices.
func TestWebSocketDriver_Contract(t *testing.T) {
	broadcasttest.RunDriverContractTests(t, func(t *testing.T) broadcast.Driver {
		return &WebSocketDriver{
			channels:             make(map[string]map[string]*websocket.Client),
			clientSubs:           make(map[string]map[string]struct{}),
			authorizer:           denyAllChannelAuthorizer,
			maxChannelsPerClient: DefaultMaxChannelsPerClient,
			maxChannelNameLength: DefaultMaxChannelNameLength,
		}
	})
}
