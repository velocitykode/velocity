package storage

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/drain/draintest"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

// TestLocalDriver_PutStream_ReaderCallsShutdown shuts the driver down from
// inside the stream's Read. The write is the driver's work in flight, which
// Shutdown would wait for, so Shutdown is refused at once and changes
// nothing: the write commits, and a Shutdown from outside then succeeds.
func TestLocalDriver_PutStream_ReaderCallsShutdown(t *testing.T) {
	d, dir := newTestLocalDriver(t)
	var shutdownErr error
	code := hostile.New(t, hostile.Reenter, func() { shutdownErr = d.Shutdown(context.Background()) })
	var err error
	if p := hostile.Within(t, hostile.Deadline, func() {
		err = d.PutStream("sub/obj", &codeReader{code: code, data: []byte("x")})
	}); p != nil {
		t.Fatalf("PutStream panicked: %v", p)
	}
	if code.Calls() != 1 {
		t.Fatalf("reader ran %d times, want 1", code.Calls())
	}
	if !errors.Is(shutdownErr, contract.ErrStopFromOwnWork) {
		t.Fatalf("Shutdown from Read = %v, want contract.ErrStopFromOwnWork", shutdownErr)
	}
	if err != nil {
		t.Fatalf("PutStream whose reader's Shutdown was refused = %v, want nil", err)
	}
	assertEntries(t, dir, "sub/obj")
	if got, gerr := d.Get("sub/obj"); gerr != nil || string(got) != "x" {
		t.Fatalf("Get after the write = %q, %v; want %q", got, gerr, "x")
	}
	hostile.Within(t, hostile.Deadline, func() {
		if err := d.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown from outside = %v, want nil", err)
		}
	})
}

// TestLocalDriver_Shutdown_WaitsForABlockedStreamWithinCtx shuts the
// driver down while a stream's Read is blocked: Shutdown waits for the
// write until its ctx ends and returns ctx's error, closing the root so
// the write cannot commit; the write fails once the reader returns,
// leaving the root empty, and a Shutdown after it returns nil.
func TestLocalDriver_Shutdown_WaitsForABlockedStreamWithinCtx(t *testing.T) {
	d, dir := newTestLocalDriver(t)
	code := hostile.New(t, hostile.Block, nil)
	done := make(chan error, 1)
	go func() { done <- d.PutStream("obj", &codeReader{code: code, data: []byte("x")}) }() //safe-goroutine: the blocked writer; its result is read below
	if !code.AwaitEntered(t) {
		return
	}
	hostile.Within(t, hostile.Deadline, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if err := d.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Shutdown with the write blocked = %v, want context.DeadlineExceeded", err)
		}
	})
	select {
	case err := <-done:
		t.Fatalf("the blocked write returned before its reader did: %v", err)
	default:
	}
	// The deadline's force closes the root on a goroutine of its own:
	// wait for that evidence before the reader returns.
	for !rootClosed(d) {
		runtime.Gosched()
	}
	code.Release()
	if err := <-done; !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("PutStream cut at the deadline = %v, want ErrInvalidPath", err)
	}
	assertEntries(t, dir)
	hostile.Within(t, hostile.Deadline, func() {
		if err := d.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown after the write released = %v, want nil", err)
		}
	})
}

// TestLocalDriver_Shutdown_WaitsForAWriteToCommit shuts the driver down
// while a write is copying: Shutdown returns once the write committed.
func TestLocalDriver_Shutdown_WaitsForAWriteToCommit(t *testing.T) {
	d, dir := newTestLocalDriver(t)
	code := hostile.New(t, hostile.Block, nil)
	done := make(chan error, 1)
	go func() { done <- d.PutStream("obj", &codeReader{code: code, data: []byte("x")}) }() //safe-goroutine: the blocked writer; its result is read below
	if !code.AwaitEntered(t) {
		return
	}
	stopped := make(chan error, 1)
	go func() { stopped <- d.Shutdown(context.Background()) }() //safe-goroutine: the waiting Shutdown; its result is read below
	// Admission closes on Shutdown's own goroutine: wait for that evidence.
	for !d.run.Stopping() {
		runtime.Gosched()
	}
	if err := d.PutStream("late", bytes.NewReader([]byte("y"))); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("PutStream once Shutdown began = %v, want ErrInvalidPath", err)
	}
	select {
	case err := <-stopped:
		t.Fatalf("Shutdown returned before the write in flight finished: %v", err)
	default:
	}
	code.Release()
	if err := <-done; err != nil {
		t.Fatalf("PutStream in flight at Shutdown = %v, want nil", err)
	}
	hostile.Within(t, hostile.Deadline, func() {
		if err := <-stopped; err != nil {
			t.Errorf("Shutdown = %v, want nil", err)
		}
	})
	assertEntries(t, dir, "obj")
}

// TestLocalDriver_DrainContract runs the drain owner contract.
func TestLocalDriver_DrainContract(t *testing.T) {
	draintest.Run(t, func(t *testing.T) draintest.Owner {
		d, _ := newTestLocalDriver(t)
		return draintest.Owner{
			Hold: func(t *testing.T) func() {
				code := hostile.New(t, hostile.Block, nil)
				done := make(chan error, 1)
				go func() { done <- d.PutStream("held", &codeReader{code: code, data: []byte("x")}) }() //safe-goroutine: the held write; released by the returned func
				code.AwaitEntered(t)
				var once sync.Once
				return func() { once.Do(func() { code.Release(); <-done }) }
			},
			Stop: d.Shutdown,
			Refused: func(t *testing.T) bool {
				return errors.Is(d.PutStream("refused", bytes.NewReader([]byte("y"))), ErrInvalidPath)
			},
			StopFromOwnWork: func(t *testing.T) error {
				var err error
				code := hostile.New(t, hostile.Reenter, func() { err = d.Shutdown(context.Background()) })
				_ = d.PutStream("own", &codeReader{code: code, data: []byte("x")})
				return err
			},
		}
	})
}

// TestLocalDriver_PutStream_HostileReader runs the stream's reader as each
// kind of hostile user code. A panic reaches the caller with the root left
// as it was; a blocked reader holds up no other operation; a reader that
// calls back into the driver completes, and so does the write.
func TestLocalDriver_PutStream_HostileReader(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			d, dir := newTestLocalDriver(t)
			if err := d.Put("keep", []byte("k")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			var reentered error
			code := hostile.New(t, mode, func() {
				if err := d.Put("other", []byte("o")); err != nil {
					reentered = err
					return
				}
				if _, err := d.Get("keep"); err != nil {
					reentered = err
					return
				}
				reentered = d.PutStream("nested", bytes.NewReader([]byte("n")))
			})
			done := make(chan error, 1)
			var panicked any
			go func() { //safe-goroutine: the writer under test; its result and panic are read below
				defer func() {
					panicked = recover()
					close(done)
				}()
				done <- d.PutStream("obj", &codeReader{code: code, data: []byte("x")})
			}()
			switch mode {
			case hostile.Panic:
				<-done
				if panicked != hostile.PanicValue {
					t.Fatalf("panic = %v, want the reader's panic", panicked)
				}
				assertEntries(t, dir, "keep")
			case hostile.Block:
				if !code.AwaitEntered(t) {
					return
				}
				hostile.Within(t, hostile.Deadline, func() {
					if err := d.Put("side", []byte("s")); err != nil {
						t.Errorf("Put while a stream is blocked: %v", err)
					}
					if _, err := d.Get("keep"); err != nil {
						t.Errorf("Get while a stream is blocked: %v", err)
					}
					if err := d.Delete("side"); err != nil {
						t.Errorf("Delete while a stream is blocked: %v", err)
					}
				})
				code.Release()
				if err := <-done; err != nil {
					t.Fatalf("PutStream: %v", err)
				}
				assertEntries(t, dir, "keep", "obj")
			case hostile.Reenter:
				var err error
				hostile.Within(t, hostile.Deadline, func() { err = <-done })
				if err != nil || reentered != nil {
					t.Fatalf("PutStream = %v, re-entered calls = %v", err, reentered)
				}
				assertEntries(t, dir, "keep", "nested", "obj", "other")
			}
			if code.Calls() == 0 {
				t.Fatal("the reader never ran")
			}
		})
	}
}

func BenchmarkLocalDriver_PutStream(b *testing.B) {
	d := NewLocalDriver(DiskConfig{Driver: "local", Root: b.TempDir()})
	b.Cleanup(func() { _ = d.Shutdown(context.Background()) })
	payload := bytes.Repeat([]byte("x"), 4096)
	b.ReportAllocs()
	for b.Loop() {
		if err := d.PutStream("dir/obj", bytes.NewReader(payload)); err != nil {
			b.Fatal(err)
		}
	}
}

// rootClosed reports whether the driver's root is closed.
func rootClosed(d *LocalDriver) bool {
	d.rootMu.RLock()
	defer d.rootMu.RUnlock()
	return d.rootHandle == nil
}

// TestManager_OwnsCaller_InsideALocalUpload asks the manager from a
// stream's Read: the write is work the disk's Shutdown waits for, and the
// manager's Shutdown waits for the disk's, so the manager owns the caller.
// Outside the write it does not.
func TestManager_OwnsCaller_InsideALocalUpload(t *testing.T) {
	d, _ := newTestLocalDriver(t)
	m := NewManager(Config{})
	m.AddDisk("plain", NewMemoryDriver(DiskConfig{}))
	m.AddDisk("local", d)
	if m.OwnsCaller() {
		t.Fatal("OwnsCaller outside any write = true, want false")
	}
	var inside bool
	code := hostile.New(t, hostile.Reenter, func() { inside = m.OwnsCaller() })
	hostile.Within(t, hostile.Deadline, func() {
		if err := d.PutStream("obj", &codeReader{code: code, data: []byte("x")}); err != nil {
			t.Errorf("PutStream = %v, want nil", err)
		}
	})
	if !inside {
		t.Fatal("OwnsCaller inside a stream's Read = false, want true")
	}
	if m.OwnsCaller() {
		t.Fatal("OwnsCaller after the write = true, want false")
	}
}

// askedDisk is a disk that exports OwnsCaller and runs code in it.
type askedDisk struct {
	Driver
	code *hostile.Code
	owns bool
}

func (d *askedDisk) OwnsCaller() bool {
	d.code.Run()
	return d.owns
}

// TestManager_OwnsCaller_AsksDisksWithNoLockHeld gives the manager a disk
// whose OwnsCaller replaces a disk on the manager, which takes the
// manager's write lock: the manager asks it with no lock held, takes its
// answer, and skips a typed-nil disk.
func TestManager_OwnsCaller_AsksDisksWithNoLockHeld(t *testing.T) {
	m := NewManager(Config{})
	disk := &askedDisk{owns: true}
	disk.code = hostile.New(t, hostile.Reenter, func() { m.AddDisk("other", NewMemoryDriver(DiskConfig{})) })
	m.AddDisk("nil", (*LocalDriver)(nil))
	m.AddDisk("asked", disk)
	var owns bool
	if p := hostile.Within(t, hostile.Deadline, func() { owns = m.OwnsCaller() }); p != nil {
		t.Fatalf("OwnsCaller panicked: %v", p)
	}
	if disk.code.Calls() != 1 {
		t.Fatalf("the disk was asked %d times, want 1", disk.code.Calls())
	}
	if !owns {
		t.Fatal("OwnsCaller = false, want the disk's answer, true")
	}
}

// TestManager_Shutdown_FromAStreamReadIsRefused shuts the manager down
// from inside a stream's Read. The write is work the disk's Shutdown waits
// for, and the manager's Shutdown waits for the disk's, so it is refused
// at once and changes nothing: the disk stays registered, the write
// commits, and a Shutdown from outside then succeeds.
func TestManager_Shutdown_FromAStreamReadIsRefused(t *testing.T) {
	d, dir := newTestLocalDriver(t)
	m := NewManager(Config{})
	m.AddDisk("local", d)
	var shutdownErr error
	code := hostile.New(t, hostile.Reenter, func() { shutdownErr = m.Shutdown(context.Background()) })
	var err error
	if p := hostile.Within(t, hostile.Deadline, func() {
		err = d.PutStream("obj", &codeReader{code: code, data: []byte("x")})
	}); p != nil {
		t.Fatalf("PutStream panicked: %v", p)
	}
	if !errors.Is(shutdownErr, contract.ErrStopFromOwnWork) {
		t.Fatalf("Manager.Shutdown from Read = %v, want contract.ErrStopFromOwnWork", shutdownErr)
	}
	if err != nil {
		t.Fatalf("PutStream whose reader's Shutdown was refused = %v, want nil", err)
	}
	if _, derr := m.Disk("local"); derr != nil {
		t.Fatalf("Disk after the refused Shutdown = %v, want the disk still registered", derr)
	}
	assertEntries(t, dir, "obj")
	hostile.Within(t, hostile.Deadline, func() {
		if err := m.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown from outside = %v, want nil", err)
		}
	})
}

// TestManager_OwnsCaller_InsideAnUploadOnADrainingDisk asks the manager
// from a stream's Read while a Shutdown from outside drains the disk: the
// registry is already empty, and the manager still owns the caller, since
// its Shutdown waits for that write.
func TestManager_OwnsCaller_InsideAnUploadOnADrainingDisk(t *testing.T) {
	d, _ := newTestLocalDriver(t)
	m := NewManager(Config{})
	m.AddDisk("local", d)
	var inside bool
	var nested error
	detached := make(chan struct{})
	code := hostile.New(t, hostile.Reenter, func() {
		<-detached
		inside = m.OwnsCaller()
		nested = m.Shutdown(context.Background())
	})
	put := make(chan error, 1)
	go func() { put <- d.PutStream("obj", &codeReader{code: code, data: []byte("x")}) }()
	if !code.AwaitEntered(t) {
		return
	}
	shut := make(chan error, 1)
	go func() { shut <- m.Shutdown(context.Background()) }()
	if !hostile.Eventually(t, hostile.Deadline, "the manager to detach its disks", func() bool {
		_, err := m.Disk("local")
		return err != nil
	}) {
		return
	}
	close(detached)
	hostile.Within(t, hostile.Deadline, func() {
		if err := <-put; err != nil {
			t.Errorf("PutStream = %v, want nil: the Shutdown from outside waits for it", err)
		}
		if err := <-shut; err != nil {
			t.Errorf("Shutdown from outside = %v, want nil", err)
		}
	})
	if !inside {
		t.Error("OwnsCaller inside a stream's Read on a draining disk = false, want true")
	}
	if !errors.Is(nested, contract.ErrStopFromOwnWork) {
		t.Errorf("Manager.Shutdown from Read on a draining disk = %v, want contract.ErrStopFromOwnWork", nested)
	}
	if m.OwnsCaller() {
		t.Error("OwnsCaller after the Shutdown = true, want false")
	}
}

// TestManager_OwnsCaller_ContainsAPanickingDisk gives the manager a disk
// whose OwnsCaller panics: the panic stays inside the manager and counts
// as that disk not owning the caller, the other disks are still asked,
// and a Shutdown, which asks the disks first, goes on.
func TestManager_OwnsCaller_ContainsAPanickingDisk(t *testing.T) {
	m := NewManager(Config{})
	hostileDisk := &askedDisk{code: hostile.New(t, hostile.Panic, nil), owns: true}
	m.AddDisk("hostile", hostileDisk)
	var owns bool
	if p := hostile.Within(t, hostile.Deadline, func() { owns = m.OwnsCaller() }); p != nil {
		t.Fatalf("OwnsCaller panicked: %v", p)
	}
	if owns {
		t.Error("OwnsCaller with a panicking disk = true, want false: a panic is not an answer")
	}
	owner := &askedDisk{owns: true}
	m.AddDisk("owner", owner)
	for range 20 { // the disks are asked in map order
		if p := hostile.Within(t, hostile.Deadline, func() { owns = m.OwnsCaller() }); p != nil {
			t.Fatalf("OwnsCaller panicked: %v", p)
		}
		if !owns {
			t.Fatal("OwnsCaller = false, want the answer of the disk that did not panic, true")
		}
	}
	owner.owns = false
	var err error
	if p := hostile.Within(t, hostile.Deadline, func() { err = m.Shutdown(context.Background()) }); p != nil {
		t.Fatalf("Shutdown panicked: %v", p)
	}
	if err != nil {
		t.Errorf("Shutdown = %v, want nil", err)
	}
	if _, derr := m.Disk("hostile"); derr == nil {
		t.Error("the disks are still registered after Shutdown")
	}
}

// ownsCallerPanicLine is the warning written for an OwnsCaller that panics.
const ownsCallerPanicLine = "velocity: OwnsCaller panicked"

// TestManager_OwnsCaller_ReportsAPanickingDiskOnce asks a manager whose
// disk's OwnsCaller panics several times, and shuts it down: the panic is
// written once for that disk, and once more for a second such disk.
func TestManager_OwnsCaller_ReportsAPanickingDiskOnce(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	m := NewManager(Config{})
	m.AddDisk("hostile", &askedDisk{code: hostile.New(t, hostile.Panic, nil)})
	hostile.Within(t, hostile.Deadline, func() {
		for range 3 {
			_ = m.OwnsCaller()
		}
	})
	if n := out.Count("WARN", ownsCallerPanicLine); n != 1 {
		t.Fatalf("%d warnings after three asks of one disk, want 1; output:\n%s", n, out)
	}
	m.AddDisk("second", &askedDisk{code: hostile.New(t, hostile.Panic, nil)})
	hostile.Within(t, hostile.Deadline, func() {
		_ = m.OwnsCaller()
		_ = m.Shutdown(context.Background())
	})
	if n := out.Count("WARN", ownsCallerPanicLine); n != 2 {
		t.Fatalf("%d warnings with two panicking disks, want 2; output:\n%s", n, out)
	}
}
