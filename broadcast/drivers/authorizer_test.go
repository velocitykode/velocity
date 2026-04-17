package drivers

import (
	"strings"
	"testing"

	"github.com/velocitykode/velocity/websocket"
)

// TestHandleSubscribe_DefaultDeny covers Task 1 for the websocket driver:
// a freshly-constructed driver must deny subscriptions to private- and
// presence- channels because the default authorizer is deny-all.
func TestHandleSubscribe_DefaultDeny(t *testing.T) {
	d := &WebSocketDriver{
		channels:   make(map[string]map[string]*websocket.Client),
		authorizer: denyAllChannelAuthorizer,
	}
	client := createTestClient("c1")

	for _, ch := range []string{"private-x", "presence-y"} {
		err := d.handleSubscribe(client, websocket.Message{
			Type: "subscribe",
			Data: map[string]interface{}{"channel": ch},
		})
		if err == nil || !strings.Contains(err.Error(), "velocity/broadcast") {
			t.Errorf("channel %s: want velocity/broadcast error, got %v", ch, err)
		}
	}
}

// TestHandleSubscribe_AuthorizerAllows verifies an installed authorizer is
// consulted for restricted channels.
func TestHandleSubscribe_AuthorizerAllows(t *testing.T) {
	d := &WebSocketDriver{
		channels:   make(map[string]map[string]*websocket.Client),
		authorizer: func(client *websocket.Client, channel string) bool { return channel == "private-ok" },
	}
	client := createTestClient("c2")

	err := d.handleSubscribe(client, websocket.Message{
		Type: "subscribe",
		Data: map[string]interface{}{"channel": "private-ok"},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if _, ok := d.channels["private-ok"]["c2"]; !ok {
		t.Fatal("client was not subscribed after authorizer approval")
	}
}
