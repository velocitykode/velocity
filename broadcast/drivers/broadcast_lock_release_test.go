package drivers

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/websocket"
)

// TestBroadcast_SlowClientDoesNotBlockSubscribe is the M-28 regression: under
// WithBlockingSend, a slow subscriber must NOT hold the channel-map lock while
// the send is in flight. A concurrent Subscribe on the same channel must
// proceed without waiting for the slow write to drain or time out.
func TestBroadcast_SlowClientDoesNotBlockSubscribe(t *testing.T) {
	d := &WebSocketDriver{
		channels:       make(map[string]map[string]*websocket.Client),
		blockingSendTO: 500 * time.Millisecond,
	}

	// "slow" has a full Send buffer that we will never drain. With the
	// blocking-send timeout set, the broadcast will park on this client for
	// the full 500 ms before dropping. Before M-28 this parked the channel
	// map's RLock for that whole window.
	slow := &websocket.Client{
		ID:       "slow",
		Send:     make(chan websocket.Message, 1),
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}
	slow.Send <- websocket.Message{Type: "filler"} // fill, never drain

	d.channels["c"] = map[string]*websocket.Client{"slow": slow}

	// Kick off the broadcast in the background. It is going to spend ~500 ms
	// trying to deliver to slow.
	broadcastDone := make(chan struct{})
	go func() {
		_ = d.Broadcast([]string{"c"}, "evt", "data")
		close(broadcastDone)
	}()

	// Give the broadcast a moment to enter the send path and (under the old
	// code) take its RLock-held write blocking window.
	time.Sleep(20 * time.Millisecond)

	// Now run a concurrent Subscribe on the same channel. It needs the
	// channel-map write lock. Pre-M-28 this would wait for the broadcast's
	// RLock to drop, i.e. for the entire 500 ms slow-client timeout.
	subscribed := make(chan time.Duration, 1)
	newClient := &websocket.Client{
		ID:       "fresh",
		Send:     make(chan websocket.Message, 1),
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}
	go func() {
		start := time.Now()
		_ = d.Subscribe("c", newClient)
		subscribed <- time.Since(start)
	}()

	select {
	case took := <-subscribed:
		// Generous bound: should be well under 100 ms, but allow slack for
		// CI scheduling jitter. The pre-fix code would clock in at ~500 ms.
		if took >= 200*time.Millisecond {
			t.Errorf("Subscribe blocked %v - slow-client RLock was held across the write", took)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Subscribe did not complete within 300 ms - lock still held by Broadcast")
	}

	// Cleanup: wait for the broadcast to finish so we do not leak the
	// goroutine across tests.
	<-broadcastDone
}

// TestBroadcast_FastSubscribersStillDeliveredEventually pins that under the
// snapshot-then-send pattern, a fast subscriber on the same channel as a
// slow one still receives the broadcast within roughly one blockingSendTO
// window. Fan-out is sequential per the M-28 fix scope (snapshot then
// iterate); map iteration order is non-deterministic so we tolerate either
// ordering, but the overall broadcast must complete in ~one timeout window
// rather than two.
func TestBroadcast_FastSubscribersStillDeliveredEventually(t *testing.T) {
	d := &WebSocketDriver{
		channels:       make(map[string]map[string]*websocket.Client),
		blockingSendTO: 150 * time.Millisecond,
	}

	slow := &websocket.Client{
		ID:       "slow",
		Send:     make(chan websocket.Message, 1),
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}
	slow.Send <- websocket.Message{Type: "filler"}

	fast := &websocket.Client{
		ID:       "fast",
		Send:     make(chan websocket.Message, 4),
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}

	d.channels["c"] = map[string]*websocket.Client{
		"slow": slow,
		"fast": fast,
	}

	broadcastDone := make(chan struct{})
	go func() {
		_ = d.Broadcast([]string{"c"}, "evt", "payload")
		close(broadcastDone)
	}()

	// Fast client must receive the broadcast eventually. The slow client
	// holds the send path for at most one blockingSendTO window; with map
	// iteration order non-deterministic we allow up to ~3x that window for
	// the fast client to drain.
	select {
	case msg := <-fast.Send:
		if msg.Type != "evt" {
			t.Errorf("fast client got %q, want %q", msg.Type, "evt")
		}
		if msg.Data != "payload" {
			t.Errorf("fast client got data %v, want %q", msg.Data, "payload")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("fast subscriber never received the broadcast")
	}

	<-broadcastDone

	// The slow client's filler is still in its buffer; the broadcast should
	// have eventually dropped its evt frame after the timeout.
	if got := d.DroppedCount(); got != 1 {
		t.Errorf("DroppedCount = %d, want 1 (slow client should have timed out)", got)
	}
}

// TestBroadcast_LockReleasedBeforeSend pins the invariant explicitly: from
// inside d.sendOrDrop the channel-map mutex must be unheld so the send path
// can never contend with a Subscribe/Unsubscribe. We instrument by hijacking
// the onDrop callback to fire a concurrent Unsubscribe; if the RLock is still
// held the Unsubscribe write lock will deadlock and the test times out.
func TestBroadcast_LockReleasedBeforeSend(t *testing.T) {
	var unsubFinished atomic.Bool

	d := &WebSocketDriver{
		channels:       make(map[string]map[string]*websocket.Client),
		blockingSendTO: 50 * time.Millisecond,
	}

	d.onDrop = func(_, _, _ string) {
		// Inside the send path. If snapshotTargets failed to release the
		// RLock we cannot acquire the write lock here and the test will
		// time out.
		_ = d.Unsubscribe("c", "slow")
		unsubFinished.Store(true)
	}

	slow := &websocket.Client{
		ID:       "slow",
		Send:     make(chan websocket.Message, 1),
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}
	slow.Send <- websocket.Message{Type: "filler"}

	d.channels["c"] = map[string]*websocket.Client{"slow": slow}

	done := make(chan struct{})
	go func() {
		_ = d.Broadcast([]string{"c"}, "evt", "data")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Broadcast deadlocked - lock not released before send")
	}

	if !unsubFinished.Load() {
		t.Error("Unsubscribe never ran from inside onDrop - send-path lock contention")
	}
}
