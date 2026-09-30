package notification

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

// loggedChannel records the logger handed to it.
type loggedChannel struct {
	testChannel
	got contract.Logger
}

func (c *loggedChannel) SetLogger(l contract.Logger) { c.got = l }

// One channel whose SetLogger panics does not stop the handoff: every
// other channel is handed the manager's logger, SetLogger returns, and the
// panic is written as one warning.
func TestSetLogger_PanickingChannelDoesNotStopTheHandoff(t *testing.T) {
	fallbacklogtest.Capture(t)
	m := NewManager()
	good := []*loggedChannel{{}, {}, {}, {}}
	m.SetChannel("bad", &hostileChannel{code: hostile.New(t, hostile.Panic, nil)})
	for i, c := range good {
		m.SetChannel(string(rune('a'+i)), c)
	}
	rec := hostile.NewLogger(nil)
	if p := hostile.Within(t, hostile.Deadline, func() { m.SetLogger(rec) }); p != nil {
		t.Fatalf("SetLogger let the panic escape: %v", p)
	}
	for i, c := range good {
		if c.got != &m.logger {
			t.Errorf("channel %d was not handed the manager's logger", i)
		}
	}
	if n := len(rec.Lines()); n != 1 {
		t.Errorf("lines written = %d, want the one warning: %v", n, rec.Lines())
	}
}

// A channel set or created after the handoff began, whose SetLogger
// panics, is registered and returned; the panic does not escape.
func TestSetLogger_LaterChannelWithPanickingSetLogger(t *testing.T) {
	fallbacklogtest.Capture(t)
	m := NewManager()
	m.SetLogger(hostile.NewLogger(nil))
	code := hostile.New(t, hostile.Panic, nil)
	if p := hostile.Within(t, hostile.Deadline, func() { m.SetChannel("set", &hostileChannel{code: code}) }); p != nil {
		t.Fatalf("SetChannel let the panic escape: %v", p)
	}
	name := "handoff-created-" + t.Name()
	drivers.Register(name, func(context.Context, ChannelConfig) (Channel, error) {
		return &hostileChannel{code: code}, nil
	})
	t.Cleanup(func() { drivers.Override(name, nil) })
	var err error
	if p := hostile.Within(t, hostile.Deadline, func() { _, err = m.Channel(name) }); p != nil || err != nil {
		t.Fatalf("Channel = %v, panic %v; want the created channel", err, p)
	}
	m.mu.RLock()
	_, set := m.channels["set"]
	_, created := m.channels[name]
	m.mu.RUnlock()
	if !set || !created {
		t.Error("a channel whose SetLogger panicked was not registered")
	}
}
