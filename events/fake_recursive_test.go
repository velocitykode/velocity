package events

import (
	"context"
	"sync"
	"testing"
	"time"
)

// reentrantListener captures a reference to the FakeDispatcher and, on
// every Handle call, calls back into it. This is the canonical M-47 shape:
// a listener body re-enters the dispatcher (AssertDispatched / Dispatch /
// HasListeners) on the same goroutine that holds the listener slot.
type reentrantListener struct {
	fake *FakeDispatcher
	fn   func(f *FakeDispatcher)
}

func (l *reentrantListener) Handle(ctx context.Context, event interface{}) error {
	l.fn(l.fake)
	return nil
}

func (l *reentrantListener) Async() bool { return false }

// runWithTimeout invokes fn in a goroutine and fails the test with msg if
// it does not complete within d. Used to convert lock-acquisition deadlocks
// into deterministic test failures instead of hung CI runs.
func runWithTimeout(t *testing.T, d time.Duration, msg string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal(msg)
	}
}

// TestFakeDispatcher_ListenerCallsAssertDispatched pins the M-47 deadlock
// regression. Before the fix, Dispatch held f.mu for the duration of
// listener execution; AssertDispatched acquires f.mu.RLock, so a listener
// body that asserts on its own dispatcher deadlocks the test. After the
// fix the listener slice is snapshotted under the lock and the lock is
// dropped before listener bodies run, so re-entrant assertions succeed.
func TestFakeDispatcher_ListenerCallsAssertDispatched(t *testing.T) {
	fake := NewFakeDispatcher()
	fake.StopFaking()

	var asserted bool
	fake.Listen("ping", &reentrantListener{
		fake: fake,
		fn: func(f *FakeDispatcher) {
			// Re-enter the FakeDispatcher from inside a listener body.
			// Pre-fix: deadlocks because Dispatch holds f.mu.Lock and
			// AssertDispatched needs f.mu.RLock on the same dispatcher.
			//
			// We are not asserting against the live event set; we just
			// need a method that takes f.mu. AssertNothingDispatched is
			// the cheapest such method when shouldFake is false (no
			// events were recorded, so it returns nil).
			if err := f.AssertNothingDispatched(); err != nil {
				t.Errorf("AssertNothingDispatched returned unexpected error: %v", err)
			}
			asserted = true
		},
	})

	runWithTimeout(t, 2*time.Second, "FakeDispatcher.Dispatch deadlocked when listener re-entered AssertDispatched", func() {
		if err := fake.Dispatch(context.Background(), "ping"); err != nil {
			t.Errorf("Dispatch returned error: %v", err)
		}
	})

	if !asserted {
		t.Fatal("listener body never ran")
	}
}

// TestFakeDispatcher_ListenerDispatchesFollowUp pins the deadlock variant
// where a listener body dispatches a follow-up event on the same fake.
// Pre-fix the outer Dispatch holds f.mu.Lock and the follow-up Dispatch
// blocks acquiring it; post-fix the outer Dispatch has already released
// the lock by the time the listener body runs.
func TestFakeDispatcher_ListenerDispatchesFollowUp(t *testing.T) {
	fake := NewFakeDispatcher()
	fake.StopFaking()

	var followUpRan bool
	fake.Listen("follow.up", &reentrantListener{
		fake: fake,
		fn: func(*FakeDispatcher) {
			followUpRan = true
		},
	})

	fake.Listen("outer", &reentrantListener{
		fake: fake,
		fn: func(f *FakeDispatcher) {
			if err := f.Dispatch(context.Background(), "follow.up"); err != nil {
				t.Errorf("follow-up Dispatch returned error: %v", err)
			}
		},
	})

	runWithTimeout(t, 2*time.Second, "FakeDispatcher.Dispatch deadlocked when listener dispatched a follow-up event", func() {
		if err := fake.Dispatch(context.Background(), "outer"); err != nil {
			t.Errorf("outer Dispatch returned error: %v", err)
		}
	})

	if !followUpRan {
		t.Fatal("follow-up listener never ran")
	}
}

// TestFakeDispatcher_ListenerListensConcurrently pins the variant where a
// listener body registers another listener via Listen (write lock). The
// snapshot-then-release pattern must let the inner Listen acquire f.mu.Lock
// while the outer Dispatch is still inside the call.
func TestFakeDispatcher_ListenerListensConcurrently(t *testing.T) {
	fake := NewFakeDispatcher()
	fake.StopFaking()

	var registered bool
	fake.Listen("trigger", &reentrantListener{
		fake: fake,
		fn: func(f *FakeDispatcher) {
			id := f.Listen("new.event", &reentrantListener{
				fake: f,
				fn:   func(*FakeDispatcher) {},
			})
			if id == 0 {
				t.Error("Listen returned 0 id")
			}
			registered = true
		},
	})

	runWithTimeout(t, 2*time.Second, "FakeDispatcher.Dispatch deadlocked when listener body called Listen", func() {
		if err := fake.Dispatch(context.Background(), "trigger"); err != nil {
			t.Errorf("Dispatch returned error: %v", err)
		}
	})

	if !registered {
		t.Fatal("inner Listen never ran")
	}
}

// TestFakeDispatcher_ListenerRaceWithParallelAssertion pins the snapshot
// guarantee: a listener invocation iterates over the snapshot taken under
// f.mu, not the live map, so a parallel Off / Listen / AssertDispatched
// running while the listener body is executing cannot corrupt the
// iteration or block until the listener returns.
func TestFakeDispatcher_ListenerRaceWithParallelAssertion(t *testing.T) {
	fake := NewFakeDispatcher()
	fake.StopFaking()

	released := make(chan struct{})
	listenerEntered := make(chan struct{})

	fake.Listen("evt", &reentrantListener{
		fake: fake,
		fn: func(*FakeDispatcher) {
			close(listenerEntered)
			// Block until the parallel goroutine has had a chance to
			// acquire the lock. If Dispatch still held f.mu the goroutine
			// would never make progress.
			<-released
		},
	})

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		runWithTimeout(t, 2*time.Second, "Dispatch hung", func() {
			if err := fake.Dispatch(context.Background(), "evt"); err != nil {
				t.Errorf("Dispatch returned error: %v", err)
			}
		})
	}()

	go func() {
		defer wg.Done()
		<-listenerEntered
		// While the listener body is running we acquire the lock via a
		// read-locked method. Pre-fix this blocked until the listener
		// returned; post-fix it succeeds immediately because Dispatch
		// has already released the lock.
		runWithTimeout(t, 2*time.Second, "parallel HasListeners blocked while listener was running", func() {
			_ = fake.HasListeners("evt")
		})
		close(released)
	}()

	wg.Wait()
}
