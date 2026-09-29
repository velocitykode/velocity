package notification

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	logdrivers "github.com/velocitykode/velocity/log/drivers"
)

var _ contract.LoggerAware = (*Manager)(nil)

const dispatchFailed = "event dispatch failed"

// failingDispatch is an event dispatcher whose listeners always fail.
func failingDispatch(context.Context, interface{}) error {
	return errors.New("listener failed")
}

// An event dispatch that fails is written through the manager's logger,
// and through the fallback logger without one; nothing goes through the
// standard library log.
func TestManager_DispatchFailureWritesThroughItsLogger(t *testing.T) {
	n := &testNotification{subject: "hi", channels: []string{"test"}}

	t.Run("manager logger", func(t *testing.T) {
		stdlib := fallbacklogtest.CaptureStdlib(t)
		fallback := fallbacklogtest.Capture(t)
		out := &fallbacklogtest.Output{}
		m := NewManager()
		m.SetChannel("test", &testChannel{})
		m.SetEventDispatcher(failingDispatch)
		m.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))

		if err := m.Send(context.Background(), &testNotifiable{}, n); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if got := strings.Count(out.String(), "WARN: "+dispatchFailed); got == 0 {
			t.Errorf("manager logger warn lines = 0, want at least 1 (%q)", out.String())
		}
		if s := stdlib.String() + fallback.String(); s != "" {
			t.Errorf("stdlib / fallback got %q, want nothing", s)
		}
	})

	t.Run("no logger", func(t *testing.T) {
		stdlib := fallbacklogtest.CaptureStdlib(t)
		fallback := fallbacklogtest.Capture(t)
		m := NewManager()
		m.SetChannel("test", &testChannel{})
		m.SetEventDispatcher(failingDispatch)

		if err := m.Send(context.Background(), &testNotifiable{}, n); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if got := fallback.Count("WARN", dispatchFailed); got == 0 {
			t.Errorf("fallback warn lines = 0, want at least 1 (%q)", fallback.String())
		}
		if s := stdlib.String(); s != "" {
			t.Errorf("stdlib got %q, want nothing", s)
		}
	})
}

// loggerChannel is a channel that records the logger it is handed.
type loggerChannel struct {
	testChannel
	mu     sync.Mutex
	logger contract.Logger
}

func (c *loggerChannel) SetLogger(l contract.Logger) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logger = l
}

func (c *loggerChannel) current() contract.Logger {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.logger
}

// SetLogger reaches a channel registered before it, and a channel set
// after it is handed the manager's logger.
func TestManager_SetLoggerReachesItsChannels(t *testing.T) {
	l := logdrivers.NewConsoleLoggerTo(&fallbacklogtest.Output{}, 0)
	before, after := &loggerChannel{}, &loggerChannel{}
	m := NewManager()
	m.SetChannel("before", before)
	m.SetLogger(l)
	m.SetChannel("after", after)
	for name, ch := range map[string]*loggerChannel{"before": before, "after": after} {
		if got := ch.current(); got != contract.Logger(l) {
			t.Errorf("channel %q holds logger %v, want the manager's", name, got)
		}
	}
}

// SetLogger may run while notifications are sent: the logger is held under
// the manager's mutex.
func TestManager_SetLoggerWhileSendingIsSafe(t *testing.T) {
	fallbacklogtest.Capture(t)
	out := &fallbacklogtest.Output{}
	m := NewManager()
	m.SetChannel("test", &testChannel{})
	m.SetEventDispatcher(failingDispatch)
	n := &testNotification{subject: "hi", channels: []string{"test"}}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				m.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
				m.SetLogger(nil)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = m.Send(context.Background(), &testNotifiable{}, n)
			}
		}()
	}
	wg.Wait()
}

// Concurrent SetLogger calls leave the manager and every channel on the
// same logger: the channels are handed it under the manager's lock.
func TestManager_ConcurrentSetLoggerKeepsChannelsInStep(t *testing.T) {
	for round := 0; round < 50; round++ {
		m := NewManager()
		channels := make([]*loggerChannel, 8)
		for i := range channels {
			channels[i] = &loggerChannel{}
			m.SetChannel(string(rune('a'+i)), channels[i])
		}
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				m.SetLogger(logdrivers.NewConsoleLoggerTo(&fallbacklogtest.Output{}, 0))
			}()
		}
		wg.Wait()
		want := m.log()
		for i, ch := range channels {
			if got := ch.current(); got != want {
				t.Fatalf("round %d: channel %d holds %p, the manager %p", round, i, got, want)
			}
		}
	}
}

// SetLogger's edge inputs: nil puts the manager back on the fallback; a
// zero-value Manager takes a logger without panicking; a logger set after
// Shutdown still receives the manager's lines.
func TestManager_SetLoggerEdgeInputs(t *testing.T) {
	t.Run("nil restores the fallback", func(t *testing.T) {
		fallback := fallbacklogtest.Capture(t)
		out := &fallbacklogtest.Output{}
		m := NewManager()
		m.SetEventDispatcher(failingDispatch)
		m.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
		m.SetLogger(nil)
		m.dispatchEvent(context.Background(), struct{}{})
		if out.String() != "" {
			t.Errorf("replaced logger got %q, want nothing", out.String())
		}
		if got := fallback.Count("WARN", dispatchFailed); got != 1 {
			t.Errorf("fallback warn lines = %d, want 1 (%q)", got, fallback.String())
		}
	})

	t.Run("zero value", func(t *testing.T) {
		out := &fallbacklogtest.Output{}
		var m Manager
		m.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
		m.SetEventDispatcher(failingDispatch)
		m.dispatchEvent(context.Background(), struct{}{})
		if got := strings.Count(out.String(), "WARN: "+dispatchFailed); got != 1 {
			t.Errorf("logger warn lines = %d, want 1 (%q)", got, out.String())
		}
	})

	t.Run("after shutdown", func(t *testing.T) {
		fallback := fallbacklogtest.Capture(t)
		out := &fallbacklogtest.Output{}
		m := NewManager()
		if err := m.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		m.SetEventDispatcher(failingDispatch)
		m.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
		m.dispatchEvent(context.Background(), struct{}{})
		if got := strings.Count(out.String(), "WARN: "+dispatchFailed); got != 1 {
			t.Errorf("logger warn lines = %d, want 1 (%q)", got, out.String())
		}
		if s := fallback.String(); s != "" {
			t.Errorf("fallback got %q, want nothing", s)
		}
	})
}
