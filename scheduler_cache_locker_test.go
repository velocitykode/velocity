package velocity

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/scheduler"
)

// newSharedMemoryCache builds a single *cache.Manager backed by an
// in-memory store. Used as the "shared backend" stand-in for tests --
// the cache.MemoryStore IS the contended state across two scheduler
// instances pointed at the same Manager (mirrors the multi-host Redis
// case where both hosts dial the same Redis instance).
func newSharedMemoryCache(t *testing.T) *cache.Manager {
	t.Helper()
	cm := cache.NewManager(&cache.Config{
		Default: "default",
		Stores: map[string]cache.StoreConfig{
			"default": {Driver: "memory"},
		},
	})
	if cm == nil {
		t.Fatal("cache.NewManager returned nil")
	}
	return cm
}

// runSchedulerOnce drives a *scheduler.Scheduler through exactly one
// runDueJobs evaluation via the public Run() entry point. Run() invokes
// runDueJobs synchronously before starting its ticker loop; cancelling
// the context immediately afterwards triggers Shutdown, which blocks
// until all in-flight job goroutines (tracked by the scheduler's
// internal runWg) have finished. This is the externally-observable
// equivalent of calling runDueJobs + runWg.Wait() that the in-package
// tests use directly.
func runSchedulerOnce(t *testing.T, s *scheduler.Scheduler) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Run(ctx)
	}()
	// Give Run() time to dispatch the initial runDueJobs invocation +
	// its in-flight goroutines. The jobs themselves are atomic counter
	// bumps, well under 100ms even on a loaded CI host.
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Scheduler.Run did not return after cancel")
	}
}

// TestCacheLocker_TwoSchedulers_OnOneServer_OnlyOneFires is the C-04
// follow-up regression test: a stock multi-host deployment that shares a
// cache backend (Redis in production; here a single in-memory
// *cache.Manager standing in for any shared backend) must elect a
// single winner per scheduled minute for OnOneServer() jobs. Pre-fix
// (cacheLocker missing) BOTH schedulers fired because each defaulted to
// its own process-local scheduler.InMemoryLocker.
func TestCacheLocker_TwoSchedulers_OnOneServer_OnlyOneFires(t *testing.T) {
	t.Parallel()

	shared := newSharedMemoryCache(t)
	lockerA := newCacheLocker(shared)
	lockerB := newCacheLocker(shared)
	if lockerA == nil || lockerB == nil {
		t.Fatal("newCacheLocker returned nil for valid cache.Manager")
	}

	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())

	var counter atomic.Int32
	work := func() { counter.Add(1) }

	hostA := scheduler.New()
	hostA.SetLocker(lockerA)
	hostA.Named("billing.run", work).Cron(cron).OnOneServer()

	hostB := scheduler.New()
	hostB.SetLocker(lockerB)
	hostB.Named("billing.run", work).Cron(cron).OnOneServer()

	// Concurrent dispatch maximizes the contention surface. Pre-fix:
	// both fire (no cross-process lock); post-fix: exactly one fires.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); runSchedulerOnce(t, hostA) }()
	go func() { defer wg.Done(); runSchedulerOnce(t, hostB) }()
	wg.Wait()

	if got := counter.Load(); got != 1 {
		t.Fatalf("expected exactly 1 execution across both hosts sharing one cache, got %d", got)
	}
}

// TestCacheLocker_FencingTokenMonotonic verifies the contract documented
// on scheduler.Lock: every successful Acquire returns a strictly
// increasing token. The cache backend cannot provide this directly, so
// the adapter tracks tokens itself via a package-level atomic counter;
// this test pins that contract so a future refactor cannot break it.
func TestCacheLocker_FencingTokenMonotonic(t *testing.T) {
	t.Parallel()

	cm := newSharedMemoryCache(t)
	l := newCacheLocker(cm)

	ctx := context.Background()
	a, err := l.Acquire(ctx, "lockA", time.Minute)
	if err != nil {
		t.Fatalf("acquire A: %v", err)
	}
	b, err := l.Acquire(ctx, "lockB", time.Minute)
	if err != nil {
		t.Fatalf("acquire B: %v", err)
	}
	if b.FencingToken() <= a.FencingToken() {
		t.Fatalf("fencing token must be strictly increasing; A=%d B=%d", a.FencingToken(), b.FencingToken())
	}

	// Release + re-acquire same name: new token must still advance.
	if err := a.Release(ctx); err != nil {
		t.Fatalf("release A: %v", err)
	}
	c, err := l.Acquire(ctx, "lockA", time.Minute)
	if err != nil {
		t.Fatalf("re-acquire A: %v", err)
	}
	if c.FencingToken() <= b.FencingToken() {
		t.Fatalf("fencing token must advance across same-name re-acquire; B=%d C=%d", b.FencingToken(), c.FencingToken())
	}
}

// TestCacheLocker_AcquireReportsContention verifies the contended path
// surfaces scheduler.ErrLockHeld (not a generic error) so the scheduler
// can distinguish "skip this tick" from "misconfiguration".
func TestCacheLocker_AcquireReportsContention(t *testing.T) {
	t.Parallel()

	cm := newSharedMemoryCache(t)
	l := newCacheLocker(cm)
	ctx := context.Background()

	if _, err := l.Acquire(ctx, "contended", time.Minute); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	_, err := l.Acquire(ctx, "contended", time.Minute)
	if err == nil {
		t.Fatal("expected ErrLockHeld on contested acquire")
	}
	if !errors.Is(err, scheduler.ErrLockHeld) {
		t.Fatalf("expected wrapped scheduler.ErrLockHeld, got %v", err)
	}
}

// TestCacheLocker_ReleaseIsIdempotent verifies the documented contract:
// a second Release is a no-op (no error). The deferred-release path in
// scheduler.runDueJobs relies on this so retries during shutdown do not
// surface spurious errors.
func TestCacheLocker_ReleaseIsIdempotent(t *testing.T) {
	t.Parallel()

	cm := newSharedMemoryCache(t)
	l := newCacheLocker(cm)
	ctx := context.Background()

	lk, err := l.Acquire(ctx, "idempotent", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := lk.Release(ctx); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := lk.Release(ctx); err != nil {
		t.Fatalf("second release must be a no-op, got %v", err)
	}
}

// TestInstallSchedulerLocker_MemoryDriverLeavesDefault asserts the
// documented carve-out: memory cache driver retains InMemoryLocker
// (same scope as the cache, swapping in an adapter buys nothing).
func TestInstallSchedulerLocker_MemoryDriverLeavesDefault(t *testing.T) {
	t.Parallel()

	cm := newSharedMemoryCache(t)
	sched := scheduler.New()
	beforeType := reflect.TypeOf(sched.Locker()).String()

	installSchedulerLocker(sched, cm, "memory")

	afterType := reflect.TypeOf(sched.Locker()).String()
	if beforeType != afterType {
		t.Fatalf("memory driver must leave the default Locker in place; before=%s after=%s", beforeType, afterType)
	}
	if afterType != "*scheduler.InMemoryLocker" {
		t.Fatalf("expected *scheduler.InMemoryLocker default, got %s", afterType)
	}
}

// TestInstallSchedulerLocker_NonMemoryDriverInstallsAdapter verifies the
// positive case: when the configured cache driver is anything other
// than "memory" (redis, file, database), the scheduler's Locker is
// replaced with the cache-backed adapter. The driver name is the only
// thing the installer inspects -- the actual backend connection is not
// required for this assertion.
func TestInstallSchedulerLocker_NonMemoryDriverInstallsAdapter(t *testing.T) {
	t.Parallel()

	for _, driver := range []string{"redis", "file", "database"} {
		driver := driver
		t.Run(driver, func(t *testing.T) {
			t.Parallel()
			cm := newSharedMemoryCache(t)
			sched := scheduler.New()
			installSchedulerLocker(sched, cm, driver)

			got := reflect.TypeOf(sched.Locker()).String()
			if got == "*scheduler.InMemoryLocker" {
				t.Fatalf("driver %q must install cacheLocker; got InMemoryLocker still", driver)
			}
			if got != "*velocity.cacheLocker" {
				t.Fatalf("driver %q expected *velocity.cacheLocker, got %s", driver, got)
			}
		})
	}
}

// TestInstallSchedulerLocker_NilArgsAreSafe pins the no-op behaviour on
// nil inputs. installSchedulerLocker is called unconditionally during
// app bootstrap; a nil cache (cache disabled) or nil scheduler (never
// constructed) must not panic.
func TestInstallSchedulerLocker_NilArgsAreSafe(t *testing.T) {
	t.Parallel()

	// All three call shapes must not panic.
	installSchedulerLocker(nil, nil, "redis")
	installSchedulerLocker(scheduler.New(), nil, "redis")
	installSchedulerLocker(nil, newSharedMemoryCache(t), "redis")
}
