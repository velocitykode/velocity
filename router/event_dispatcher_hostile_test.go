package router

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/hostile"
)

// An async listener is user code on the pool's worker: one that panics is
// counted as a failed event and the worker lives on; one that blocks holds
// only its worker, so dispatch stays non-blocking and a stop returns at its
// deadline; one that dispatches again and stops its own pool returns, and
// a stop from outside then drains.
func TestAsyncEventDispatcher_ListenerIsContained(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			r := NewV2()
			failures := &eventemit.Failures{}
			r.ShareEventFailures(failures)
			var failed, delivered atomic.Int64
			var lastErr atomic.Pointer[error]
			failures.SetHook(func(err error, _ any) {
				failed.Add(1)
				lastErr.Store(&err)
			})

			code := hostile.New(t, mode, func() {
				_ = r.events.Dispatcher()(context.Background(), "inner")
				_ = r.ShutdownEventDispatcher(context.Background())
			})
			r.SetAsyncEventDispatcher(func(_ context.Context, ev interface{}) error {
				if ev == "hostile" {
					code.Run()
				}
				delivered.Add(1)
				return nil
			}, 1, 8)
			if err := r.events.Dispatcher()(context.Background(), "hostile"); err != nil {
				t.Fatalf("dispatch: %v", err)
			}

			switch mode {
			case hostile.Panic:
				waitUntil(t, func() bool { return failed.Load() == 1 }, "the listener's panic counted as a failed event")
				var rp contract.RecoveredPanic
				if p := lastErr.Load(); p == nil || !errors.As(*p, &rp) {
					t.Errorf("the failure handed to the policy is not a contract.RecoveredPanic: %v", p)
				}
				if err := r.events.Dispatcher()(context.Background(), "next"); err != nil {
					t.Fatalf("dispatch after the panic: %v", err)
				}
				waitUntil(t, func() bool { return delivered.Load() >= 1 }, "the worker delivering after the panic")
				if n := failed.Load(); n != 1 {
					t.Errorf("failed events = %d, want the one panic counted once", n)
				}
			case hostile.Block:
				<-code.Entered()
				hostile.Within(t, hostile.Deadline, func() {
					if err := r.events.Dispatcher()(context.Background(), "next"); err != nil && !errors.Is(err, ErrEventBufferFull) {
						t.Errorf("dispatch while the listener blocks = %v", err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
					defer cancel()
					if err := r.ShutdownEventDispatcher(ctx); !errors.Is(err, context.DeadlineExceeded) {
						t.Errorf("stop while the listener blocks = %v, want its deadline", err)
					}
				})
				code.Release()
			case hostile.Reenter:
				waitUntil(t, func() bool { return code.Calls() == 1 && delivered.Load() >= 1 }, "the re-entering listener returning")
			}
			hostile.Within(t, hostile.Deadline, func() {
				if err := r.ShutdownEventDispatcher(context.Background()); err != nil {
					t.Errorf("stop from outside = %v, want nil once drained", err)
				}
			})
		})
	}
}

// waitUntil polls cond until it holds or hostile.Deadline passes.
func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(hostile.Deadline)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
