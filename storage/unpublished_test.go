package storage

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// builtDisk records its Shutdown; err is what Shutdown returns.
type builtDisk struct {
	Driver
	shutdowns atomic.Int32
	err       error
}

func (d *builtDisk) Shutdown(context.Context) error {
	d.shutdowns.Add(1)
	return d.err
}

// registerDisk registers a driver under a name unique to the test and
// returns it.
func registerDisk(t *testing.T, suffix string, factory func() (Driver, error)) string {
	t.Helper()
	name := "unpublished-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + suffix
	drivers.Register(name, func(context.Context, DiskConfig) (Driver, error) { return factory() })
	t.Cleanup(func() { drivers.Override(name, nil) })
	return name
}

// A factory that panics is that disk's error: Configure returns it instead
// of panicking, and every disk built before it is published, as it is for
// a factory that returns an error.
func TestConfigure_FactoryPanicIsThatDisksError(t *testing.T) {
	var mu sync.Mutex
	built := map[*builtDisk]bool{}
	ok := registerDisk(t, "ok", func() (Driver, error) {
		d := &builtDisk{}
		mu.Lock()
		built[d] = true
		mu.Unlock()
		return d, nil
	})
	bad := registerDisk(t, "panic", func() (Driver, error) { panic("factory panicked") })
	m := NewManager(Config{})
	cfg := Config{Disks: map[string]DiskConfig{"a": {Driver: ok}, "b": {Driver: ok}, "c": {Driver: ok}, "bad": {Driver: bad}}}

	var err error
	if p := hostile.Within(t, hostile.Deadline, func() { err = m.Configure(cfg) }); p != nil {
		t.Fatalf("Configure panicked: %v", p)
	}
	var pe *panicerr.Error
	if !errors.As(err, &pe) || pe.Recovered() != "factory panicked" {
		t.Fatalf("Configure = %v, want the factory's panic as its error", err)
	}
	published := 0
	for _, name := range []string{"a", "b", "c"} {
		if d, derr := m.Disk(name); derr == nil {
			published++
			if !built[d.(*builtDisk)] {
				t.Errorf("disk %q is not one the factory built", name)
			}
		}
	}
	if published != len(built) {
		t.Errorf("published %d of the %d disks built before the panic; the rest leaked", published, len(built))
	}
}

// A Configure whose factory finishes after a Shutdown that began meanwhile
// publishes nothing: it shuts down every disk it built, writes neither the
// config nor the default, and returns an error holding the disks' own
// Shutdown errors.
func TestConfigure_AcrossShutdownDisposesWhatItBuilt(t *testing.T) {
	code := hostile.New(t, hostile.Block, nil)
	errDispose := errors.New("dispose failed")
	var disk *builtDisk
	name := registerDisk(t, "blocks", func() (Driver, error) {
		code.Run()
		disk = &builtDisk{err: errDispose}
		return disk, nil
	})
	m := NewManager(Config{Default: "before"})
	done := make(chan error, 1)
	go func() {
		done <- m.Configure(Config{Default: "main", Disks: map[string]DiskConfig{"main": {Driver: name}}})
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
		t.Fatal("Configure across a Shutdown returned nil; its disk was published into the emptied manager")
	}
	if !errors.Is(err, errDispose) {
		t.Errorf("Configure = %v, want it to hold the disk's Shutdown error", err)
	}
	if n := disk.shutdowns.Load(); n != 1 {
		t.Errorf("built disk Shutdown calls = %d, want 1", n)
	}
	if _, derr := m.Disk("main"); !errors.Is(derr, ErrDiskNotFound) {
		t.Errorf("Disk(main) = %v, want ErrDiskNotFound", derr)
	}
	m.mu.RLock()
	def, disks := m.defaultDisk, m.config.Disks
	m.mu.RUnlock()
	if def != "before" || disks != nil {
		t.Errorf("default = %q, config disks = %v: the refused Configure wrote its config", def, disks)
	}
}
