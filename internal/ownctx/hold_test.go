package ownctx

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/tracekeys"
)

func TestHold_DefersUntilReleaseInOrder(t *testing.T) {
	ctx, h := Hold(context.Background())
	if HeldBy(ctx) != h {
		t.Fatal("HeldBy(Hold's context) is not its Held")
	}
	var order []int
	for i := 1; i <= 3; i++ {
		if !h.Defer(func() { order = append(order, i) }) {
			t.Fatalf("Defer %d before Release = false", i)
		}
	}
	if len(order) != 0 {
		t.Fatalf("deferred work ran before Release: %v", order)
	}
	h.Release()
	if len(order) != 3 || order[0] != 1 || order[1] != 2 || order[2] != 3 {
		t.Fatalf("Release ran %v, want [1 2 3]", order)
	}
	// After Release, Defer declines: the caller runs the work itself.
	if h.Defer(func() { t.Error("work deferred after Release ran") }) {
		t.Fatal("Defer after Release = true")
	}
	h.Release() // a second Release does nothing
}

func TestHold_NilAndOtherContexts(t *testing.T) {
	var nilHeld *Held
	if nilHeld.Defer(func() {}) {
		t.Error("Defer on a nil Held = true")
	}
	nilHeld.Release()

	for name, ctx := range map[string]context.Context{
		"background": context.Background(),
		"bridge":     Bridge(withIDs(context.Background())),
		"detached":   Detached(withIDs(context.Background())),
	} {
		if HeldBy(ctx) != nil {
			t.Errorf("HeldBy(%s) != nil", name)
		}
	}
	// A context derived from a holding one does not hold: HeldBy runs no
	// method of the context it is given.
	held, _ := Hold(context.Background())
	derived, cancel := context.WithCancel(held)
	defer cancel()
	if HeldBy(derived) != nil {
		t.Error("HeldBy(a context derived from Hold's) != nil")
	}
	// A nil caller context still holds.
	var none context.Context
	ctx, h := Hold(none)
	if h == nil || HeldBy(ctx) != h {
		t.Error("Hold(nil) returned no Held")
	}
}

// HeldBy runs no method of a caller's context: it is safe to call on one
// under a lock.
func TestHeldBy_CallsNoContextMethod(t *testing.T) {
	var calls atomic.Int64
	ctx := countingCtx{Context: context.Background(), calls: &calls}
	if HeldBy(ctx) != nil {
		t.Fatal("HeldBy(a caller's context) != nil")
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("HeldBy called the context's methods %d time(s)", n)
	}
}

func TestHold_CarriesTheCallersEndAndIDs(t *testing.T) {
	parent, cancel := context.WithCancel(withIDs(context.Background()))
	ctx, _ := Hold(parent)
	if ctx.Value(tracekeys.RequestID) != "req-1" {
		t.Errorf("request id = %v", ctx.Value(tracekeys.RequestID))
	}
	if ctx.Err() != nil {
		t.Fatalf("Err before the caller's end = %v", ctx.Err())
	}
	cancel()
	<-ctx.Done()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("Err after the caller's end = %v", ctx.Err())
	}
}

func TestHoldDetached_BoundedAndDetached(t *testing.T) {
	parent, cancelParent := context.WithCancel(withIDs(context.Background()))
	cancelParent() // the caller's end does not reach a detached context
	ctx, cancel, h := HoldDetached(parent, time.Hour)
	if HeldBy(ctx) != h {
		t.Fatal("HeldBy(HoldDetached's context) is not its Held")
	}
	if ctx.Err() != nil {
		t.Fatalf("Err of a detached context whose caller ended = %v", ctx.Err())
	}
	if ctx.Value(tracekeys.TraceID) != "trace-1" {
		t.Errorf("trace id = %v", ctx.Value(tracekeys.TraceID))
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Error("HoldDetached's context has no deadline")
	}
	cancel()
	<-ctx.Done()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("Err after cancel = %v", ctx.Err())
	}

	var none context.Context
	short, cancelShort, _ := HoldDetached(none, time.Millisecond)
	defer cancelShort()
	<-short.Done()
	if !errors.Is(short.Err(), context.DeadlineExceeded) {
		t.Errorf("Err after the bound = %v", short.Err())
	}
}

// Defer and Release race from many goroutines: every Defer that reports
// true runs exactly once, at Release, and every one that reports false
// runs nothing. Run with -race -cpu 1,2.
func TestHeld_ConcurrentDeferAndRelease(t *testing.T) {
	for round := 0; round < 50; round++ {
		_, h := Hold(context.Background())
		var accepted, ran atomic.Int64
		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func() { //safe-goroutine: a test worker joined by wg
				defer wg.Done()
				for i := 0; i < 20; i++ {
					if h.Defer(func() { ran.Add(1) }) {
						accepted.Add(1)
					}
				}
			}()
		}
		wg.Add(1)
		go func() { //safe-goroutine: a test worker joined by wg
			defer wg.Done()
			h.Release()
		}()
		wg.Wait()
		h.Release()
		if accepted.Load() != ran.Load() {
			t.Fatalf("round %d: %d deferred, %d ran", round, accepted.Load(), ran.Load())
		}
	}
}
