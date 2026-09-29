package orm

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// countingPanicLogger counts every line and panics on each.
type countingPanicLogger struct{ lines atomic.Int64 }

func (c *countingPanicLogger) line()                           { c.lines.Add(1); panic("relay logger boom") }
func (c *countingPanicLogger) Debug(string, ...any)            { c.line() }
func (c *countingPanicLogger) Info(string, ...any)             { c.line() }
func (c *countingPanicLogger) Warn(string, ...any)             { c.line() }
func (c *countingPanicLogger) Error(string, ...any)            { c.line() }
func (c *countingPanicLogger) Fatal(string, ...any)            { c.line() }
func (c *countingPanicLogger) With(kvs ...any) contract.Logger { return contract.BindFields(c, kvs...) }

// enqueueOne commits one job row.
func enqueueOne(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.TransactionWithOutbox(context.Background(), func(_ *sql.Tx, outbox Pending) error {
		_, err := outbox.Enqueue(outboxTestPayload{Name: "job"}, WithMaxAttempts(2))
		return err
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
}

// fastRelay returns a relay polling every few milliseconds.
func fastRelay(m *Manager, cb RelayCallbacks) *Relay {
	return NewRelay(m, cb, RelayConfig{
		PollInterval:  5 * time.Millisecond,
		LeaseDuration: 30 * time.Millisecond,
		BackoffBase:   time.Millisecond,
		BackoffMax:    5 * time.Millisecond,
		MaxAttempts:   2,
	})
}

// A worker whose callback panics records the failure even when the relay
// logger panics too: the line lands on the fallback, the row reaches the
// dead-letter state, and the process survives.
func TestRelay_PanickingLoggerOnWorkerPanic(t *testing.T) {
	fallbacklogtest.Capture(t)
	m, _ := newOutboxFileManager(t)
	enqueueOne(t, m)
	relay := fastRelay(m, RelayCallbacks{OnJob: func(context.Context, any, string, string) error { panic("callback boom") }})
	logger := &countingPanicLogger{}
	relay.SetLogger(logger)
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = relay.Stop(context.Background()) })
	waitFor(t, 3*time.Second, func() bool {
		rows, _ := m.ListOutboxRows(context.Background(), 10)
		return len(rows) == 1 && rows[0].DLQ && rows[0].LastError != ""
	})
	if logger.lines.Load() == 0 {
		t.Error("the relay logger was never called; the test proves nothing")
	}
}

// A relay loop whose claim warning's logger panics keeps polling.
func TestRelay_PanickingLoggerOnClaimKeepsTheLoop(t *testing.T) {
	fallbacklogtest.Capture(t)
	m := newTestManager(t) // no outbox table: every claim fails and warns
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	relay := fastRelay(m, RelayCallbacks{})
	logger := &countingPanicLogger{}
	relay.SetLogger(logger)
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = relay.Stop(context.Background()) })
	waitFor(t, 3*time.Second, func() bool { return logger.lines.Load() >= 3 })
}

// Stop called from a dispatch callback returns an error at once and
// changes nothing; Stop from outside then stops the relay.
func TestRelay_StopFromCallbackIsRefused(t *testing.T) {
	m, _ := newOutboxFileManager(t)
	enqueueOne(t, m)
	var relay *Relay
	inner := make(chan error, 1)
	relay = fastRelay(m, RelayCallbacks{OnJob: func(context.Context, any, string, string) error {
		inner <- relay.Stop(context.Background())
		return nil
	}})
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case err := <-inner:
		if err == nil {
			t.Error("Stop from a callback returned nil, want an error: it would wait on itself")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop from a callback did not return")
	}
	done := make(chan error, 1)
	go func() { done <- relay.Stop(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Stop from outside: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop from outside did not return")
	}
}

// Stop honours its ctx while a callback ignores the shutdown ctx: it
// returns ctx's error at the deadline instead of waiting for the callback.
func TestRelay_StopHonoursCtxWithAStuckCallback(t *testing.T) {
	m, _ := newOutboxFileManager(t)
	enqueueOne(t, m)
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	relay := NewRelay(m, RelayCallbacks{OnJob: func(context.Context, any, string, string) error {
		close(entered)
		<-release // ignores ctx
		return nil
	}}, RelayConfig{PollInterval: 5 * time.Millisecond, ShutdownGrace: 10 * time.Millisecond})
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- relay.Stop(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Stop = %v, want ctx's DeadlineExceeded while a callback is stuck", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not honour its ctx: it waited on a stuck callback")
	}
}
