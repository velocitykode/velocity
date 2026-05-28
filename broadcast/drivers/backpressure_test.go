package drivers

import (
	"context"
	"errors"
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

// TestBroadcastCtx_CancellationAbortsBlockingSend is the round-4 regression
// test: under WithBlockingSend the per-send wait window must honour caller
// ctx cancellation, not just the per-send timer. Before sendOrDrop became
// ctx-aware, a cancelled BroadcastCtx could still wait the full
// blockingSendTO per slow client before returning; this test fails on the
// pre-fix code because the elapsed time would equal the configured timeout
// instead of the much shorter cancellation delay.
func TestBroadcastCtx_CancellationAbortsBlockingSend(t *testing.T) {
	const (
		blockTO     = 500 * time.Millisecond
		cancelAt    = 30 * time.Millisecond
		maxAllowed  = 200 * time.Millisecond // generous bound; well under blockTO
		clientCount = 5
	)

	d := &WebSocketDriver{
		channels:       make(map[string]map[string]*websocket.Client),
		blockingSendTO: blockTO,
	}

	// Several slow clients, each with a 1-buffer channel filled and never
	// drained, so every per-client send hits the blocking-wait path.
	d.channels["c"] = make(map[string]*websocket.Client)
	for i := 0; i < clientCount; i++ {
		id := "slow-" + string(rune('a'+i))
		c := &websocket.Client{
			ID:       id,
			Send:     make(chan websocket.Message, 1),
			Groups:   make(map[string]bool),
			Metadata: make(map[string]interface{}),
		}
		c.Send <- websocket.Message{Type: "filler"} // fill and never drain
		d.channels["c"][id] = c
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay so the first per-client send is
	// definitely mid-wait when cancellation lands.
	go func() {
		time.Sleep(cancelAt)
		cancel()
	}()

	start := time.Now()
	err := d.BroadcastCtx(ctx, []string{"c"}, "evt", "data")
	elapsed := time.Since(start)

	if elapsed >= maxAllowed {
		t.Fatalf("BroadcastCtx blocked for %v after ctx cancellation; want < %v (blockingSendTO=%v)",
			elapsed, maxAllowed, blockTO)
	}
	if elapsed >= blockTO {
		t.Fatalf("BroadcastCtx blocked the full blockingSendTO (%v); ctx cancellation was not honoured inside sendOrDrop", blockTO)
	}
	// Round-5: a Ctx-suffixed API must surface ctx.Err() when cancellation
	// aborted the work. Before the post-loop cancellation check landed,
	// BroadcastCtx returned nil here because sendOrDrop swallowed the
	// cancellation as a drop and the inner pre-check only fired between
	// targets (so a cancellation observed mid-send on the final target
	// would never gate any subsequent iteration).
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("BroadcastCtx returned err=%v after ctx.Cancel; want errors.Is(err, context.Canceled)", err)
	}
}

// TestBroadcastExceptCtx_CancellationReturnsErr exercises the same
// post-loop ctx.Err() surfacing path on the BroadcastExcept side. The
// regression covers the symmetry between the two entry points: a
// single-target broadcast that gets cancelled mid-send on the only
// target must return ctx.Err() even though no further iteration would
// re-observe the cancellation.
func TestBroadcastExceptCtx_CancellationReturnsErr(t *testing.T) {
	d := &WebSocketDriver{
		channels:       make(map[string]map[string]*websocket.Client),
		blockingSendTO: 500 * time.Millisecond,
	}

	c := &websocket.Client{
		ID:       "only-slow",
		Send:     make(chan websocket.Message, 1),
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}
	c.Send <- websocket.Message{Type: "filler"} // fill and never drain
	d.channels["c"] = map[string]*websocket.Client{"only-slow": c}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	// excludeSocketID empty so the single client is targeted; the loop
	// runs exactly once, sendOrDrop blocks, ctx cancels mid-wait. Only
	// the post-loop ctx.Err() check can surface the cancellation.
	err := d.BroadcastExceptCtx(ctx, []string{"c"}, "evt", "data", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("BroadcastExceptCtx returned err=%v; want errors.Is(err, context.Canceled)", err)
	}
}

// TestSendOrDrop_NilCtxKeepsOldBehaviour asserts the nil-ctx defensive guard
// inside sendOrDrop still routes through the timer-only path (the
// pre-ctx-aware behaviour). Tests that bypass BroadcastCtx and reach
// sendOrDrop directly with a nil ctx must keep working.
func TestSendOrDrop_NilCtxKeepsOldBehaviour(t *testing.T) {
	d := &WebSocketDriver{
		channels:       make(map[string]map[string]*websocket.Client),
		blockingSendTO: 20 * time.Millisecond,
	}

	c := &websocket.Client{
		ID:       "x",
		Send:     make(chan websocket.Message, 1),
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}
	c.Send <- websocket.Message{Type: "filler"}
	d.channels["c"] = map[string]*websocket.Client{"x": c}

	start := time.Now()
	//nolint:staticcheck // intentional nil ctx exercising the defensive guard
	d.sendOrDrop(nil, c, "c", "evt", "data")
	elapsed := time.Since(start)

	if elapsed < 15*time.Millisecond {
		t.Fatalf("sendOrDrop returned in %v with nil ctx; want at least the blockingSendTO (20ms)", elapsed)
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
