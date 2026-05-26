package drivers

import (
	"strings"
	"testing"

	"github.com/velocitykode/velocity/websocket"
)

// TestGetClients_OpaqueShape pins the wire-format contract for opaque
// per-channel identifiers: each entry is exactly 16 hex characters.
func TestGetClients_OpaqueShape(t *testing.T) {
	d := &WebSocketDriver{
		channels: make(map[string]map[string]*websocket.Client),
	}
	d.channels["presence-room.1"] = map[string]*websocket.Client{
		"sock-alice":   createTestClient("sock-alice"),
		"sock-bob":     createTestClient("sock-bob"),
		"sock-charlie": createTestClient("sock-charlie"),
	}

	ids := d.GetClients("presence-room.1")
	if len(ids) != 3 {
		t.Fatalf("len(ids) = %d, want 3", len(ids))
	}
	for _, id := range ids {
		if len(id) != 16 {
			t.Errorf("id %q has length %d, want 16", id, len(id))
		}
		for _, r := range id {
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
			if !isHex {
				t.Errorf("id %q has non-hex rune %q", id, r)
				break
			}
		}
	}
}

// TestGetClients_NoRawSocketIDsOnWire is the central M-27 regression: the
// internal socket IDs (per-connection nonces) must NEVER appear in the
// payload returned by GetClients.
func TestGetClients_NoRawSocketIDsOnWire(t *testing.T) {
	d := &WebSocketDriver{
		channels: make(map[string]map[string]*websocket.Client),
	}
	rawIDs := []string{
		"sock-alice",
		"sock-bob",
		"sock-charlie",
		"a-very-long-internal-socket-id-1234567890",
	}
	d.channels["presence-room.1"] = make(map[string]*websocket.Client)
	for _, id := range rawIDs {
		d.channels["presence-room.1"][id] = createTestClient(id)
	}

	got := d.GetClients("presence-room.1")
	for _, raw := range rawIDs {
		for _, opaque := range got {
			if opaque == raw {
				t.Errorf("opaque list leaked raw socket id %q", raw)
			}
			if strings.Contains(opaque, raw) {
				t.Errorf("opaque id %q embeds raw socket id %q", opaque, raw)
			}
		}
	}
}

// TestGetClients_StablePerSubscription pins that successive GetClients calls
// during the same subscription return the same opaque id for the same socket.
// Without this clients could not correlate "user joined" with subsequent
// "user-typing" events in a presence channel.
func TestGetClients_StablePerSubscription(t *testing.T) {
	d := &WebSocketDriver{
		channels: make(map[string]map[string]*websocket.Client),
	}
	d.channels["presence-room.1"] = map[string]*websocket.Client{
		"sock-alice": createTestClient("sock-alice"),
	}

	first := d.GetClients("presence-room.1")
	second := d.GetClients("presence-room.1")

	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("expected one entry each call, got %d and %d", len(first), len(second))
	}
	if first[0] != second[0] {
		t.Errorf("opaque id changed between calls: %q -> %q", first[0], second[0])
	}
}

// TestGetClients_UnlinkableAcrossChannels pins the cross-channel privacy
// property: the same socket on two different channels must produce two
// different opaque ids. A tenant on one channel must not be able to spot the
// same socket on a sibling channel by matching opaque ids.
func TestGetClients_UnlinkableAcrossChannels(t *testing.T) {
	d := &WebSocketDriver{
		channels: make(map[string]map[string]*websocket.Client),
	}
	d.channels["presence-tenant-a.room"] = map[string]*websocket.Client{
		"sock-alice": createTestClient("sock-alice"),
	}
	d.channels["presence-tenant-b.room"] = map[string]*websocket.Client{
		"sock-alice": createTestClient("sock-alice"),
	}

	a := d.GetClients("presence-tenant-a.room")
	b := d.GetClients("presence-tenant-b.room")

	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("expected one entry per channel, got %d and %d", len(a), len(b))
	}
	if a[0] == b[0] {
		t.Errorf("same socket produced same opaque id across channels: %q", a[0])
	}
}

// TestGetClients_UnlinkableAcrossProcesses pins that two separate driver
// instances (modelling two server processes) hand out different opaque ids
// for the same logical (socket, channel) pair. The opaqueSeed is process-
// local and never persisted, so a tenant in one process cannot use the
// opaque id to recognise the same socket on another node of the same
// deployment.
func TestGetClients_UnlinkableAcrossProcesses(t *testing.T) {
	mk := func() *WebSocketDriver {
		d := &WebSocketDriver{
			channels: make(map[string]map[string]*websocket.Client),
		}
		d.channels["presence-room.1"] = map[string]*websocket.Client{
			"sock-alice": createTestClient("sock-alice"),
		}
		return d
	}

	a := mk().GetClients("presence-room.1")
	b := mk().GetClients("presence-room.1")

	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("expected one entry per driver, got %d and %d", len(a), len(b))
	}
	if a[0] == b[0] {
		t.Errorf("two driver instances produced the same opaque id %q for the same input - the seed is not process-local", a[0])
	}
}

// TestGetClients_UniqueWithinChannel pins that distinct sockets in the same
// channel get distinct opaque ids. With a 64-bit truncated HMAC space and
// realistic subscriber counts the probability of a collision is negligible;
// this is a sanity guard, not an absolute proof.
func TestGetClients_UniqueWithinChannel(t *testing.T) {
	d := &WebSocketDriver{
		channels: make(map[string]map[string]*websocket.Client),
	}
	d.channels["presence-room.1"] = make(map[string]*websocket.Client)
	for i := 0; i < 64; i++ {
		id := "sock-" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))
		d.channels["presence-room.1"][id] = createTestClient(id)
	}

	got := d.GetClients("presence-room.1")
	if len(got) != 64 {
		t.Fatalf("len(got) = %d, want 64", len(got))
	}
	seen := make(map[string]bool, len(got))
	for _, id := range got {
		if seen[id] {
			t.Errorf("opaque id %q appeared twice in the same channel", id)
		}
		seen[id] = true
	}
}
