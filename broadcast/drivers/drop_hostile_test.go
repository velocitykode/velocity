package drivers

import (
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/websocket"
)

// fullClient returns a registered client whose send buffer is full, so a
// broadcast to it is dropped.
func fullClient(d *WebSocketDriver) *websocket.Client {
	c := &websocket.Client{
		ID:       "slow",
		Send:     make(chan websocket.Message, 1),
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}
	c.Send <- websocket.Message{Type: "filler"}
	d.channels["c"] = map[string]*websocket.Client{"slow": c}
	return c
}

// The onDrop callback and the logger that writes the drop line are user
// code run on the broadcast path. One that panics is contained where it is
// called: the drop is counted once, the callback runs once, one line
// reports the panic, the healthy client stays registered, and Broadcast
// returns normally. The closed-channel recover is only for the send.
func TestBroadcast_HostileDropPathIsContained(t *testing.T) {
	for _, blocking := range []time.Duration{0, time.Millisecond} {
		name := "non-blocking"
		if blocking > 0 {
			name = "blocking"
		}
		t.Run(name+"/onDrop panics", func(t *testing.T) {
			out := fallbacklogtest.Capture(t)
			code := hostile.New(t, hostile.Panic, nil)
			d := &WebSocketDriver{
				channels:       make(map[string]map[string]*websocket.Client),
				blockingSendTO: blocking,
				onDrop:         func(string, string, string) { code.Run() },
			}
			fullClient(d)
			assertContainedDrop(t, d)
			if n := code.Calls(); n != 1 {
				t.Errorf("onDrop calls = %d, want 1", n)
			}
			if n := out.Count("WARN", "velocity/broadcast: onDrop callback panicked"); n != 1 {
				t.Errorf("panic lines = %d, want 1:\n%s", n, out.String())
			}
		})
		t.Run(name+"/logger panics", func(t *testing.T) {
			out := fallbacklogtest.Capture(t)
			code := hostile.New(t, hostile.Panic, nil)
			d := &WebSocketDriver{
				channels:       make(map[string]map[string]*websocket.Client),
				blockingSendTO: blocking,
			}
			d.SetLogger(hostile.NewLogger(code, hostile.Warn))
			fullClient(d)
			assertContainedDrop(t, d)
			if n := code.Calls(); n != 1 {
				t.Errorf("logger Warn calls = %d, want 1", n)
			}
			if n := out.Count("WARN", "velocity/broadcast: dropped message"); n != 1 {
				t.Errorf("fallback drop lines = %d, want 1:\n%s", n, out.String())
			}
		})
	}
}

func assertContainedDrop(t *testing.T, d *WebSocketDriver) {
	t.Helper()
	var err error
	if p := hostile.Within(t, hostile.Deadline, func() {
		err = d.Broadcast([]string{"c"}, "evt", "data")
	}); p != nil {
		t.Fatalf("Broadcast panicked: %v", p)
	}
	if err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	if got := d.DroppedCount(); got != 1 {
		t.Errorf("DroppedCount = %d, want 1", got)
	}
	d.mu.RLock()
	_, registered := d.channels["c"]["slow"]
	d.mu.RUnlock()
	if !registered {
		t.Error("the healthy client was purged")
	}
}

// closedClient returns a registered client whose Send channel is closed,
// so a broadcast to it panics on the send.
func closedClient(d *WebSocketDriver) {
	ch := make(chan websocket.Message)
	close(ch)
	d.channels["c"] = map[string]*websocket.Client{"ghost": {
		ID:       "ghost",
		Send:     ch,
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}}
}

// A send on a closed channel is recovered, the client purged and the drop
// reported once, even when the drop report is hostile: a panicking onDrop
// or logger is contained, and one that broadcasts again finds the client
// already purged instead of recursing into it. A panicking report writes a
// second line next to the closed-send one: they are two events.
func TestBroadcast_ClosedSendWithHostileDropReport(t *testing.T) {
	const closedLine = "velocity/broadcast: recovered from send-on-closed-channel; purged client"
	cases := []struct {
		name string
		arm  func(d *WebSocketDriver, code *hostile.Code)
		mode hostile.Mode
		// line is the second line the report writes, "" for none.
		line string
		// calls is how many times the hostile code runs: onDrop once, the
		// logger once per line (the drop and the closed send).
		calls int
	}{
		{"onDrop panics", func(d *WebSocketDriver, code *hostile.Code) {
			d.onDrop = func(string, string, string) { code.Run() }
		}, hostile.Panic, "velocity/broadcast: onDrop callback panicked", 1},
		{"logger panics", func(d *WebSocketDriver, code *hostile.Code) {
			d.SetLogger(hostile.NewLogger(code, hostile.Warn))
		}, hostile.Panic, "velocity/broadcast: dropped message", 2},
		{"onDrop broadcasts again", func(d *WebSocketDriver, code *hostile.Code) {
			d.onDrop = func(string, string, string) { code.Run() }
		}, hostile.Reenter, "", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := fallbacklogtest.Capture(t)
			d := &WebSocketDriver{channels: make(map[string]map[string]*websocket.Client)}
			code := hostile.New(t, c.mode, func() {
				_ = d.Broadcast([]string{"c"}, "again", nil)
			})
			c.arm(d, code)
			closedClient(d)

			var err error
			if p := hostile.Within(t, hostile.Deadline, func() {
				err = d.Broadcast([]string{"c"}, "evt", "data")
			}); p != nil {
				t.Fatalf("Broadcast panicked: %v", p)
			}
			if err != nil {
				t.Fatalf("Broadcast: %v", err)
			}
			if got := d.DroppedCount(); got != 1 {
				t.Errorf("DroppedCount = %d, want 1", got)
			}
			if n := code.Calls(); n != c.calls {
				t.Errorf("hostile calls = %d, want %d", n, c.calls)
			}
			d.mu.RLock()
			_, still := d.channels["c"]
			d.mu.RUnlock()
			if still {
				t.Error("the closed client was not purged")
			}
			if n := out.Count("WARN", closedLine); n != 1 {
				t.Errorf("closed-send lines = %d, want 1:\n%s", n, out.String())
			}
			if c.line != "" {
				if n := out.Count("WARN", c.line); n != 1 {
					t.Errorf("%q lines = %d, want 1:\n%s", c.line, n, out.String())
				}
			}
		})
	}
}
