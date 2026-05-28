// Package schedulertest provides executable specifications (contract tests)
// for [scheduler.Locker] implementations.
//
// The framework ships [scheduler.InMemoryLocker] for single-instance
// deployments; distributed lockers (Redis SET NX, ZooKeeper, etcd, database
// advisory locks) plug into the same contract. Every implementation must
// pass this runner.
package schedulertest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/scheduler"
)

// LockerFactory returns a fresh Locker per sub-test.
type LockerFactory func(t *testing.T) scheduler.Locker

// RunLockerContractTests is the executable specification of
// [scheduler.Locker].
func RunLockerContractTests(t *testing.T, factory LockerFactory) {
	t.Helper()

	t.Run("Acquire_FreshName_ReturnsLock", func(t *testing.T) {
		l := factory(t)
		lock, err := l.Acquire(context.Background(), "contract-acquire", time.Second)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		if lock == nil {
			t.Fatal("expected non-nil lock")
		}
		if lock.Name() != "contract-acquire" {
			t.Fatalf("Name mismatch: %q", lock.Name())
		}
		_ = lock.Release(context.Background())
	})

	t.Run("Acquire_Held_ReturnsErrLockHeld", func(t *testing.T) {
		l := factory(t)
		ctx := context.Background()
		first, err := l.Acquire(ctx, "contract-contention", time.Minute)
		if err != nil {
			t.Fatalf("first Acquire: %v", err)
		}
		t.Cleanup(func() { _ = first.Release(ctx) })

		_, err = l.Acquire(ctx, "contract-contention", time.Minute)
		if err == nil {
			t.Fatal("expected Acquire on held lock to error, got nil")
		}
		if !errors.Is(err, scheduler.ErrLockHeld) {
			t.Fatalf("expected ErrLockHeld, got %v", err)
		}
	})

	t.Run("Release_AllowsReacquire", func(t *testing.T) {
		l := factory(t)
		ctx := context.Background()
		lock, err := l.Acquire(ctx, "contract-reacquire", time.Minute)
		if err != nil {
			t.Fatalf("first Acquire: %v", err)
		}
		if err := lock.Release(ctx); err != nil {
			t.Fatalf("Release: %v", err)
		}
		lock2, err := l.Acquire(ctx, "contract-reacquire", time.Minute)
		if err != nil {
			t.Fatalf("reacquire: %v", err)
		}
		_ = lock2.Release(ctx)
	})

	t.Run("Release_Idempotent", func(t *testing.T) {
		l := factory(t)
		ctx := context.Background()
		lock, err := l.Acquire(ctx, "contract-idempotent-release", time.Minute)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		if err := lock.Release(ctx); err != nil {
			t.Fatalf("first Release: %v", err)
		}
		// Second Release must not error.
		if err := lock.Release(ctx); err != nil {
			t.Fatalf("second Release must be idempotent, got %v", err)
		}
	})

	t.Run("FencingToken_StrictlyIncreasing_PerName", func(t *testing.T) {
		l := factory(t)
		ctx := context.Background()
		// scheduler.Lock only promises strictly-increasing fencing
		// tokens for the SAME lock name; backends that maintain per-name
		// counters (rather than a single global counter) are conformant.
		// Acquire + release the same name twice and assert the second
		// token exceeds the first.
		a, err := l.Acquire(ctx, "contract-fence-same", time.Minute)
		if err != nil {
			t.Fatalf("first Acquire: %v", err)
		}
		tokA := a.FencingToken()
		if err := a.Release(ctx); err != nil {
			t.Fatalf("Release a: %v", err)
		}
		b, err := l.Acquire(ctx, "contract-fence-same", time.Minute)
		if err != nil {
			t.Fatalf("second Acquire: %v", err)
		}
		t.Cleanup(func() { _ = b.Release(ctx) })
		if b.FencingToken() <= tokA {
			t.Fatalf("fencing token did not increase for same name: %d -> %d", tokA, b.FencingToken())
		}
	})

	t.Run("Acquire_ExpiredLease_AllowsReclaim", func(t *testing.T) {
		l := factory(t)
		ctx := context.Background()
		// Acquire with a short lease, then wait for it to expire.
		_, err := l.Acquire(ctx, "contract-reclaim", 30*time.Millisecond)
		if err != nil {
			t.Fatalf("first Acquire: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
		// Reclaim must succeed: previous holder's lease is gone.
		lock2, err := l.Acquire(ctx, "contract-reclaim", time.Minute)
		if err != nil {
			t.Fatalf("expected reclaim after lease expiry, got %v", err)
		}
		_ = lock2.Release(ctx)
	})

	t.Run("Acquire_Concurrent_AtMostOneConcurrentWinner", func(t *testing.T) {
		l := factory(t)
		ctx := context.Background()
		const racers = 16

		// active tracks the count of holders currently inside the
		// critical section. The mutual-exclusion invariant is
		// "active never exceeds 1". A broken locker that granted every
		// racer simultaneously would push active to 16 and the
		// assertion below would fire.
		var active atomic.Int32
		var maxActive atomic.Int32
		var wins atomic.Int32
		var concurrentViolations atomic.Int32

		var wg sync.WaitGroup
		wg.Add(racers)
		for i := 0; i < racers; i++ {
			go func() {
				defer wg.Done()
				lk, err := l.Acquire(ctx, "contract-race", time.Minute)
				if err != nil {
					return
				}
				wins.Add(1)
				inside := active.Add(1)
				if inside > 1 {
					concurrentViolations.Add(1)
				}
				// Track the high-water mark.
				for {
					prev := maxActive.Load()
					if inside <= prev || maxActive.CompareAndSwap(prev, inside) {
						break
					}
				}
				// Hold briefly so a buggy locker has time to grant a
				// concurrent winner and trip the inside>1 check.
				time.Sleep(10 * time.Millisecond)
				active.Add(-1)
				_ = lk.Release(ctx)
			}()
		}
		wg.Wait()

		if got := maxActive.Load(); got > 1 {
			t.Fatalf("mutual exclusion violated: %d holders observed concurrently (violations=%d)",
				got, concurrentViolations.Load())
		}
		// Sanity: at least one winner. A locker that never grants the
		// lock would also pass the mutex invariant trivially.
		if wins.Load() < 1 {
			t.Fatal("expected at least one Acquire to succeed under contention")
		}
	})
}
