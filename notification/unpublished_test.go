package notification

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// builtChannel records its Shutdown; err is what Shutdown returns.
type builtChannel struct {
	testChannel
	shutdowns atomic.Int32
	err       error
}

func (c *builtChannel) Shutdown(context.Context) error {
	c.shutdowns.Add(1)
	return c.err
}

// registerBlockingChannel registers a channel driver that runs code, then
// builds a builtChannel whose Shutdown returns errDispose.
func registerBlockingChannel(t *testing.T, code *hostile.Code, errDispose error, built **builtChannel) string {
	t.Helper()
	name := "unpublished-" + strings.ReplaceAll(t.Name(), "/", "-")
	drivers.Register(name, func(context.Context, ChannelConfig) (Channel, error) {
		code.Run()
		*built = &builtChannel{err: errDispose}
		return *built, nil
	})
	t.Cleanup(func() { drivers.Override(name, nil) })
	return name
}

// A channel created across a Shutdown is shut down, and the lookup's error
// holds that channel's own Shutdown error.
func TestChannel_AcrossShutdownDisposesTheChannel(t *testing.T) {
	code := hostile.New(t, hostile.Block, nil)
	errDispose := errors.New("dispose failed")
	var built *builtChannel
	name := registerBlockingChannel(t, code, errDispose, &built)
	m := NewManager()
	done := make(chan error, 1)
	go func() {
		_, err := m.Channel(name)
		done <- err
	}()
	if !code.AwaitEntered(t) {
		return
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown = %v", err)
	}
	code.Release()
	var err error
	hostile.Within(t, hostile.Deadline, func() { err = <-done })
	if err == nil {
		t.Fatal("Channel across a Shutdown returned no error")
	}
	if !errors.Is(err, errDispose) {
		t.Errorf("Channel = %v, want it to hold the channel's Shutdown error", err)
	}
	if n := built.shutdowns.Load(); n != 1 {
		t.Errorf("built channel Shutdown calls = %d, want 1", n)
	}
}

// A channel created while SetChannel registered another under its name is
// shut down; the lookup succeeds with the one set, and the created
// channel's Shutdown error is written once as a warning.
func TestChannel_ExistingWinsDisposesTheCreatedChannel(t *testing.T) {
	code := hostile.New(t, hostile.Block, nil)
	errDispose := errors.New("dispose failed")
	var built *builtChannel
	name := registerBlockingChannel(t, code, errDispose, &built)
	m := NewManager()
	logger := hostile.NewLogger(nil)
	m.SetLogger(logger)
	type result struct {
		ch  Channel
		err error
	}
	done := make(chan result, 1)
	go func() {
		ch, err := m.Channel(name)
		done <- result{ch, err}
	}()
	if !code.AwaitEntered(t) {
		return
	}
	set := &testChannel{}
	m.SetChannel(name, set)
	code.Release()
	var r result
	hostile.Within(t, hostile.Deadline, func() { r = <-done })
	if r.err != nil || r.ch != set {
		t.Fatalf("Channel = %v, %v; want the channel SetChannel registered", r.ch, r.err)
	}
	if n := built.shutdowns.Load(); n != 1 {
		t.Errorf("created channel Shutdown calls = %d, want 1", n)
	}
	var warned int
	for _, l := range logger.Lines() {
		if l.Level == hostile.Warn {
			for i := 0; i+1 < len(l.KVs); i += 2 {
				if err, ok := l.KVs[i+1].(error); ok && errors.Is(err, errDispose) {
					warned++
				}
			}
		}
	}
	if warned != 1 {
		t.Errorf("warnings carrying the Shutdown error = %d, want 1; lines: %v", warned, logger.Lines())
	}
}
