package drivers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gorillaws "github.com/gorilla/websocket"
	"github.com/velocitykode/velocity/internal/hostile"
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

// TestBroadcast_ClosedClientPurgedOnSend (audit D-01): a snapshot taken
// before the disconnect purge holds a client that has since closed. The
// send reports it gone, at once on the blocking path too, and the driver
// purges it and counts one drop; a repeat broadcast is a no-op.
func TestBroadcast_ClosedClientPurgedOnSend(t *testing.T) {
	t.Parallel()

	for _, blocking := range []time.Duration{0, time.Hour} {
		name := "non-blocking"
		if blocking > 0 {
			name = "blocking"
		}
		t.Run(name, func(t *testing.T) {
			d := &WebSocketDriver{
				channels:       make(map[string]map[string]*websocket.Client),
				blockingSendTO: blocking,
			}
			c := newClientHost(t).closed(t)
			d.channels["c"] = map[string]*websocket.Client{c.ID: c}

			hostile.Within(t, hostile.Deadline, func() {
				if err := d.Broadcast([]string{"c"}, "evt", "data"); err != nil {
					t.Errorf("Broadcast: %v", err)
				}
			})
			if got := d.DroppedCount(); got != 1 {
				t.Fatalf("DroppedCount = %d, want 1", got)
			}
			d.mu.RLock()
			_, exists := d.channels["c"]
			d.mu.RUnlock()
			if exists {
				t.Fatal("the closed client was not purged")
			}

			// A repeat broadcast must also be safe and a no-op for state.
			if err := d.Broadcast([]string{"c"}, "evt", "data"); err != nil {
				t.Fatalf("Broadcast (repeat): %v", err)
			}
			if got := d.DroppedCount(); got != 1 {
				t.Fatalf("DroppedCount after no-op broadcast = %d, want 1", got)
			}
		})
	}
}

// A purge removes only the client instance it is given: an entry that
// holds another client under the same ID stays, and so does that entry's
// share of the ID's subscription budget; the purged instance's share is
// given back.
func TestPurgeClient_OnlyThatInstance(t *testing.T) {
	t.Parallel()

	stale := &websocket.Client{ID: "same"}
	current := &websocket.Client{ID: "same"}
	d := &WebSocketDriver{
		channels: map[string]map[string]*websocket.Client{
			"old": {"same": stale},
			"new": {"same": current},
		},
		clientSubs: map[string]map[string]struct{}{"same": {"old": {}, "new": {}}},
	}
	d.purgeClient(stale)

	d.mu.RLock()
	defer d.mu.RUnlock()
	if _, ok := d.channels["old"]; ok {
		t.Error("the stale instance was not purged")
	}
	if d.channels["new"]["same"] != current {
		t.Error("the current instance under the same ID was purged")
	}
	subs, ok := d.clientSubs["same"]
	if !ok {
		t.Fatal("the ID's subscription budget was dropped while another instance holds it")
	}
	if _, ok := subs["new"]; !ok {
		t.Error("the current instance's subscription was dropped from the budget")
	}
	if _, ok := subs["old"]; ok {
		t.Error("the purged instance's subscription still counts against the budget")
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

	d.purgeClient(c1)

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
	d.purgeClient(&websocket.Client{})
	d.purgeClient(nil)
	d.mu.RLock()
	defer d.mu.RUnlock()
	if _, ok := d.channels["c"]["a"]; !ok {
		t.Fatal("purging a client with no ID must be a no-op")
	}
}

// TestServer_AddOnDisconnect_FiresOnUnregister verifies the server-side
// contract the broadcast driver depends on: a disconnect listener fires
// for a client that disconnects, with the client itself, and once the
// server is done with it the client takes no more sends. A send the
// listener makes may already report the client gone; it never panics.
func TestServer_AddOnDisconnect_FiresOnUnregister(t *testing.T) {
	t.Parallel()

	h := newClientHost(t)
	var fired atomic.Pointer[websocket.Client]
	done := make(chan struct{})
	h.server.AddOnDisconnect(func(c *websocket.Client) {
		defer close(done)
		_ = c.SendMessage(websocket.Message{Type: "probe"})
		fired.Store(c)
	})
	c, ws := h.connect(t)
	_ = ws.Close()

	hostile.Within(t, hostile.Deadline, func() { <-done })
	if fired.Load() != c {
		t.Fatal("the listener was not handed the disconnected client")
	}
	hostile.Eventually(t, hostile.Deadline, "the client closed", func() bool {
		return errors.Is(c.SendMessage(websocket.Message{Type: "late"}), websocket.ErrClientNotFound)
	})
}

// A broadcast whose ctx ends while a blocking send waits on a full queue
// returns ctx's error: the waiting message counts as the one drop, and the
// caller's cancellation is not mistaken for the send's own timeout. The
// ctx is cancelled only once the send is waiting: its first Done call is
// the blocking send deriving its own timeout from it.
func TestBroadcastCtx_CancellationEndsABlockingSend(t *testing.T) {
	t.Parallel()

	d := &WebSocketDriver{
		channels:       make(map[string]map[string]*websocket.Client),
		blockingSendTO: time.Hour,
	}
	c, _ := newClientHost(t).full(t)
	d.channels["c"] = map[string]*websocket.Client{c.ID: c}

	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &doneProbe{Context: parent, waiting: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- d.BroadcastCtx(ctx, []string{"c"}, "evt", "data") }()
	select {
	case <-ctx.waiting:
	case err := <-done:
		t.Fatalf("BroadcastCtx returned %v before its send waited", err)
	case <-time.After(hostile.Deadline):
		t.Fatal("the blocking send never waited on the ctx")
	}
	cancel()
	var err error
	select {
	case err = <-done:
	case <-time.After(hostile.Deadline):
		t.Fatal("BroadcastCtx did not return after its ctx was cancelled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("BroadcastCtx = %v, want the caller's cancellation", err)
	}
	if got := d.DroppedCount(); got != 1 {
		t.Errorf("DroppedCount = %d, want exactly the one waiting message", got)
	}
	d.mu.RLock()
	_, registered := d.channels["c"][c.ID]
	d.mu.RUnlock()
	if !registered {
		t.Error("a client whose send was cancelled was purged")
	}
}

// doneProbe is a context that closes waiting at its first Done call.
type doneProbe struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
}

func (p *doneProbe) Done() <-chan struct{} {
	p.once.Do(func() { close(p.waiting) })
	return p.Context.Done()
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
