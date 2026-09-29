package drivers

import (
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/websocket"
)

// fullClient registers, on channel "c", a connected client whose send
// queue is full, so a broadcast to it is dropped.
func fullClient(t *testing.T, d *WebSocketDriver) *websocket.Client {
	c, _ := newClientHost(t).full(t)
	d.channels["c"] = map[string]*websocket.Client{c.ID: c}
	return c
}

// The onDrop callback and the logger that writes the drop line are user
// code run on the broadcast path. One that panics is contained where it is
// called: the drop is counted once, the callback runs once, one line
// reports the panic, the healthy client stays registered, and Broadcast
// returns normally. A full queue never purges the client.
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
			assertContainedDrop(t, d, fullClient(t, d))
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
			assertContainedDrop(t, d, fullClient(t, d))
			if n := code.Calls(); n != 1 {
				t.Errorf("logger Warn calls = %d, want 1", n)
			}
			if n := out.Count("WARN", "velocity/broadcast: dropped message"); n != 1 {
				t.Errorf("fallback drop lines = %d, want 1:\n%s", n, out.String())
			}
		})
	}
}

func assertContainedDrop(t *testing.T, d *WebSocketDriver, c *websocket.Client) {
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
	_, registered := d.channels["c"][c.ID]
	d.mu.RUnlock()
	if !registered {
		t.Error("the healthy client was purged")
	}
}

// closedClient registers, on channel "c", a client that has disconnected
// but that the driver has not purged yet: the state a broadcast snapshot
// taken before the purge holds.
func closedClient(t *testing.T, d *WebSocketDriver) {
	c := newClientHost(t).closed(t)
	d.channels["c"] = map[string]*websocket.Client{c.ID: c}
}

// A send to a client that disconnected after the snapshot purges it and
// reports the drop once, even when the drop report is hostile: a panicking
// onDrop or logger is contained, and one that broadcasts again finds the
// client already purged instead of reaching it again.
func TestBroadcast_ClosedSendWithHostileDropReport(t *testing.T) {
	cases := []struct {
		name string
		arm  func(d *WebSocketDriver, code *hostile.Code)
		mode hostile.Mode
		// line is the line the report writes, "" for none.
		line string
	}{
		{"onDrop panics", func(d *WebSocketDriver, code *hostile.Code) {
			d.onDrop = func(string, string, string) { code.Run() }
		}, hostile.Panic, "velocity/broadcast: onDrop callback panicked"},
		{"logger panics", func(d *WebSocketDriver, code *hostile.Code) {
			d.SetLogger(hostile.NewLogger(code, hostile.Warn))
		}, hostile.Panic, "velocity/broadcast: dropped message"},
		{"onDrop broadcasts again", func(d *WebSocketDriver, code *hostile.Code) {
			d.onDrop = func(string, string, string) { code.Run() }
		}, hostile.Reenter, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := fallbacklogtest.Capture(t)
			d := &WebSocketDriver{channels: make(map[string]map[string]*websocket.Client)}
			code := hostile.New(t, c.mode, func() {
				_ = d.Broadcast([]string{"c"}, "again", nil)
			})
			c.arm(d, code)
			closedClient(t, d)

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
			if n := code.Calls(); n != 1 {
				t.Errorf("hostile calls = %d, want 1", n)
			}
			d.mu.RLock()
			_, still := d.channels["c"]
			d.mu.RUnlock()
			if still {
				t.Error("the closed client was not purged")
			}
			if c.line != "" {
				if n := out.Count("WARN", c.line); n != 1 {
					t.Errorf("%q lines = %d, want 1:\n%s", c.line, n, out.String())
				}
			}
		})
	}
}
