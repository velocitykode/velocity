package log

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

// builtLogger records its Shutdown, which panics when told to or returns
// err.
type builtLogger struct {
	NullLogger
	shutdowns atomic.Int32
	panics    bool
	err       error
}

func (l *builtLogger) Shutdown(context.Context) error {
	l.shutdowns.Add(1)
	if l.panics {
		panic("duplicate shutdown panicked")
	}
	return l.err
}

// registerLogDriver registers factory under a name unique to the test.
func registerLogDriver(t *testing.T, factory func() Logger) string {
	t.Helper()
	name := "unpublished-" + strings.ReplaceAll(t.Name(), "/", "-")
	Drivers().Register(name, func(context.Context, LogConfig) (Logger, error) { return factory(), nil })
	t.Cleanup(func() { Drivers().Override(name, nil) })
	return name
}

// A channel built by a caller that loses the race to publish it is shut
// down contained: the caller gets the published channel, the loser's
// Shutdown panic does not escape, and it is written once as a warning.
func TestChannel_DuplicateIsDisposedContained(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	code := hostile.New(t, hostile.Block, nil)
	var builds atomic.Int32
	var loser, winner *builtLogger
	name := registerLogDriver(t, func() Logger {
		if builds.Add(1) == 1 {
			code.Run()
			loser = &builtLogger{panics: true}
			return loser
		}
		winner = &builtLogger{}
		return winner
	})
	m := NewManager(LoggingConfig{Channels: map[string]ChannelConfig{"main": {Driver: name}}})
	type result struct {
		l   Logger
		err error
	}
	done := make(chan result, 1)
	go func() {
		l, err := m.Channel("main")
		done <- result{l, err}
	}()
	if !code.AwaitEntered(t) {
		return
	}
	if got, err := m.Channel("main"); err != nil || got != winner {
		t.Fatalf("second Channel = %v, %v; want the logger it built", got, err)
	}
	code.Release()
	var r result
	if p := hostile.Within(t, hostile.Deadline, func() { r = <-done }); p != nil {
		t.Fatalf("waiting for the first Channel: %v", p)
	}
	if r.err != nil || r.l != winner {
		t.Fatalf("first Channel = %v, %v; want the published logger", r.l, r.err)
	}
	if n := loser.shutdowns.Load(); n != 1 {
		t.Errorf("duplicate Shutdown calls = %d, want 1", n)
	}
	if lines := out.Lines(); len(lines) != 1 || !strings.Contains(lines[0], "duplicate shutdown panicked") {
		t.Errorf("fallback lines = %v, want one warning carrying the panic", lines)
	}
}

// A channel built across a Shutdown is not published into the emptied
// manager: it is shut down and the lookup's error holds its Shutdown
// error.
func TestChannel_AcrossShutdownIsNotPublished(t *testing.T) {
	code := hostile.New(t, hostile.Block, nil)
	errDispose := errors.New("dispose failed")
	var built *builtLogger
	name := registerLogDriver(t, func() Logger {
		code.Run()
		built = &builtLogger{err: errDispose}
		return built
	})
	m := NewManager(LoggingConfig{Channels: map[string]ChannelConfig{"main": {Driver: name}}})
	done := make(chan error, 1)
	go func() {
		_, err := m.Channel("main")
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
	if !errors.Is(err, errDispose) {
		t.Errorf("Channel across a Shutdown = %v, want an error holding the logger's Shutdown error", err)
	}
	if n := built.shutdowns.Load(); n != 1 {
		t.Errorf("built logger Shutdown calls = %d, want 1", n)
	}
	m.mu.RLock()
	_, published := m.channels["main"]
	m.mu.RUnlock()
	if published {
		t.Error("the channel built across the Shutdown was published")
	}
}
