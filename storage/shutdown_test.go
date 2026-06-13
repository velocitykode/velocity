package storage

import (
	"context"
	"errors"
	"testing"
)

// shutdownableDisk satisfies the Driver interface (via the embedded
// contract.StorageDriver) and records Shutdown calls. Only Shutdown is
// exercised by these tests; the embedded interface is nil, so any other
// method call would panic and surface an unintended dependency.
type shutdownableDisk struct {
	Driver
	calls int
	err   error
}

func (d *shutdownableDisk) Shutdown(ctx context.Context) error {
	d.calls++
	return d.err
}

func TestManagerShutdown_JoinsErrorsAndClears(t *testing.T) {
	m := NewManager(Config{})

	errA := errors.New("disk a boom")
	errB := errors.New("disk b boom")
	a := &shutdownableDisk{err: errA}
	b := &shutdownableDisk{err: errB}
	ok := &shutdownableDisk{}
	m.AddDisk("a", a)
	m.AddDisk("b", b)
	m.AddDisk("ok", ok)

	err := m.Shutdown(context.Background())
	if err == nil {
		t.Fatal("expected joined error, got nil")
	}

	// Every child attempted even though earlier ones failed.
	if a.calls != 1 || b.calls != 1 || ok.calls != 1 {
		t.Fatalf("expected each disk shut down once, got a=%d b=%d ok=%d", a.calls, b.calls, ok.calls)
	}

	// errors.Is finds each child error in the joined result.
	if !errors.Is(err, errA) {
		t.Errorf("joined error missing errA: %v", err)
	}
	if !errors.Is(err, errB) {
		t.Errorf("joined error missing errB: %v", err)
	}

	// Post-shutdown the disk is no longer resolvable.
	if _, derr := m.Disk("a"); !errors.Is(derr, ErrDiskNotFound) {
		t.Errorf("expected ErrDiskNotFound after shutdown, got %v", derr)
	}

	// Second call is a no-op that returns nil and does not re-call children.
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown should be nil, got %v", err)
	}
	if a.calls != 1 || b.calls != 1 || ok.calls != 1 {
		t.Fatalf("second Shutdown re-called children: a=%d b=%d ok=%d", a.calls, b.calls, ok.calls)
	}
}

// plainDisk satisfies Driver but does not implement contract.ShutdownAware,
// so Shutdown must skip it without error.
type plainDisk struct{ Driver }

func TestManagerShutdown_SkipsNonShutdownable(t *testing.T) {
	m := NewManager(Config{})
	m.AddDisk("plain", &plainDisk{})

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown over non-shutdownable disk should be nil, got %v", err)
	}
	if _, err := m.Disk("plain"); !errors.Is(err, ErrDiskNotFound) {
		t.Errorf("expected registry cleared, got %v", err)
	}
}
