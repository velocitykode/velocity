package drivers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gorillaws "github.com/gorilla/websocket"
	"github.com/velocitykode/velocity/websocket"
)

// originHeader mirrors websocket/testing_test.go: gorilla's default dialer
// omits Origin, which the server's post-H-24 same-origin check rejects.
func originHeader(httpURL string) http.Header {
	h := http.Header{}
	h.Set("Origin", httpURL)
	return h
}

// allowingAuthorizer returns true for every private/presence subscribe so the
// disconnect path tests can use any channel name without wiring an HMAC
// verifier (which is exercised in autowire_test.go).
func allowingAuthorizer(*websocket.Client, string) bool { return true }

// TestBroadcast_NoPanicAfterDisconnect (audit D-01, primary defence):
// drives a real gorilla WebSocket client through the driver, force-closes the
// connection mid-subscription, and asserts that a subsequent Broadcast on the
// (now stale) channel does NOT panic with `send on closed channel`. Repeats
// 100 times to surface any timing-sensitive variant.
func TestBroadcast_NoPanicAfterDisconnect(t *testing.T) {
	t.Parallel()

	driver := newRealDriver(t)
	defer driver.server.Shutdown(context.Background())

	ts := httptest.NewServer(http.HandlerFunc(driver.server.HandleConnection))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	const iterations = 100
	for i := 0; i < iterations; i++ {
		// Open a fresh connection and subscribe to a channel.
		ws, _, err := gorillaws.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
		if err != nil {
			t.Fatalf("iteration %d: dial: %v", i, err)
		}

		// Read welcome to make sure register has fully processed.
		var welcome websocket.Message
		if err := ws.ReadJSON(&welcome); err != nil {
			ws.Close()
			t.Fatalf("iteration %d: read welcome: %v", i, err)
		}
		if welcome.Type != "welcome" {
			ws.Close()
			t.Fatalf("iteration %d: unexpected first message %q", i, welcome.Type)
		}

		// Subscribe (channel does not need to be unique; the post-broadcast
		// state must be a no-op regardless).
		channel := "room"
		if err := ws.WriteJSON(websocket.Message{
			Type: "subscribe",
			Data: map[string]interface{}{"channel": channel},
		}); err != nil {
			ws.Close()
			t.Fatalf("iteration %d: write subscribe: %v", i, err)
		}

		// Drain the subscribe ACK so we know handleSubscribe has run.
		var ack websocket.Message
		if err := ws.ReadJSON(&ack); err != nil {
			ws.Close()
			t.Fatalf("iteration %d: read ack: %v", i, err)
		}

		// Force the client offline without an unsubscribe. This is the path
		// audit D-01 cared about: the broadcast driver retains the stale
		// *Client pointer until OnDisconnect fires.
		ws.Close()

		// Wait for the server-side teardown to complete. After this point the
		// purgeClient listener has fired AND close(client.Send) has happened
		// (handleUnregister fires listeners BEFORE close to make this safe).
		deadline := time.Now().Add(2 * time.Second)
		for {
			if !driverHasSubscribers(driver, channel) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("iteration %d: purgeClient did not run within 2s", i)
			}
			time.Sleep(2 * time.Millisecond)
		}

		// The actual assertion: broadcasting on the freshly-cleared channel
		// must not panic. Pre-fix this would crash with
		// `send on closed channel`.
		if err := driver.Broadcast([]string{channel}, "evt", "data"); err != nil {
			t.Fatalf("iteration %d: Broadcast: %v", i, err)
		}
	}
}

// TestBroadcast_DefensiveRecoverOnClosedSend (audit D-01, defensive layer):
// simulates the race window where a snapshot was taken just before
// purgeClient ran and close(client.Send) ran. We construct that state
// directly by inserting a client whose Send channel is already closed, then
// call Broadcast and assert (1) it does not panic, (2) the stale pointer is
// purged by the recover path, and (3) the dropped count is incremented.
func TestBroadcast_DefensiveRecoverOnClosedSend(t *testing.T) {
	t.Parallel()

	d := &WebSocketDriver{
		channels: make(map[string]map[string]*websocket.Client),
	}

	// Build a client whose Send channel is closed: sending to it always
	// panics with `send on closed channel`.
	closed := make(chan websocket.Message)
	close(closed)
	c := &websocket.Client{
		ID:       "ghost",
		Send:     closed,
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}
	d.channels["c"] = map[string]*websocket.Client{"ghost": c}

	if err := d.Broadcast([]string{"c"}, "evt", "data"); err != nil {
		t.Fatalf("Broadcast: %v", err)
	}

	if got := d.DroppedCount(); got != 1 {
		t.Fatalf("DroppedCount = %d, want 1 (defensive recover should have counted the drop)", got)
	}

	// The defensive recover must have purged the stale pointer. After purge
	// the channel "c" is empty and removed from the map entirely.
	d.mu.RLock()
	_, exists := d.channels["c"]
	d.mu.RUnlock()
	if exists {
		t.Fatal("purgeClient was not invoked from sendOrDrop recover; channel still present")
	}

	// A repeat broadcast must also be safe and a no-op for state.
	if err := d.Broadcast([]string{"c"}, "evt", "data"); err != nil {
		t.Fatalf("Broadcast (repeat): %v", err)
	}
	if got := d.DroppedCount(); got != 1 {
		t.Fatalf("DroppedCount after no-op broadcast = %d, want 1", got)
	}
}

// TestBroadcast_DefensiveRecoverOnBlockingSend covers the blocking path of
// sendOrDrop: WithBlockingSend(timeout) routes through the timer-based
// select rather than the non-blocking default. The recover must catch the
// send-on-closed panic on this branch too.
func TestBroadcast_DefensiveRecoverOnBlockingSend(t *testing.T) {
	t.Parallel()

	d := &WebSocketDriver{
		channels:       make(map[string]map[string]*websocket.Client),
		blockingSendTO: 100 * time.Millisecond,
	}

	closed := make(chan websocket.Message)
	close(closed)
	c := &websocket.Client{
		ID:       "ghost-blocking",
		Send:     closed,
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}
	d.channels["b"] = map[string]*websocket.Client{"ghost-blocking": c}

	start := time.Now()
	if err := d.Broadcast([]string{"b"}, "evt", "data"); err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 100*time.Millisecond {
		// The send-on-closed panic must fire instantly, not after the
		// timeout. If we ever wait the full 100ms the recover is on the
		// wrong branch (or the panic is being eaten elsewhere).
		t.Fatalf("Broadcast blocked %v on closed Send; recover should have fired immediately", elapsed)
	}
	if got := d.DroppedCount(); got != 1 {
		t.Fatalf("DroppedCount = %d, want 1", got)
	}
}

// TestPurgeClient_RemovesFromAllChannels asserts that purgeClient walks every
// channel the client was subscribed to, not just one. This is the contract
// the OnDisconnect listener relies on.
func TestPurgeClient_RemovesFromAllChannels(t *testing.T) {
	t.Parallel()

	d := &WebSocketDriver{
		channels: make(map[string]map[string]*websocket.Client),
	}

	c1 := createTestClient("multi")
	c2 := createTestClient("other")

	d.channels["alpha"] = map[string]*websocket.Client{"multi": c1, "other": c2}
	d.channels["beta"] = map[string]*websocket.Client{"multi": c1}
	d.channels["gamma"] = map[string]*websocket.Client{"other": c2}

	d.purgeClient("multi")

	d.mu.RLock()
	defer d.mu.RUnlock()

	if _, ok := d.channels["alpha"]["multi"]; ok {
		t.Error("purgeClient did not remove from alpha")
	}
	if _, ok := d.channels["alpha"]["other"]; !ok {
		t.Error("purgeClient should not have touched other clients on alpha")
	}
	if _, ok := d.channels["beta"]; ok {
		t.Error("purgeClient should have removed beta (now empty)")
	}
	if _, ok := d.channels["gamma"]["other"]; !ok {
		t.Error("purgeClient should not have touched unrelated channel gamma")
	}
}

// TestPurgeClient_EmptyID is a guard against unconditional walks: passing
// an empty client ID must short-circuit (otherwise a misconfigured listener
// could clear arbitrary subscriptions).
func TestPurgeClient_EmptyID(t *testing.T) {
	t.Parallel()

	d := &WebSocketDriver{
		channels: map[string]map[string]*websocket.Client{
			"c": {"a": createTestClient("a")},
		},
	}
	d.purgeClient("")
	d.mu.RLock()
	defer d.mu.RUnlock()
	if _, ok := d.channels["c"]["a"]; !ok {
		t.Fatal("purgeClient(\"\") must be a no-op")
	}
}

// TestServer_AddOnDisconnect_FiresBeforeClose verifies the server-side
// contract that the broadcast driver depends on: disconnect listeners fire
// while client.Send is still a live channel.
func TestServer_AddOnDisconnect_FiresBeforeClose(t *testing.T) {
	t.Parallel()

	s := websocket.New(websocket.DefaultConfig())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Shutdown(context.Background())

	var sendStillOpen atomic.Bool
	done := make(chan struct{})
	s.AddOnDisconnect(func(c *websocket.Client) {
		// Probe whether Send is still open. A non-blocking send into a
		// buffered channel succeeds when open; a closed channel panics.
		defer func() {
			if r := recover(); r != nil {
				sendStillOpen.Store(false)
			}
			close(done)
		}()
		select {
		case c.Send <- websocket.Message{Type: "probe"}:
			sendStillOpen.Store(true)
		default:
			// Buffer was full but channel was open: still counts as "open".
			sendStillOpen.Store(true)
		}
	})

	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := gorillaws.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	// Drain welcome and disconnect.
	var welcome websocket.Message
	_ = ws.ReadJSON(&welcome)
	ws.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("disconnect listener never fired")
	}

	if !sendStillOpen.Load() {
		t.Fatal("listener observed closed Send channel; close must happen AFTER listeners fire")
	}
}

// --- helpers ---

func newRealDriver(t *testing.T) *WebSocketDriver {
	t.Helper()
	cfg := websocket.DefaultConfig()
	// Use any-origin so we do not need to set up the same-origin gate per
	// dial. Origin still has to be a non-empty http URL in tests.
	cfg.AllowedOrigins = []string{"*"}
	d := NewWebSocketDriver(cfg)
	d.SetAuthorizer(allowingAuthorizer)
	return d
}

func driverHasSubscribers(d *WebSocketDriver, channel string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.channels[channel]) > 0
}
