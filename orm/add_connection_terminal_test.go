package orm

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/orm/drivers"
)

// closeCounter is a driver that counts its Close calls.
type closeCounter struct {
	drivers.Driver
	closes atomic.Int32
}

func (d *closeCounter) Close() error {
	d.closes.Add(1)
	return d.Driver.Close()
}

// A registration after Shutdown began is disposed, not published. When the
// driver registered is the manager's default connection the manager still
// owns it: Shutdown closes it, so the registration must not. One close in
// total, whether the registration arrives while Shutdown still delivers
// the queued events or after Shutdown returned.
func TestAddConnection_DefaultRegisteredOnceShutdownBeganIsClosedOnce(t *testing.T) {
	m := newTestManager(t)
	def := &closeCounter{Driver: m.DefaultDriver()}
	m.mu.Lock()
	m.defaultDriver = def
	m.mu.Unlock()

	release := blockPump(t, m, 0)
	defer release()
	done := make(chan error, 1)
	go func() { done <- m.Shutdown(context.Background()) }()
	if !hostile.Eventually(t, hostile.Deadline, "Shutdown to begin", m.closed.Load) {
		return
	}

	within(t, hostile.Deadline, "AddConnection during Shutdown", func() { m.AddConnection("again", def) })
	if got := def.closes.Load(); got != 0 {
		t.Errorf("default registered while Shutdown still delivers closed %d times, want 0: Shutdown closes it", got)
	}
	if _, err := m.Connection("again"); err == nil {
		t.Error("a connection added once Shutdown began was published")
	}

	release()
	within(t, hostile.Deadline, "Shutdown", func() {
		if err := <-done; err != nil {
			t.Errorf("Shutdown = %v, want nil", err)
		}
	})
	if got := def.closes.Load(); got != 1 {
		t.Errorf("default after Shutdown closed %d times, want 1", got)
	}

	within(t, hostile.Deadline, "AddConnection after Shutdown", func() { m.AddConnection("again", def) })
	if got := def.closes.Load(); got != 1 {
		t.Errorf("default registered after Shutdown closed %d times in total, want 1", got)
	}
}

// Any other driver registered once Shutdown began is still closed, once.
func TestAddConnection_AnotherDriverRegisteredOnceShutdownBeganIsClosed(t *testing.T) {
	m := newTestManager(t)
	other := &closeCounter{Driver: newTestManager(t).DefaultDriver()}
	within(t, hostile.Deadline, "Shutdown", func() {
		if err := m.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown = %v, want nil", err)
		}
	})
	m.AddConnection("late", other)
	if got := other.closes.Load(); got != 1 {
		t.Errorf("driver registered after Shutdown closed %d times, want 1", got)
	}
}

// A driver the manager still holds under a name, registered again under
// another name once Shutdown began, is the manager's to close: the
// registration closes nothing and Shutdown closes it once.
func TestAddConnection_NamedDriverRegisteredAgainOnceShutdownBeganIsClosedOnce(t *testing.T) {
	m := newTestManager(t)
	named := &closeCounter{Driver: newTestManager(t).DefaultDriver()}
	m.AddConnection("a", named)

	release := blockPump(t, m, 0)
	defer release()
	done := make(chan error, 1)
	go func() { done <- m.Shutdown(context.Background()) }()
	if !hostile.Eventually(t, hostile.Deadline, "Shutdown to begin", m.closed.Load) {
		return
	}
	within(t, hostile.Deadline, "AddConnection during Shutdown", func() { m.AddConnection("b", named) })
	if got := named.closes.Load(); got != 0 {
		t.Errorf("named driver registered again while Shutdown still delivers closed %d times, want 0: Shutdown closes it", got)
	}
	release()
	within(t, hostile.Deadline, "Shutdown", func() {
		if err := <-done; err != nil {
			t.Errorf("Shutdown = %v, want nil", err)
		}
	})
	if got := named.closes.Load(); got != 1 {
		t.Errorf("named driver after Shutdown closed %d times, want 1", got)
	}
}

// blockingCloser is a driver whose Close runs code before it closes.
type blockingCloser struct {
	closeCounter
	code *hostile.Code
}

func (d *blockingCloser) Close() error {
	d.closes.Add(1)
	d.code.Run()
	return d.Driver.Close()
}

// The same while Shutdown is closing that driver: the registry no longer
// holds it, its Close has not returned, and the registration does not
// close it a second time.
func TestAddConnection_NamedDriverRegisteredAgainWhileItClosesIsClosedOnce(t *testing.T) {
	m := newTestManager(t)
	code := hostile.New(t, hostile.Block, nil)
	named := &blockingCloser{closeCounter: closeCounter{Driver: newTestManager(t).DefaultDriver()}, code: code}
	m.AddConnection("a", named)
	done := make(chan error, 1)
	go func() { done <- m.Shutdown(context.Background()) }()
	if !code.AwaitEntered(t) {
		return
	}
	registered := make(chan struct{})
	go func() {
		defer close(registered)
		m.AddConnection("b", named)
	}()
	within(t, hostile.Deadline, "AddConnection while the driver closes", func() { <-registered })
	if got := named.closes.Load(); got != 1 {
		t.Errorf("named driver registered again while it closes: %d Close calls, want 1", got)
	}
	code.Release()
	within(t, hostile.Deadline, "Shutdown", func() {
		if err := <-done; err != nil {
			t.Errorf("Shutdown = %v, want nil", err)
		}
	})
	if got := named.closes.Load(); got != 1 {
		t.Errorf("named driver after Shutdown closed %d times, want 1", got)
	}
}
