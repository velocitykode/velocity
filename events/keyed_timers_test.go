package events

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// firedBy returns the callback key's pending timer runs when it fires, so
// a test can run it by hand: a timer that fired but had not yet taken its
// delivery when it was replaced or stopped runs it late.
func (k *keyedTimers[V]) firedBy(key string) func() {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.pending[key].fired
}

// timedDispatcher is a debouncing or coalescing dispatcher under test, on
// real timers of an hour, whose callbacks the test runs by hand.
type timedDispatcher struct {
	d        *DefaultDispatcher
	dispatch func(context.Context, interface{}) error
	pending  func() int
	stop     func()
	// firedBy returns the pending timer's callback for a key.
	firedBy func(key string) func()
}

func timedDispatchers() map[string]func() *timedDispatcher {
	return map[string]func() *timedDispatcher{
		"debounce": func() *timedDispatcher {
			d := NewDebouncingDispatcher(time.Hour)
			return &timedDispatcher{d: d.DefaultDispatcher, dispatch: d.Dispatch, pending: d.GetPendingCount, stop: d.Stop, firedBy: d.timers.firedBy}
		},
		"coalesce": func() *timedDispatcher {
			d := NewCoalescingDispatcher(time.Hour)
			pending := func() int { return d.pending.count() }
			return &timedDispatcher{d: d.DefaultDispatcher, dispatch: d.Dispatch, pending: pending, stop: d.Stop, firedBy: d.pending.firedBy}
		},
	}
}

// A timer that fired but was replaced before it took its event delivers
// nothing: the replacement keeps its own window and is delivered once,
// when its own timer fires.
func TestTimedDispatch_AReplacedTimerThatFiredDeliversNothing(t *testing.T) {
	for name, build := range timedDispatchers() {
		t.Run(name, func(t *testing.T) {
			td := build()
			var got []interface{}
			td.d.Listen("evt", listenerFunc(func(_ context.Context, e interface{}) error {
				got = append(got, e)
				return nil
			}))
			first := &BaseEvent{EventName: "evt"}
			second := &BaseEvent{EventName: "evt"}
			t.Cleanup(td.stop)
			_ = td.dispatch(context.Background(), first)
			stale := td.firedBy("evt")
			_ = td.dispatch(context.Background(), second)

			stale()
			if len(got) != 0 {
				t.Fatalf("the replaced timer delivered %d events before the replacement's window", len(got))
			}
			if n := td.pending(); n != 1 {
				t.Fatalf("pending = %d after the stale timer, want the replacement", n)
			}
			td.firedBy("evt")()
			if len(got) != 1 || got[0] != second {
				t.Fatalf("delivered %v, want only the replacement", got)
			}
			if n := td.pending(); n != 0 {
				t.Errorf("pending = %d after delivery, want 0", n)
			}
		})
	}
}

// A delivery whose listener dispatches the same event again leaves the new
// pending delivery in place, and Stop still cancels it: its timer, fired
// after Stop, delivers nothing.
func TestTimedDispatch_RedispatchFromAListenerStaysPending(t *testing.T) {
	for name, build := range timedDispatchers() {
		t.Run(name, func(t *testing.T) {
			td := build()
			var handled atomic.Int32
			td.d.Listen("evt", listenerFunc(func(ctx context.Context, event interface{}) error {
				if handled.Add(1) == 1 {
					_ = td.dispatch(ctx, event)
				}
				return nil
			}))
			t.Cleanup(td.stop)
			_ = td.dispatch(context.Background(), "evt")
			// The first timer's callback runs here, on this goroutine, and
			// has returned when it does: its listener re-dispatched.
			td.firedBy("evt")()
			if got := handled.Load(); got != 1 {
				t.Fatalf("handled = %d, want 1", got)
			}
			if got := td.pending(); got != 1 {
				t.Fatalf("pending = %d, want 1 (the listener's dispatch)", got)
			}
			late := td.firedBy("evt")
			td.stop()
			if got := td.pending(); got != 0 {
				t.Errorf("pending after Stop = %d, want 0", got)
			}
			late()
			if got := handled.Load(); got != 1 {
				t.Errorf("handled = %d after the stopped timer fired, want 1", got)
			}
		})
	}
}

// A listener run by a timer that panics, blocks, or calls back into the
// dispatcher (Dispatch, the pending count, Stop) never holds the timers'
// lock: while it runs, the dispatcher's entry points return, and the
// delivery's failure does not escape the timer's goroutine.
func TestTimedDispatch_HostileListener(t *testing.T) {
	for name, build := range timedDispatchers() {
		for _, mode := range hostile.Modes() {
			t.Run(name+"/"+mode.String(), func(t *testing.T) {
				td := build()
				code := hostile.New(t, mode, func() {
					_ = td.dispatch(context.Background(), "evt")
					_ = td.pending()
					td.stop()
				})
				td.d.Listen("evt", listenerFunc(func(context.Context, interface{}) error {
					code.Run()
					return nil
				}))
				t.Cleanup(td.stop)
				_ = td.dispatch(context.Background(), "evt")
				timerFired := td.firedBy("evt")
				fired := make(chan any, 1)
				go func() {
					defer func() { fired <- recover() }()
					timerFired()
				}()
				code.AwaitEntered(t)
				hostile.Within(t, hostile.Deadline, func() {
					_ = td.dispatch(context.Background(), "evt")
					_ = td.pending()
					td.stop()
				})
				code.Release()
				select {
				case p := <-fired:
					if p != nil {
						t.Fatalf("the delivery's failure escaped the timer: %v", p)
					}
				case <-time.After(hostile.Deadline):
					t.Fatal("the timer's delivery never returned")
				}
				if code.Calls() == 0 {
					t.Fatal("the hostile listener never ran")
				}
			})
		}
	}
}

// Dispatch, the pending count and Stop race real timers firing, from many
// goroutines, for both dispatchers; run under -race.
func TestTimedDispatch_ConcurrentStress(t *testing.T) {
	debounce := NewDebouncingDispatcher(time.Microsecond)
	coalesce := NewCoalescingDispatcher(time.Microsecond)
	var delivered atomic.Int64
	count := listenerFunc(func(context.Context, interface{}) error { delivered.Add(1); return nil })
	for _, key := range []string{"a", "b", "c"} {
		debounce.Listen(key, count)
		coalesce.Listen(key, count)
	}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				key := []string{"a", "b", "c"}[(g+i)%3]
				_ = debounce.Dispatch(context.Background(), key)
				_ = coalesce.Dispatch(context.Background(), key)
				_ = debounce.GetPendingCount()
				_ = coalesce.GetCoalescedCount(key)
				if i%97 == 0 {
					debounce.Stop()
					coalesce.Stop()
				}
			}
		}()
	}
	wg.Wait()
	// Both still deliver after the stress: one more dispatch each arrives.
	before := delivered.Load()
	_ = debounce.Dispatch(context.Background(), "a")
	_ = coalesce.Dispatch(context.Background(), "b")
	hostile.Eventually(t, hostile.Deadline, "both dispatchers delivered after the stress", func() bool {
		return delivered.Load() >= before+2
	})
	debounce.Stop()
	coalesce.Stop()
}
