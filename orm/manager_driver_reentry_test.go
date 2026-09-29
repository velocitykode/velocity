package orm

import (
	"context"
	"database/sql"
	"errors"
	"github.com/velocitykode/velocity/contract"
	"sync/atomic"
	"testing"
	"time"
)

// callbackDriver runs a callback from Close, DB and DriverName, the driver
// methods the manager calls: a driver that re-enters the manager, as one
// logging through the manager's forwarder into an app logger may.
type callbackDriver struct {
	nopDriver
	onClose  func()
	onDB     func()
	onName   func()
	closes   atomic.Int32
	closeErr error
}

func (d *callbackDriver) Close() error {
	d.closes.Add(1)
	if d.onClose != nil {
		d.onClose()
	}
	return d.closeErr
}

func (d *callbackDriver) DB() *sql.DB {
	if d.onDB != nil {
		d.onDB()
	}
	return nil
}

func (d *callbackDriver) DriverName() string {
	if d.onName != nil {
		d.onName()
	}
	return "callback"
}

// installDefault makes d the manager's default driver.
func installDefault(m *Manager, d *callbackDriver) {
	m.mu.Lock()
	m.defaultDriver = d
	m.mu.Unlock()
}

// The manager calls a driver under none of its locks: a driver whose
// Close, DB or DriverName calls back into the manager returns.
func TestManager_DriverCallbacksMayReenterTheManager(t *testing.T) {
	reenter := func(m *Manager) {
		m.SetLogger(&levelLog{})
		_ = m.DatabaseName()
		_, _ = m.Connection("other")
	}
	t.Run("Close", func(t *testing.T) {
		m := newTestManager(t)
		d := &callbackDriver{}
		d.onClose = func() { reenter(m) }
		m.AddConnection("reentrant", d)
		finishes(t, "Shutdown", func() {
			if err := m.Shutdown(context.Background()); err != nil {
				t.Errorf("Shutdown: %v", err)
			}
		})
		if d.closes.Load() != 1 {
			t.Errorf("closes = %d, want 1", d.closes.Load())
		}
	})
	t.Run("DB", func(t *testing.T) {
		m := newTestManager(t)
		t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
		d := &callbackDriver{}
		d.onDB = func() { reenter(m) }
		prev := m.DefaultDriver()
		installDefault(m, d)
		finishes(t, "DB", func() { m.DB() })
		finishes(t, "Stats", func() { m.Stats() })
		m.AddConnection("previous-default", prev)
	})
	t.Run("DriverName", func(t *testing.T) {
		m := newTestManager(t)
		t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
		d := &callbackDriver{}
		d.onName = func() { reenter(m) }
		prev := m.DefaultDriver()
		installDefault(m, d)
		finishes(t, "DriverName", func() { m.DriverName() })
		m.AddConnection("previous-default", prev)
	})
}

// A Shutdown called from inside a driver's Close returns an error at once
// instead of waiting on the closes it is part of; the outer Shutdown still
// closes every driver once.
func TestManagerShutdown_FromADriverCloseIsRefused(t *testing.T) {
	m := newTestManager(t)
	d := &callbackDriver{}
	var inner error
	d.onClose = func() { inner = m.Shutdown(context.Background()) }
	m.AddConnection("reentrant", d)
	finishes(t, "Shutdown", func() {
		if err := m.Shutdown(context.Background()); err != nil {
			t.Errorf("outer Shutdown: %v", err)
		}
	})
	if !errors.Is(inner, contract.ErrStopFromOwnWork) {
		t.Errorf("Shutdown from inside Close = %v, want an error wrapping contract.ErrStopFromOwnWork: the closes were not finished", inner)
	}
	if d.closes.Load() != 1 {
		t.Errorf("closes = %d, want 1", d.closes.Load())
	}
}

// A Shutdown overlapping one that is still closing drivers waits for the
// closes, or returns its own ctx's error; it never reports success early.
func TestManagerShutdown_OverlappingWaitsForTheCloses(t *testing.T) {
	m := newTestManager(t)
	gate := make(chan struct{})
	entered := make(chan struct{})
	d := &callbackDriver{}
	d.onClose = func() { close(entered); <-gate }
	m.AddConnection("slow", d)
	first := make(chan error, 1)
	go func() { first <- m.Shutdown(context.Background()) }()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := m.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("overlapping Shutdown = %v, want its ctx's DeadlineExceeded while a Close runs", err)
	}
	second := make(chan error, 1)
	go func() { second <- m.Shutdown(context.Background()) }()
	close(gate)
	for _, ch := range []chan error{first, second} {
		select {
		case err := <-ch:
			if err != nil {
				t.Errorf("Shutdown = %v, want nil", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Shutdown did not return after the close finished")
		}
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown after the closes = %v, want nil", err)
	}
}

// A connection added after Shutdown began is not published: it is closed,
// and one warning says so, with no driver error text.
func TestManagerAddConnection_AfterShutdownClosesTheDriver(t *testing.T) {
	m := newTestManager(t)
	logs := &levelLog{}
	m.SetLogger(logs)
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for _, d := range []*callbackDriver{
		{},
		{closeErr: errors.New("close failed: secret dsn")},
		{onClose: func() { panic("close boom") }},
	} {
		m.AddConnection("late", d)
		if d.closes.Load() != 1 {
			t.Errorf("late driver closes = %d, want 1", d.closes.Load())
		}
		m.mu.RLock()
		_, published := m.connections["late"]
		m.mu.RUnlock()
		if published {
			t.Error("a connection added after Shutdown was published")
		}
	}
	if got := logs.count("WARN velocity/orm: connection added after Shutdown"); got != 3 {
		t.Errorf("warnings = %d, want one per late connection (3)", got)
	}
}

// Many Shutdowns racing each other and a statement-event drain: every one
// returns nil once the drain and the closes finish, and each driver is
// closed exactly once.
func TestManagerShutdown_Concurrent(t *testing.T) {
	m := newTestManager(t)
	release := blockPump(t, m, 5)
	d := &callbackDriver{}
	m.AddConnection("counted", d)
	const n = 16
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { errs <- m.Shutdown(context.Background()) }()
	}
	time.Sleep(20 * time.Millisecond)
	release()
	for i := 0; i < n; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Errorf("Shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a concurrent Shutdown did not return")
		}
	}
	if got := d.closes.Load(); got != 1 {
		t.Errorf("closes = %d, want 1", got)
	}
}
