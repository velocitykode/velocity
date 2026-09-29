package notification

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

// hostileChannel is a channel whose SetLogger runs code: user code the
// manager calls when it hands its logger.
type hostileChannel struct {
	testChannel
	code *hostile.Code
}

func (c *hostileChannel) SetLogger(contract.Logger) { c.code.Run() }

var hostileChannelSeq atomic.Int64

// registerHostileChannel registers a channel factory that runs code before
// it returns a testChannel, and removes it when the test ends.
func registerHostileChannel(t *testing.T, code *hostile.Code, builds *atomic.Int32) string {
	t.Helper()
	name := "hostile-channel-" + string(rune('a'+hostileChannelSeq.Add(1)%26)) + strings.ReplaceAll(t.Name(), "/", "-")
	drivers.Register(name, func(context.Context, ChannelConfig) (Channel, error) {
		if builds != nil {
			builds.Add(1)
		}
		code.Run()
		return &testChannel{}, nil
	})
	t.Cleanup(func() { drivers.Override(name, nil) })
	return name
}

// managerEntries are the manager's entry points user code may call back
// into.
func managerEntries(name string) map[string]func(m *Manager) {
	return map[string]func(m *Manager){
		"Channel":    func(m *Manager) { _, _ = m.Channel(name) },
		"SetChannel": func(m *Manager) { m.SetChannel("other", &testChannel{}) },
		"SetLogger":  func(m *Manager) { m.SetLogger(nil) },
		"Shutdown":   func(m *Manager) { _ = m.Shutdown(context.Background()) },
		"Send": func(m *Manager) {
			_ = m.Send(context.Background(), &testNotifiable{}, &testNotification{subject: "s", channels: []string{"other"}})
		},
	}
}

// A channel's SetLogger is user code: the manager hands its logger with no
// lock held, so a SetLogger that panics, blocks, or calls back into the
// manager cannot deadlock it, and the manager works afterwards.
func TestManager_HandsItsLoggerWithHostileChannel(t *testing.T) {
	for _, mode := range hostile.Modes() {
		for entryName, entry := range managerEntries("x") {
			t.Run(mode.String()+"/"+entryName, func(t *testing.T) {
				fallbacklogtest.Capture(t)
				m := NewManager()
				m.SetChannel("other", &testChannel{})
				code := hostile.New(t, mode, func() { entry(m) })
				m.SetChannel("hostile", &hostileChannel{code: code})
				hand := func() { m.SetLogger(hostile.NewLogger(nil)) }
				if mode == hostile.Block {
					go hand()
					if !code.AwaitEntered(t) {
						return
					}
					hostile.Within(t, hostile.Deadline, func() { entry(m) })
				} else {
					hostile.Within(t, hostile.Deadline, hand)
				}
				code.Release()
				code.Disarm()
				hostile.Within(t, hostile.Deadline, func() {
					m.SetChannel("late", &hostileChannel{code: code})
					if _, err := m.Channel("late"); err != nil {
						t.Errorf("Channel after a retry: %v", err)
					}
				})
			})
		}
	}
}

// A registered channel factory is user code: the manager creates the
// channel with no lock held.
func TestManager_ChannelCreationWithHostileFactory(t *testing.T) {
	for _, mode := range hostile.Modes() {
		for entryName := range managerEntries("") {
			t.Run(mode.String()+"/"+entryName, func(t *testing.T) {
				fallbacklogtest.Capture(t)
				m := NewManager()
				m.SetChannel("other", &testChannel{})
				var name string
				code := hostile.New(t, mode, func() { managerEntries(name)[entryName](m) })
				name = registerHostileChannel(t, code, nil)
				create := func() { _, _ = m.Channel(name) }
				if mode == hostile.Block {
					go create()
					if !code.AwaitEntered(t) {
						return
					}
					if entryName != "Channel" {
						hostile.Within(t, hostile.Deadline, func() { managerEntries(name)[entryName](m) })
					}
				} else {
					hostile.Within(t, hostile.Deadline, create)
				}
				code.Release()
				code.Disarm()
				hostile.Eventually(t, hostile.Deadline, "a Channel after the release", func() bool {
					_, err := m.Channel(name)
					return err == nil
				})
			})
		}
	}
}

// A lookup of a channel from inside its own creation gets an error at once.
func TestManager_ChannelFromInsideItsOwnCreation(t *testing.T) {
	m := NewManager()
	var name string
	var inner error
	code := hostile.New(t, hostile.Reenter, func() { _, inner = m.Channel(name) })
	name = registerHostileChannel(t, code, nil)
	hostile.Within(t, hostile.Deadline, func() {
		if _, err := m.Channel(name); err != nil {
			t.Errorf("outer Channel: %v", err)
		}
	})
	if inner == nil || !strings.Contains(inner.Error(), "requested from inside its own build") {
		t.Fatalf("inner Channel err = %v, want the re-entry error", inner)
	}
}

// Concurrent first uses of one channel create it once.
func TestManager_ChannelCreatedOnceUnderConcurrency(t *testing.T) {
	var builds atomic.Int32
	code := hostile.New(t, hostile.Block, nil)
	m := NewManager()
	name := registerHostileChannel(t, code, &builds)
	var wg sync.WaitGroup
	got := make(chan Channel, 32)
	for range 32 {
		wg.Go(func() {
			ch, err := m.Channel(name)
			if err != nil {
				t.Error(err)
			}
			got <- ch
		})
	}
	if !code.AwaitEntered(t) {
		return
	}
	hostile.Eventually(t, hostile.Deadline, "every other caller waiting on the build", func() bool {
		return m.builds.Joined(name) == 31
	})
	code.Release()
	hostile.Within(t, hostile.Deadline, wg.Wait)
	close(got)
	var first Channel
	for ch := range got {
		if first == nil {
			first = ch
		}
		if ch != first {
			t.Fatal("callers got different channels")
		}
	}
	if n := builds.Load(); n != 1 {
		t.Fatalf("the factory ran %d times, want 1", n)
	}
}

// Channels registered while the first SetLogger runs are all handed the
// forwarder: race SetChannel against SetLogger.
func TestManager_HandoffRacingSetChannel(t *testing.T) {
	for round := 0; round < 50; round++ {
		m := NewManager()
		channels := make([]*loggerChannel, 16)
		var wg sync.WaitGroup
		for i := range channels {
			channels[i] = &loggerChannel{}
			wg.Go(func() { m.SetChannel(string(rune('a'+i)), channels[i]) })
		}
		wg.Go(func() { m.SetLogger(hostile.NewLogger(nil)) })
		wg.Wait()
		for i, ch := range channels {
			if ch.current() != m.log() {
				t.Fatalf("round %d: channel %d was not handed the forwarder", round, i)
			}
		}
	}
}
