package mail

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

// The log driver writes each message's line through its logger, user
// code. For every entry point a logger may call back into (GetLog,
// ClearLog, SetLogger, Send), a logger that panics, blocks, or makes that
// call must not break Send or deadlock the driver, and the driver keeps
// working afterwards.
func TestLogDriver_HostileLogger(t *testing.T) {
	msg := func() *Message {
		return NewMessage().From("a@example.com", "A").To("b@example.com").Subject("s").Body("b")
	}
	entries := map[string]func(d *LogDriver){
		"GetLog":    func(d *LogDriver) { _ = d.GetLog() },
		"ClearLog":  func(d *LogDriver) { d.ClearLog() },
		"SetLogger": func(d *LogDriver) { d.SetLogger(nil) },
		"Send":      func(d *LogDriver) { _ = d.Send(context.Background(), msg()) },
	}
	for _, mode := range hostile.Modes() {
		for name, entry := range entries {
			t.Run(mode.String()+"/"+name, func(t *testing.T) {
				fallbacklogtest.Capture(t)
				d := NewLogDriver()
				code := hostile.New(t, mode, func() { entry(d) })
				d.SetLogger(hostile.NewLogger(code))
				send := func() {
					if err := d.Send(context.Background(), msg()); err != nil {
						t.Errorf("Send: %v", err)
					}
				}
				if mode == hostile.Block {
					go send()
					if !code.AwaitEntered(t) {
						return
					}
					if name == "Send" {
						// The second Send runs the same blocking logger:
						// it must reach it, not wait on the driver.
						go entry(d)
						hostile.Eventually(t, hostile.Deadline, "the second Send reaching the logger", func() bool {
							return code.Calls() >= 2
						})
					} else {
						hostile.Within(t, hostile.Deadline, func() { entry(d) })
					}
				} else if p := hostile.Within(t, hostile.Deadline, send); p != nil {
					t.Fatalf("Send panicked: %v", p)
				}
				code.Release()
				code.Disarm()
				hostile.Within(t, hostile.Deadline, func() {
					d.ClearLog()
					send()
					if n := len(d.GetLog()); n != 1 {
						t.Errorf("retained %d entries after a retry, want 1", n)
					}
				})
			})
		}
	}
}
