package drivers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/websocket"
)

// TestSubscribe_ChannelCapExceeded covers audit D-03: a single client must
// not be able to open more than maxChannelsPerClient distinct subscriptions.
// The (N+1)th subscribe must be rejected with ErrChannelLimit and must not
// be persisted to either the channels map or the per-client set.
func TestSubscribe_ChannelCapExceeded(t *testing.T) {
	const cap = 5
	d := &WebSocketDriver{
		channels:             make(map[string]map[string]*websocket.Client),
		clientSubs:           make(map[string]map[string]struct{}),
		maxChannelsPerClient: cap,
		maxChannelNameLength: DefaultMaxChannelNameLength,
	}
	client := createTestClient("attacker")

	for i := 0; i < cap; i++ {
		ch := "room-" + string(rune('a'+i))
		if err := d.Subscribe(ch, client); err != nil {
			t.Fatalf("Subscribe %d to %q: unexpected error %v", i, ch, err)
		}
	}

	// The next subscribe must be rejected.
	err := d.Subscribe("room-overflow", client)
	if err == nil {
		t.Fatal("expected ErrChannelLimit on subscribe past cap, got nil")
	}
	if !errors.Is(err, ErrChannelLimit) {
		t.Fatalf("expected ErrChannelLimit, got %v", err)
	}

	// State must be unchanged: the channel must not exist in d.channels and
	// the per-client set must still have exactly cap entries.
	if _, exists := d.channels["room-overflow"]; exists {
		t.Error("rejected subscribe leaked into d.channels")
	}
	if got := len(d.clientSubs[client.ID]); got != cap {
		t.Errorf("per-client subscription count drifted: got %d, want %d", got, cap)
	}
}

// TestSubscribe_ChannelCapIdempotent verifies re-subscribing to the same
// channel does NOT count against the cap. This pins the documented intent
// that the cap measures held memberships, not total subscribe calls.
func TestSubscribe_ChannelCapIdempotent(t *testing.T) {
	d := &WebSocketDriver{
		channels:             make(map[string]map[string]*websocket.Client),
		clientSubs:           make(map[string]map[string]struct{}),
		maxChannelsPerClient: 2,
		maxChannelNameLength: DefaultMaxChannelNameLength,
	}
	client := createTestClient("c1")

	if err := d.Subscribe("room-a", client); err != nil {
		t.Fatalf("first subscribe: %v", err)
	}
	if err := d.Subscribe("room-a", client); err != nil {
		t.Fatalf("re-subscribe to same channel must not error: %v", err)
	}
	if err := d.Subscribe("room-b", client); err != nil {
		t.Fatalf("second distinct subscribe: %v", err)
	}
}

// TestSubscribe_ChannelCapDefaultApplied confirms NewWebSocketDriver
// installs DefaultMaxChannelsPerClient when no option is supplied.
func TestSubscribe_ChannelCapDefaultApplied(t *testing.T) {
	d := NewWebSocketDriver(websocket.DefaultConfig())
	defer d.server.Shutdown(context.Background()) //nolint:errcheck

	if d.maxChannelsPerClient != DefaultMaxChannelsPerClient {
		t.Errorf("default cap not applied: got %d, want %d", d.maxChannelsPerClient, DefaultMaxChannelsPerClient)
	}
	if d.maxChannelNameLength != DefaultMaxChannelNameLength {
		t.Errorf("default name-length cap not applied: got %d, want %d", d.maxChannelNameLength, DefaultMaxChannelNameLength)
	}
}

// TestSubscribe_ChannelCapDisabled verifies a negative cap (explicit opt
// out) lets a client subscribe past the default ceiling.
func TestSubscribe_ChannelCapDisabled(t *testing.T) {
	d := NewWebSocketDriver(websocket.DefaultConfig(), WithMaxChannelsPerClient(-1))
	defer d.server.Shutdown(context.Background()) //nolint:errcheck

	client := createTestClient("c1")
	// 200 subscriptions, well past the default of 100.
	for i := 0; i < 200; i++ {
		ch := "room-" + string(rune(i))
		if err := d.Subscribe(ch, client); err != nil {
			t.Fatalf("subscribe %d: %v", i, err)
		}
	}
}

// TestHandleSubscribe_OversizedChannelNameRejected covers the D-03 fix that
// rejects subscribes whose channel name exceeds maxChannelNameLength
// BEFORE touching the channels / clientSubs maps.
func TestHandleSubscribe_OversizedChannelNameRejected(t *testing.T) {
	const nameCap = 16
	d := &WebSocketDriver{
		channels:             make(map[string]map[string]*websocket.Client),
		clientSubs:           make(map[string]map[string]struct{}),
		maxChannelsPerClient: DefaultMaxChannelsPerClient,
		maxChannelNameLength: nameCap,
		authorizer:           denyAllChannelAuthorizer,
	}
	client := createTestClient("c1")
	long := strings.Repeat("x", nameCap+1)

	err := d.handleSubscribe(client, websocket.Message{
		Type: "subscribe",
		Data: map[string]interface{}{"channel": long},
	})
	if err == nil {
		t.Fatal("expected error for oversized channel name, got nil")
	}
	if !strings.Contains(err.Error(), "channel name exceeds") {
		t.Fatalf("expected length-cap error, got %v", err)
	}
	if _, exists := d.channels[long]; exists {
		t.Error("oversized channel name leaked into channels map")
	}
	if subs, ok := d.clientSubs[client.ID]; ok && len(subs) > 0 {
		t.Errorf("oversized channel name leaked into clientSubs: %v", subs)
	}
}

// TestHandleSubscribe_ChannelCapSurfacesAsError covers the integration
// between handleSubscribe and the underlying Subscribe cap: a client that
// has exhausted its budget gets ErrChannelLimit surfaced as the message
// handler error and the rejected channel is NOT confirmed.
func TestHandleSubscribe_ChannelCapSurfacesAsError(t *testing.T) {
	d := &WebSocketDriver{
		channels:             make(map[string]map[string]*websocket.Client),
		clientSubs:           make(map[string]map[string]struct{}),
		maxChannelsPerClient: 1,
		maxChannelNameLength: DefaultMaxChannelNameLength,
		authorizer:           denyAllChannelAuthorizer,
	}
	client := createTestClient("c1")

	// First public-channel subscribe should succeed and consume the budget.
	if err := d.handleSubscribe(client, websocket.Message{
		Type: "subscribe",
		Data: map[string]interface{}{"channel": "public-a"},
	}); err != nil {
		t.Fatalf("first subscribe: %v", err)
	}
	// Drain the subscription_succeeded confirmation so we do not block
	// the second send-confirmation path.
	<-client.Send

	// Second distinct subscribe must be rejected.
	err := d.handleSubscribe(client, websocket.Message{
		Type: "subscribe",
		Data: map[string]interface{}{"channel": "public-b"},
	})
	if err == nil {
		t.Fatal("expected channel cap error, got nil")
	}
	if !errors.Is(err, ErrChannelLimit) {
		t.Fatalf("expected ErrChannelLimit, got %v", err)
	}
}
