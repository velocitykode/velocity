package storage

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// TestLocalDriver_PutStream_ReaderCallsShutdown shuts the driver down from
// inside the stream's Read. The copy runs without the driver's lock, so
// Shutdown returns, and the write it interrupted fails without leaving its
// object or its temp file.
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
	if shutdownErr != nil {
		t.Fatalf("Shutdown from Read: %v", shutdownErr)
	}
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("PutStream after Shutdown = %v, want ErrInvalidPath", err)
	}
	assertEntries(t, dir)
}

// TestLocalDriver_Shutdown_DoesNotWaitForABlockedStream shuts the driver
// down while a stream's Read is blocked: Shutdown returns at once, and the
// write fails once the reader returns, leaving the root empty.
func TestLocalDriver_Shutdown_DoesNotWaitForABlockedStream(t *testing.T) {
	d, dir := newTestLocalDriver(t)
	code := hostile.New(t, hostile.Block, nil)
	done := make(chan error, 1)
	go func() { done <- d.PutStream("obj", &codeReader{code: code, data: []byte("x")}) }() //safe-goroutine: the blocked writer; its result is read below
	if !code.AwaitEntered(t) {
		return
	}
	hostile.Within(t, hostile.Deadline, func() {
		if err := d.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	code.Release()
	if err := <-done; !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("PutStream after Shutdown = %v, want ErrInvalidPath", err)
	}
	assertEntries(t, dir)
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
