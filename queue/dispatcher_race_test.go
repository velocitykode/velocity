package queue

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// TestMemoryDriver_DispatcherRace hammers SetEventDispatcher concurrently
// with PushCtx so the race detector flags any unsynchronised access to
// eventDispatcher. Must pass under `go test -race`.
func TestMemoryDriver_DispatcherRace(t *testing.T) {
	q := NewMemoryDriver()
	q.Start()
	defer q.Shutdown(context.Background())

	const (
		setters = 8
		pushers = 8
		iters   = 200
	)

	var calls atomic.Int64
	makeFn := func() func(context.Context, interface{}) error {
		return func(_ context.Context, _ interface{}) error {
			calls.Add(1)
			return nil
		}
	}

	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < setters; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			for j := 0; j < iters; j++ {
				if (idx+j)%2 == 0 {
					q.SetEventDispatcher(makeFn())
				} else {
					q.SetEventDispatcher(nil)
				}
			}
		}(i)
	}

	for i := 0; i < pushers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			for j := 0; j < iters; j++ {
				job := &TestJob{ID: "race", Message: "race"}
				_ = q.PushCtx(context.Background(), job, "race-queue")
			}
		}(i)
	}

	close(start)
	wg.Wait()
}

// TestDatabaseDriver_DispatcherRace hammers SetEventDispatcher concurrently
// with PushCtx on the database driver backed by an in-memory SQLite store.
func TestDatabaseDriver_DispatcherRace(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	const (
		setters = 8
		pushers = 8
		iters   = 100
	)

	var calls atomic.Int64
	makeFn := func() func(context.Context, interface{}) error {
		return func(_ context.Context, _ interface{}) error {
			calls.Add(1)
			return nil
		}
	}

	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < setters; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			for j := 0; j < iters; j++ {
				if (idx+j)%2 == 0 {
					driver.SetEventDispatcher(makeFn())
				} else {
					driver.SetEventDispatcher(nil)
				}
			}
		}(i)
	}

	for i := 0; i < pushers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			for j := 0; j < iters; j++ {
				job := &TestJob{ID: "race", Message: "race"}
				_ = driver.PushCtx(context.Background(), job, "race-queue")
			}
		}(i)
	}

	close(start)
	wg.Wait()
}

// TestMemoryDriver_DispatchUnderLockNoDeadlock is the headline regression
// guard for the original bug. PushCtx/PushDelayedCtx hold m.mu.Lock() while
// calling dispatchJobQueued -> dispatchEvent. The previous fix used
// m.mu.RLock() inside dispatchEvent, which deadlocked the goroutine against
// its own writer lock (sync.RWMutex is not reentrant: an RLock that arrives
// while the same goroutine holds the writer Lock blocks forever).
//
// With atomic.Pointer the dispatcher slot is lock-free, so the listener can
// itself swap the dispatcher (a SetEventDispatcher call that previously
// needed m.mu.Lock()) without ever touching m.mu. A simple 5s timeout
// suffices to detect the regression - the broken code would never finish a
// single PushCtx.
func TestMemoryDriver_DispatchUnderLockNoDeadlock(t *testing.T) {
	q := NewMemoryDriver()
	// Intentionally skip q.Start() so the background processDelayedJobs
	// goroutine never grabs m.mu - that way a hang here is unambiguously
	// the dispatcher self-deadlock and not contention from elsewhere.
	defer q.Shutdown(context.Background())

	var dispatched atomic.Int64
	var listener func(context.Context, interface{}) error
	listener = func(_ context.Context, _ interface{}) error {
		// Re-install the dispatcher from inside the dispatch call.
		// The broken implementation held m.mu.Lock() across this
		// call, so SetEventDispatcher's own Lock attempt blocked
		// against the writer lock the current goroutine already
		// owns - classic self-deadlock. atomic.Pointer.Store takes
		// no lock so this is safe.
		q.SetEventDispatcher(listener)
		dispatched.Add(1)
		return nil
	}
	q.SetEventDispatcher(listener)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			if err := q.PushCtx(context.Background(), &TestJob{ID: "x", Message: "y"}, "dispatch-under-lock"); err != nil {
				t.Errorf("push: %v", err)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PushCtx deadlocked while dispatching - dispatcher slot is taking m.mu")
	}

	if got := dispatched.Load(); got != 50 {
		t.Fatalf("dispatched=%d, want 50", got)
	}
}

// TestDatabaseDriver_DispatchUnderLockNoDeadlock is the database analogue:
// dispatchEvent must never grab d.mu, so listeners can reinstall themselves
// without deadlocking.
func TestDatabaseDriver_DispatchUnderLockNoDeadlock(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	var dispatched atomic.Int64
	var listener func(context.Context, interface{}) error
	listener = func(_ context.Context, _ interface{}) error {
		driver.SetEventDispatcher(listener)
		dispatched.Add(1)
		return nil
	}
	driver.SetEventDispatcher(listener)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			if err := driver.PushCtx(context.Background(), &TestJob{ID: "x", Message: "y"}, "dispatch-under-lock"); err != nil {
				t.Errorf("push: %v", err)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PushCtx deadlocked while dispatching")
	}

	if got := dispatched.Load(); got != 20 {
		t.Fatalf("dispatched=%d, want 20", got)
	}
}

// TestRedisDriver_DispatchUnderLockNoDeadlock proves the Redis driver's
// dispatcher path is lock-free, so listeners can reinstall themselves
// without deadlock.
func TestRedisDriver_DispatchUnderLockNoDeadlock(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer mr.Close()

	driver, err := NewRedisDriver(RedisConfig{
		Host: mr.Host(),
		Port: mr.Port(),
		DB:   "0",
	})
	if err != nil {
		t.Fatalf("new redis driver: %v", err)
	}
	defer driver.Shutdown(context.Background())

	var dispatched atomic.Int64
	var listener func(context.Context, interface{}) error
	listener = func(_ context.Context, _ interface{}) error {
		driver.SetEventDispatcher(listener)
		dispatched.Add(1)
		return nil
	}
	driver.SetEventDispatcher(listener)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			if err := driver.PushCtx(context.Background(), &TestJob{ID: "x", Message: "y"}, "dispatch-under-lock"); err != nil {
				t.Errorf("push: %v", err)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PushCtx deadlocked while dispatching")
	}

	if got := dispatched.Load(); got != 20 {
		t.Fatalf("dispatched=%d, want 20", got)
	}
}

// TestRedisDriver_DispatcherRace hammers SetEventDispatcher concurrently
// with PushCtx on the Redis driver backed by miniredis.
func TestRedisDriver_DispatcherRace(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer mr.Close()

	driver, err := NewRedisDriver(RedisConfig{
		Host: mr.Host(),
		Port: mr.Port(),
		DB:   "0",
	})
	if err != nil {
		t.Fatalf("new redis driver: %v", err)
	}
	defer driver.Shutdown(context.Background())

	const (
		setters = 8
		pushers = 8
		iters   = 100
	)

	var calls atomic.Int64
	makeFn := func() func(context.Context, interface{}) error {
		return func(_ context.Context, _ interface{}) error {
			calls.Add(1)
			return nil
		}
	}

	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < setters; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			for j := 0; j < iters; j++ {
				if (idx+j)%2 == 0 {
					driver.SetEventDispatcher(makeFn())
				} else {
					driver.SetEventDispatcher(nil)
				}
			}
		}(i)
	}

	for i := 0; i < pushers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			for j := 0; j < iters; j++ {
				job := &TestJob{ID: "race", Message: "race"}
				_ = driver.PushCtx(context.Background(), job, "race-queue")
			}
		}(i)
	}

	close(start)
	wg.Wait()
}
