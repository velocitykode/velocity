package drivers

import (
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/websocket"
)

// TestBroadcast_DroppedCount covers Task 2: dropped messages must increment
// the exported counter and trigger the onDrop callback.
func TestBroadcast_DroppedCount(t *testing.T) {
	var (
		onDropMu sync.Mutex
		drops    []string
	)

	d := &WebSocketDriver{
		channels: make(map[string]map[string]*websocket.Client),
		onDrop: func(clientID, channel, event string) {
			onDropMu.Lock()
			defer onDropMu.Unlock()
			drops = append(drops, clientID+":"+channel+":"+event)
		},
	}

	client, _ := newClientHost(t).full(t)
	d.channels["c"] = map[string]*websocket.Client{client.ID: client}

	if err := d.Broadcast([]string{"c"}, "evt", "data"); err != nil {
		t.Fatalf("Broadcast: %v", err)
	}

	if got := d.DroppedCount(); got != 1 {
		t.Fatalf("DroppedCount = %d, want 1", got)
	}

	onDropMu.Lock()
	defer onDropMu.Unlock()
	if want := client.ID + ":c:evt"; len(drops) != 1 || drops[0] != want {
		t.Fatalf("onDrop = %v, want [%s]", drops, want)
	}
}

// TestWithBlockingSend_BlocksUntilDrain covers Task 2: when a blocking timeout
// is configured, Broadcast waits for room in a full queue, and delivers
// once the client drains it.
func TestWithBlockingSend_BlocksUntilDrain(t *testing.T) {
	d := &WebSocketDriver{
		channels:       make(map[string]map[string]*websocket.Client),
		blockingSendTO: time.Minute,
	}

	client, peer := newClientHost(t).full(t)
	d.channels["c"] = map[string]*websocket.Client{client.ID: client}

	// The peer starts reading, which drains the queue so the waiting send
	// can enqueue.
	discard(peer)

	hostile.Within(t, hostile.Deadline, func() {
		if err := d.Broadcast([]string{"c"}, "evt", "data"); err != nil {
			t.Errorf("Broadcast: %v", err)
		}
	})
	if got := d.DroppedCount(); got != 0 {
		t.Fatalf("DroppedCount = %d, want 0", got)
	}
}

// TestWithBlockingSend_TimesOut verifies that when the buffer stays full for
// longer than the configured timeout, the message is dropped (not held
// forever).
func TestWithBlockingSend_TimesOut(t *testing.T) {
	d := &WebSocketDriver{
		channels:       make(map[string]map[string]*websocket.Client),
		blockingSendTO: 30 * time.Millisecond,
	}

	client, _ := newClientHost(t).full(t)
	d.channels["c"] = map[string]*websocket.Client{client.ID: client}

	if err := d.Broadcast([]string{"c"}, "evt", "data"); err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	if got := d.DroppedCount(); got != 1 {
		t.Fatalf("DroppedCount = %d, want 1", got)
	}
	d.mu.RLock()
	_, registered := d.channels["c"][client.ID]
	d.mu.RUnlock()
	if !registered {
		t.Error("a client whose send timed out was purged")
	}
}

// TestWithBlockingSend_Option asserts the WithBlockingSend option wires the
// timeout onto the driver.
func TestWithBlockingSend_Option(t *testing.T) {
	d := &WebSocketDriver{}
	WithBlockingSend(42 * time.Millisecond)(d)
	if d.blockingSendTO != 42*time.Millisecond {
		t.Fatalf("blockingSendTO = %v, want 42ms", d.blockingSendTO)
	}

	WithOnDrop(func(_, _, _ string) {})(d)
	if d.onDrop == nil {
		t.Fatal("onDrop was not installed")
	}
}
