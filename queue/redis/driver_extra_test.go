package redis

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/velocitykode/velocity/queue"
)

// raceJob is a minimal data-only job fixture for the leaf's redis tests.
type raceJob struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

func (j *raceJob) Handle() error  { return nil }
func (j *raceJob) Failed(_ error) {}

// TestRedisDriver_ImplementsContracts is a compile-time check that the
// Redis driver implements the dedupe-aware and trace-aware contracts.
func TestRedisDriver_ImplementsContracts(t *testing.T) {
	var _ queue.DedupeAwarePusher = (*RedisDriver)(nil)
	var _ queue.TraceAwareDriver = (*RedisDriver)(nil)
	var _ queue.Driver = (*RedisDriver)(nil)
}

// TestRedisDriver_RegistryResolves asserts the "redis" factory self-registers
// when this leaf package is imported, so NewQueue / Drivers().Resolve can
// build a redis driver. The empty-config resolve hits a connection error
// rather than a driver-not-found error, which proves the factory is present.
func TestRedisDriver_RegistryResolves(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer mr.Close()

	d, err := queue.NewQueue(queue.QueueConfig{
		Driver: "redis",
		Redis:  queue.RedisConfig{Host: mr.Host(), Port: mr.Port(), DB: "0"},
	})
	if err != nil {
		t.Fatalf("resolve redis driver via registry: %v", err)
	}
	if d == nil {
		t.Fatal("registry returned a nil redis driver")
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

	driver, err := NewRedisDriver(queue.RedisConfig{
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
			if err := driver.PushCtx(context.Background(), &raceJob{ID: "x", Message: "y"}, "dispatch-under-lock"); err != nil {
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

	driver, err := NewRedisDriver(queue.RedisConfig{
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
				job := &raceJob{ID: "race", Message: "race"}
				_ = driver.PushCtx(context.Background(), job, "race-queue")
			}
		}(i)
	}

	close(start)
	wg.Wait()
}
