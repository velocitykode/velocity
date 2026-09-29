package events

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// detachedRecords captures SetDetachedFailureRecorder calls.
type detachedRecords struct {
	mu     sync.Mutex
	errs   []error
	events []any
}

func (r *detachedRecords) record(_ context.Context, err error, event any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
	r.events = append(r.events, event)
}

func (r *detachedRecords) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errs)
}

// waitFor polls cond for up to two seconds.
func waitFor(cond func() bool) {
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline) && !cond(); time.Sleep(2 * time.Millisecond) {
	}
}

// A detached delivery two listeners fail on is recorded once, with both
// failures joined; a successful one, and a synchronous Dispatch (whose
// caller gets the failure), are not recorded.
func TestSetDetachedFailureRecorder_OncePerFailedDelivery(t *testing.T) {
	d := NewDispatcher()
	rec := &detachedRecords{}
	d.SetDetachedFailureRecorder(rec.record)
	errA, errB := errors.New("a failed"), errors.New("b failed")
	var delivered atomic.Int32
	d.Listen("order.shipped", listenerFn(func(context.Context, any) error { return errA }))
	d.Listen("order.shipped", listenerFn(func(context.Context, any) error { delivered.Add(1); return errB }))
	d.Listen("order.placed", listenerFn(func(context.Context, any) error { delivered.Add(1); return nil }))

	if err := d.Dispatch(context.Background(), "order.shipped"); err == nil {
		t.Fatal("Dispatch returned nil, want the listeners' failures")
	}
	if err := d.DispatchAsync(context.Background(), "order.placed"); err != nil {
		t.Fatalf("DispatchAsync: %v", err)
	}
	if err := d.DispatchAsync(context.Background(), "order.shipped"); err != nil {
		t.Fatalf("DispatchAsync: %v", err)
	}
	waitFor(func() bool { return rec.count() >= 1 && delivered.Load() >= 3 })
	time.Sleep(20 * time.Millisecond)
	if got := rec.count(); got != 1 {
		t.Fatalf("records = %d, want 1 (the failed detached delivery)", got)
	}
	rec.mu.Lock()
	err, ev := rec.errs[0], rec.events[0]
	rec.mu.Unlock()
	if !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Errorf("recorded %v, want both listeners' failures", err)
	}
	if ev != "order.shipped" {
		t.Errorf("recorded event %v, want order.shipped", ev)
	}
}

// Without a recorder, and after SetDetachedFailureRecorder(nil), a failed
// detached delivery is still dispatched as its AsyncFailed and nothing
// else happens.
func TestSetDetachedFailureRecorder_NilAndUnset(t *testing.T) {
	for _, name := range []string{"never set", "set then nil"} {
		t.Run(name, func(t *testing.T) {
			d := NewDispatcher()
			rec := &detachedRecords{}
			if name == "set then nil" {
				d.SetDetachedFailureRecorder(rec.record)
				d.SetDetachedFailureRecorder(nil)
			}
			var asyncFailed atomic.Int32
			d.Listen(OfType[*AsyncFailed](), listenerFn(func(context.Context, any) error { asyncFailed.Add(1); return nil }))
			d.Listen("order.shipped", listenerFn(func(context.Context, any) error { return errors.New("failed") }))
			if err := d.DispatchAsync(context.Background(), "order.shipped"); err != nil {
				t.Fatalf("DispatchAsync: %v", err)
			}
			waitFor(func() bool { return asyncFailed.Load() == 1 })
			if asyncFailed.Load() != 1 {
				t.Errorf("AsyncFailed deliveries = %d, want 1", asyncFailed.Load())
			}
			if rec.count() != 0 {
				t.Errorf("records = %d, want 0", rec.count())
			}
		})
	}
}

// SetDetachedFailureRecorder races with detached deliveries: each failed
// delivery is recorded exactly once, by whichever recorder was installed
// when it finished.
func TestSetDetachedFailureRecorder_ConcurrentWithDeliveries(t *testing.T) {
	d := NewDispatcher()
	recA, recB := &detachedRecords{}, &detachedRecords{}
	d.SetDetachedFailureRecorder(recA.record)
	d.Listen("order.shipped", listenerFn(func(context.Context, any) error { return errors.New("failed") }))

	const goroutines, per = 8, 50
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var swapper sync.WaitGroup
	swapper.Add(1)
	go func() {
		defer swapper.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				d.SetDetachedFailureRecorder(recB.record)
			} else {
				d.SetDetachedFailureRecorder(recA.record)
			}
		}
	}()
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				_ = d.dispatchNow(context.Background(), "order.shipped", true)
			}
		}()
	}
	wg.Wait()
	close(stop)
	swapper.Wait()
	if got := recA.count() + recB.count(); got != goroutines*per {
		t.Errorf("records = %d, want %d (one per failed delivery)", got, goroutines*per)
	}
}
