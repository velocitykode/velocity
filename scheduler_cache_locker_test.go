package velocity

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
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

// captureLogger collects Warn calls for assertion. Implements
// installerLogger via the structural Warn(string, ...any) shape; no
// dependency on the log package.
type captureLogger struct {
	mu    sync.Mutex
	warns []capturedWarn
}

type capturedWarn struct {
	msg string
	kvs []any
}

func (c *captureLogger) Warn(msg string, kvs ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.warns = append(c.warns, capturedWarn{msg: msg, kvs: append([]any(nil), kvs...)})
}

func (c *captureLogger) Warns() []capturedWarn {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]capturedWarn, len(c.warns))
	copy(out, c.warns)
	return out
}

// newFileBackedCache builds a *cache.Manager whose default store is the
// FileStore driver. Used to exercise the capability-detection fallback:
// FileStore does NOT implement the cache Lock primitive, so
// installSchedulerLocker must NOT install cacheLocker for this cache.
func newFileBackedCache(t *testing.T) *cache.Manager {
	t.Helper()
	dir := t.TempDir()
	cm := cache.NewManager(&cache.Config{
		Default: "default",
		Stores: map[string]cache.StoreConfig{
			"default": {Driver: "file", Path: dir},
		},
	})
	if cm == nil {
		t.Fatal("cache.NewManager returned nil for file driver")
	}
	// Force store materialisation up-front so a subsequent
	// installSchedulerLocker probe does not race on initial creation.
	if _, err := cm.DefaultStore(); err != nil {
		t.Fatalf("file-backed default store: %v", err)
	}
	return cm
}

// newDatabaseBackedCache builds a *cache.Manager configured for the
// "database" driver. The factory currently returns "not yet
// implemented", so DefaultStore() errors. Used to exercise the
// fallback's error-path branch.
func newDatabaseBackedCache(t *testing.T) *cache.Manager {
	t.Helper()
	cm := cache.NewManager(&cache.Config{
		Default: "default",
		Stores: map[string]cache.StoreConfig{
			"default": {Driver: "database"},
		},
	})
	if cm == nil {
		t.Fatal("cache.NewManager returned nil for database driver")
	}
	return cm
}

// TestInstallSchedulerLocker_MemoryDriverLeavesDefault asserts the
// documented carve-out: memory cache driver retains InMemoryLocker
// (same scope as the cache, swapping in an adapter buys nothing). No
// WARN should fire on this path -- memory is the supported dev default.
func TestInstallSchedulerLocker_MemoryDriverLeavesDefault(t *testing.T) {
	t.Parallel()

	cm := newSharedMemoryCache(t)
	sched := scheduler.New()
	beforeType := reflect.TypeOf(sched.Locker()).String()
	logger := &captureLogger{}

	installSchedulerLocker(sched, cm, "memory", logger)

	afterType := reflect.TypeOf(sched.Locker()).String()
	if beforeType != afterType {
		t.Fatalf("memory driver must leave the default Locker in place; before=%s after=%s", beforeType, afterType)
	}
	if afterType != "*scheduler.InMemoryLocker" {
		t.Fatalf("expected *scheduler.InMemoryLocker default, got %s", afterType)
	}
	if got := logger.Warns(); len(got) != 0 {
		t.Fatalf("memory driver path must not WARN; got %d warnings: %+v", len(got), got)
	}
}

// TestInstallSchedulerLocker_RedisDriverInstallsCacheLocker pins the
// positive case: a cache backed by a lockCapable store (we use
// MemoryStore here because the test does not need a real Redis -- the
// memory driver satisfies the same Lock(...)/RestoreLock(...) shape
// the type assertion checks for) plus a non-memory driver name must
// install cacheLocker, with no WARN.
//
// Production deployments substitute Redis for the MemoryStore; the
// installer's capability decision is the same.
func TestInstallSchedulerLocker_RedisDriverInstallsCacheLocker(t *testing.T) {
	t.Parallel()

	cm := newSharedMemoryCache(t)
	sched := scheduler.New()
	logger := &captureLogger{}

	installSchedulerLocker(sched, cm, "redis", logger)

	got := reflect.TypeOf(sched.Locker()).String()
	if got == "*scheduler.InMemoryLocker" {
		t.Fatal("lockCapable store must install cacheLocker; got InMemoryLocker still")
	}
	if got != "*velocity.cacheLocker" {
		t.Fatalf("expected *velocity.cacheLocker, got %s", got)
	}
	if w := logger.Warns(); len(w) != 0 {
		t.Fatalf("lock-capable cache must not WARN; got %d warnings: %+v", len(w), w)
	}
}

// TestInstallSchedulerLocker_FileDriverFallsBackToInMemory is the C-04
// follow-up regression: a fresh CACHE_DRIVER=file deployment must NOT
// install cacheLocker (FileStore does not implement Lock; cacheLocker
// would surface a misconfiguration error and the scheduler would
// silently skip every guarded job). installSchedulerLocker must
// instead leave the InMemoryLocker default in place AND emit a WARN
// so multi-host operators see a loud signal.
func TestInstallSchedulerLocker_FileDriverFallsBackToInMemory(t *testing.T) {
	t.Parallel()

	cm := newFileBackedCache(t)
	sched := scheduler.New()
	logger := &captureLogger{}

	installSchedulerLocker(sched, cm, "file", logger)

	got := reflect.TypeOf(sched.Locker()).String()
	if got != "*scheduler.InMemoryLocker" {
		t.Fatalf("file driver lacks Lock support; must fall back to InMemoryLocker, got %s", got)
	}
	warns := logger.Warns()
	if len(warns) != 1 {
		t.Fatalf("expected exactly 1 WARN on file driver fallback, got %d: %+v", len(warns), warns)
	}
	if !strings.Contains(warns[0].msg, "does not support distributed locks") {
		t.Fatalf("WARN message did not name the capability gap; got %q", warns[0].msg)
	}
	// The driver name must appear in the kvs so the warning is
	// actionable.
	found := false
	for i := 0; i+1 < len(warns[0].kvs); i += 2 {
		if k, _ := warns[0].kvs[i].(string); k == "driver" {
			if v, _ := warns[0].kvs[i+1].(string); v == "file" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("WARN kvs missing driver=file; got %+v", warns[0].kvs)
	}
}

// TestInstallSchedulerLocker_DatabaseDriverFallsBackToInMemory covers
// the related case where the cache driver factory itself returns an
// error (database driver is registered but currently returns "not yet
// implemented"). installSchedulerLocker's DefaultStore call fails, so
// the error-branch WARN fires and InMemoryLocker is retained.
func TestInstallSchedulerLocker_DatabaseDriverFallsBackToInMemory(t *testing.T) {
	t.Parallel()

	cm := newDatabaseBackedCache(t)
	sched := scheduler.New()
	logger := &captureLogger{}

	installSchedulerLocker(sched, cm, "database", logger)

	got := reflect.TypeOf(sched.Locker()).String()
	if got != "*scheduler.InMemoryLocker" {
		t.Fatalf("database driver must fall back to InMemoryLocker, got %s", got)
	}
	warns := logger.Warns()
	if len(warns) != 1 {
		t.Fatalf("expected exactly 1 WARN on database driver fallback, got %d: %+v", len(warns), warns)
	}
}

// TestInstallSchedulerLocker_NilLoggerIsSafe verifies that a nil
// installerLogger argument does not panic on the WARN paths -- the
// installer must remain robust against callers that have not yet
// constructed a logger (early bootstrap, test harnesses).
func TestInstallSchedulerLocker_NilLoggerIsSafe(t *testing.T) {
	t.Parallel()

	cm := newFileBackedCache(t)
	sched := scheduler.New()

	// Must not panic.
	installSchedulerLocker(sched, cm, "file", nil)

	if got := reflect.TypeOf(sched.Locker()).String(); got != "*scheduler.InMemoryLocker" {
		t.Fatalf("nil logger should still fall back; got %s", got)
	}
}

// TestInstallSchedulerLocker_NilArgsAreSafe pins the no-op behaviour on
// nil inputs. installSchedulerLocker is called unconditionally during
// app bootstrap; a nil cache (cache disabled) or nil scheduler (never
// constructed) must not panic.
func TestInstallSchedulerLocker_NilArgsAreSafe(t *testing.T) {
	t.Parallel()

	// All call shapes must not panic.
	logger := &captureLogger{}
	installSchedulerLocker(nil, nil, "redis", logger)
	installSchedulerLocker(scheduler.New(), nil, "redis", logger)
	installSchedulerLocker(nil, newSharedMemoryCache(t), "redis", logger)
}

// erringLock is a cache.Lock whose GetWithErr returns a configurable
// backend error. Used to drive the cacheLocker.Acquire backend-error
// path without depending on a real Redis instance.
type erringLock struct {
	err error
}

func (l *erringLock) Get(ctx context.Context) bool {
	acq, _ := l.GetWithErr(ctx)
	return acq
}
func (l *erringLock) GetWithErr(_ context.Context) (bool, error) { return false, l.err }
func (l *erringLock) Release(_ context.Context) bool             { return true }
func (l *erringLock) Run(_ context.Context, _ func()) error      { return l.err }
func (l *erringLock) Block(_ context.Context, _ time.Duration, _ func()) error {
	return l.err
}
func (l *erringLock) Owner() string                        { return "erring" }
func (l *erringLock) ForceRelease(_ context.Context) error { return l.err }

// errLockManager embeds *cache.Manager so it satisfies cache.CacheManager
// without re-stubbing 30 methods, then overrides Lock to return an
// erringLock. Used to exercise the backend-error branch of
// cacheLocker.Acquire.
type errLockManager struct {
	*cache.Manager
	backendErr error
}

func (m *errLockManager) Lock(_ string, _ ...time.Duration) cache.Lock {
	return &erringLock{err: m.backendErr}
}

// TestCacheLocker_Acquire_BackendErrorIsNotErrLockHeld is the canonical
// HIGH regression for C-04-fb4: a Redis backend error (network reset,
// AUTH failure, OOM) must NOT be wrapped as scheduler.ErrLockHeld.
// Without the fix, the scheduler treated every Redis outage as healthy
// contention and silently skipped every guarded job for the duration
// of the outage with no operator-visible signal.
func TestCacheLocker_Acquire_BackendErrorIsNotErrLockHeld(t *testing.T) {
	t.Parallel()

	backendErr := errors.New("redis: connection reset by peer")
	cm := &errLockManager{
		Manager:    newSharedMemoryCache(t),
		backendErr: backendErr,
	}

	l := newCacheLocker(cm)
	_, err := l.Acquire(context.Background(), "key", time.Minute)
	if err == nil {
		t.Fatal("expected error on backend failure, got nil")
	}
	if errors.Is(err, scheduler.ErrLockHeld) {
		t.Fatalf("backend error must NOT wrap ErrLockHeld; got %v", err)
	}
	if !errors.Is(err, backendErr) {
		t.Fatalf("expected wrapped backend error %q; got %v", backendErr, err)
	}
}

// TestCacheLocker_Acquire_ContentionIsErrLockHeld pins the converse
// case: an honest (false, nil) GetWithErr return must surface as
// ErrLockHeld so the scheduler's quiet-contention path fires (Debug
// log, no WARN).
func TestCacheLocker_Acquire_ContentionIsErrLockHeld(t *testing.T) {
	t.Parallel()

	cm := newSharedMemoryCache(t)
	l := newCacheLocker(cm)
	ctx := context.Background()

	// Acquire first to set up contention.
	if _, err := l.Acquire(ctx, "key", time.Minute); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	_, err := l.Acquire(ctx, "key", time.Minute)
	if err == nil {
		t.Fatal("expected ErrLockHeld on contested acquire")
	}
	if !errors.Is(err, scheduler.ErrLockHeld) {
		t.Fatalf("contention must wrap ErrLockHeld; got %v", err)
	}
}

// TestCacheLocker_FencingTokenDocumentedAsProcessLocal pins the
// documentation contract change (MEDIUM C-04-fb4): tokens are
// process-local and NOT safe for cross-process fencing. Two cacheLocker
// instances backed by the SAME cm produce overlapping token sequences
// because the underlying atomic counter resets on each process restart
// (here, on each test process). This test does not assert a specific
// number; it asserts that tokens are NOT a function of the shared
// backend's state, which is the property a cross-process fencing
// scheme would need.
func TestCacheLocker_FencingTokenDocumentedAsProcessLocal(t *testing.T) {
	t.Parallel()

	cm := newSharedMemoryCache(t)
	l1 := newCacheLocker(cm)
	l2 := newCacheLocker(cm)

	a, err := l1.Acquire(context.Background(), "k1", time.Minute)
	if err != nil {
		t.Fatalf("l1 acquire k1: %v", err)
	}
	b, err := l2.Acquire(context.Background(), "k2", time.Minute)
	if err != nil {
		t.Fatalf("l2 acquire k2: %v", err)
	}

	// Both came from the same package-level counter; ordering is
	// monotonic across the two adapters. The point of the assertion is
	// "tokens come from a shared in-process source," not from per-key
	// or per-backend state -- consistent with the documented
	// process-local semantic.
	if a.FencingToken() == b.FencingToken() {
		t.Fatalf("tokens from sequential acquires should differ; got %d == %d", a.FencingToken(), b.FencingToken())
	}
	// Both tokens should be small positive ints (counter starts from a
	// process-local zero, no cross-process state). We can't pin an
	// exact value because parallel tests share the counter, but we can
	// pin "well below uint64 max -- not derived from a hash or
	// timestamp."
	if a.FencingToken() > 1_000_000_000 {
		t.Errorf("unexpectedly large fencing token %d; expected process-local counter, not external state", a.FencingToken())
	}
}
