package drivers

import (
	"sync"
	"testing"
	"time"

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

	client := &websocket.Client{
		ID:       "slow",
		Send:     make(chan websocket.Message, 1),
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}
	client.Send <- websocket.Message{Type: "filler", Data: nil} // fill buffer

	d.channels["c"] = map[string]*websocket.Client{"slow": client}

	if err := d.Broadcast([]string{"c"}, "evt", "data"); err != nil {
		t.Fatalf("Broadcast: %v", err)
	}

	if got := d.DroppedCount(); got != 1 {
		t.Fatalf("DroppedCount = %d, want 1", got)
	}

	onDropMu.Lock()
	defer onDropMu.Unlock()
	if len(drops) != 1 || drops[0] != "slow:c:evt" {
		t.Fatalf("onDrop = %v, want [slow:c:evt]", drops)
	}
}

// TestWithBlockingSend_BlocksUntilDrain covers Task 2: when a blocking timeout
// is configured, Broadcast blocks until either the buffer drains or the
// deadline expires.
func TestWithBlockingSend_BlocksUntilDrain(t *testing.T) {
	d := &WebSocketDriver{
		channels:       make(map[string]map[string]*websocket.Client),
		blockingSendTO: 200 * time.Millisecond,
	}

	client := &websocket.Client{
		ID:       "busy",
		Send:     make(chan websocket.Message, 1),
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}
	client.Send <- websocket.Message{Type: "filler"} // fill

	d.channels["c"] = map[string]*websocket.Client{"busy": client}

	// Drain the filler after a short delay so the blocking send can succeed.
	go func() {
		time.Sleep(30 * time.Millisecond)
		<-client.Send
	}()

	start := time.Now()
	if err := d.Broadcast([]string{"c"}, "evt", "data"); err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed >= 200*time.Millisecond {
		t.Fatalf("Broadcast blocked for %v — should have drained before timeout", elapsed)
	}
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

	client := &websocket.Client{
		ID:       "frozen",
		Send:     make(chan websocket.Message, 1),
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}
	client.Send <- websocket.Message{Type: "filler"} // fill and never drain

	d.channels["c"] = map[string]*websocket.Client{"frozen": client}

	if err := d.Broadcast([]string{"c"}, "evt", "data"); err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	if got := d.DroppedCount(); got != 1 {
		t.Fatalf("DroppedCount = %d, want 1", got)
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
