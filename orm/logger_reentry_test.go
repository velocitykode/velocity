package orm

import (
	"context"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/orm/drivers"
)

// reentrantDriver runs onSetLogger or onObserver when the manager hands it
// a logger or a statement observer: an extension that re-enters the
// manager from its callback (registering another connection, installing
// a logger).
type reentrantDriver struct {
	drivers.Driver
	onSetLogger func()
	onObserver  func()
}

func (d *reentrantDriver) SetLogger(contract.Logger) {
	if d.onSetLogger != nil {
		d.onSetLogger()
	}
}

func (d *reentrantDriver) SetStatementObserver(drivers.StatementObserver) {
	if d.onObserver != nil {
		d.onObserver()
	}
}

func (d *reentrantDriver) Close() error { return nil }

// finishes fails the test when fn has not returned within the deadline: a
// manager that calls an extension under its own lock deadlocks when the
// extension re-enters it.
func finishes(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s did not return: the manager called the extension under a lock the extension needs", what)
	}
}

// An extension may re-enter the manager from the callbacks the manager
// makes while wiring it: none of them runs under a manager lock.
func TestManagerExtensionCallbacks_MayReenterTheManager(t *testing.T) {
	t.Run("SetLogger registers another connection", func(t *testing.T) {
		m := newTestManager(t)
		t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
		m.SetLogger(&levelLog{})
		d := &reentrantDriver{}
		d.onSetLogger = func() { d.onSetLogger = nil; m.AddConnection("nested", &gateDriver{}) }
		finishes(t, "AddConnection", func() { m.AddConnection("outer", d) })
		if _, err := m.Connection("nested"); err != nil {
			t.Errorf("nested connection: %v", err)
		}
	})
	t.Run("SetLogger installs a logger", func(t *testing.T) {
		m := newTestManager(t)
		t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
		d := &reentrantDriver{}
		inner := &levelLog{}
		d.onSetLogger = func() { d.onSetLogger = nil; m.SetLogger(inner) }
		m.AddConnection("outer", d)
		finishes(t, "SetLogger", func() { m.SetLogger(&levelLog{}) })
		if m.Logger() != contract.Logger(inner) {
			t.Errorf("manager logger = %p, want the one the extension installed last (%p)", m.Logger(), inner)
		}
	})
	t.Run("SetStatementObserver installs a logger", func(t *testing.T) {
		m := newTestManager(t)
		t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
		d := &reentrantDriver{}
		d.onObserver = func() { d.onObserver = nil; m.SetLogger(&levelLog{}) }
		finishes(t, "AddConnection", func() { m.AddConnection("outer", d) })
	})
}
