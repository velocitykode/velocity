package velocity

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/contract"
)

// newCacheProbe decorates the cache s holds now with a counting probe.
func newCacheProbe(s *app.Services, forward bool) *cacheProbe {
	return &cacheProbe{CacheManager: s.Cache, rp: &retireProbe{inner: s.Cache, forward: forward}}
}

// chainSwap adds a chain module running fn in its Start and bootstraps a.
func chainSwap(t *testing.T, a *App, fn func(s *app.Services)) error {
	t.Helper()
	a.Modules(func(r *chain.ModuleRegistry) { r.Add(swapIn(fn)) })
	return a.Bootstrap()
}

// shrinkRetireWaits makes every boundary and unwind wait at most d for
// retirements.
func shrinkRetireWaits(t *testing.T, d time.Duration) {
	grace, unwind := retireGrace, unwindRetireTimeout
	retireGrace, unwindRetireTimeout = d, d
	t.Cleanup(func() { retireGrace, unwindRetireTimeout = grace, unwind })
}

// recordingErrors installs a recording reporter on a's error handler.
func recordingErrors(a *App) *recordingReporter {
	r := &recordingReporter{}
	a.Services.Errors.SetReporters(r)
	return r
}

func TestRebind_SameInstanceReassignedClosesNothing(t *testing.T) {
	var p *cacheProbe
	a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) { p = newCacheProbe(s, true); s.Cache = p })))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := chainSwap(t, a, func(s *app.Services) { s.Cache = p }); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if got := p.rp.closes.Load(); got != 0 {
		t.Fatalf("re-assigned instance shut down %d times before Shutdown", got)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := p.rp.closes.Load(); got != 1 {
		t.Fatalf("instance shut down %d times, want 1 (at Shutdown)", got)
	}
}

func TestRebind_PutBackRetiresNothing(t *testing.T) {
	var boot contract.CacheManager
	var b *cacheProbe
	a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) {
		boot = s.Cache
		b = newCacheProbe(s, false)
		s.Cache = b    // A -> B
		s.Cache = boot // B -> A
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shutdownApp(t, a)
	if len(a.retired.ids) != 0 {
		t.Fatalf("retired %v, want nothing", a.retired.ids)
	}
}

func TestRebind_ReplacedTwiceBetweenBoundariesLeavesTheMiddleOne(t *testing.T) {
	var b, c *cacheProbe
	a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) {
		b = newCacheProbe(s, false)
		s.Cache = b
		c = &cacheProbe{CacheManager: b.CacheManager, rp: &retireProbe{inner: b.CacheManager, forward: true}}
		s.Cache = c
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := b.rp.closes.Load(); got != 0 {
		t.Fatalf("the middle replacement was shut down %d times; it is the module's", got)
	}
	if got := c.rp.closes.Load(); got != 1 {
		t.Fatalf("the final replacement was shut down %d times, want 1", got)
	}
}

func TestRebind_RegistryAliasIsKept(t *testing.T) {
	var p *cacheProbe
	a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) {
		p = newCacheProbe(s, false)
		s.Cache = p
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = chainSwap(t, a, func(s *app.Services) {
		if err := app.Register(s, p); err != nil {
			t.Errorf("Register: %v", err)
		}
		s.Cache = &cacheProbe{CacheManager: p.CacheManager, rp: &retireProbe{inner: p.CacheManager, forward: true}}
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if got := p.rp.closes.Load(); got != 0 {
		t.Fatalf("an instance the registry holds was retired (%d closes)", got)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := p.rp.closes.Load(); got != 1 {
		t.Fatalf("registered instance shut down %d times, want 1 (by the registry sweep)", got)
	}
}

// panicUnwrapCache's Unwrap panics.
type panicUnwrapCache struct{ *cacheProbe }

func (panicUnwrapCache) Unwrap() contract.CacheManager { panic("unwrap boom") }

// cyclicCache's Unwrap chain returns to itself.
type cyclicCache struct {
	*cacheProbe
	next *cyclicCache
}

func (c *cyclicCache) Unwrap() contract.CacheManager { return c.next }

// facadeCache is a registered component of a value type that cannot be
// compared (a map field). It uses inner, and exposes it through Unwrap
// when unwraps is set.
type facadeCache struct {
	tags    map[string]string
	inner   contract.CacheManager
	unwraps bool
}

func (f facadeCache) Unwrap() contract.CacheManager {
	if !f.unwraps {
		return nil
	}
	return f.inner
}

// A registered value that cannot be compared is still a holder: the
// instance its Unwrap returns is kept open, with nothing to report, and
// one whose Unwrap does not reach the displaced instance holds nothing.
func TestRebind_ANonComparableRegisteredWrapperKeepsItsInner(t *testing.T) {
	tests := []struct {
		name       string
		unwraps    bool
		wantCloses int32
	}{
		{"unwraps to the displaced instance", true, 0},
		{"does not reach it", false, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p *cacheProbe
			a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) {
				p = newCacheProbe(s, false)
				s.Cache = p
			})))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			rec := recordingErrors(a)
			err = chainSwap(t, a, func(s *app.Services) {
				if err := app.Register(s, facadeCache{tags: map[string]string{}, inner: p, unwraps: tt.unwraps}); err != nil {
					t.Errorf("Register: %v", err)
				}
				s.Cache = &cacheProbe{CacheManager: p.CacheManager, rp: &retireProbe{inner: p.CacheManager, forward: true}}
			})
			if err != nil {
				t.Fatalf("Bootstrap: %v", err)
			}
			if got := p.rp.closes.Load(); got != tt.wantCloses {
				t.Fatalf("displaced instance closed %d times at the boundary, want %d", got, tt.wantCloses)
			}
			if got := rec.count(); got != 0 {
				t.Fatalf("reported %d times, want none: %v", got, rec.errs)
			}
			if err := a.Shutdown(context.Background()); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
		})
	}
}

func TestRebind_AnUnwrapChainThatCannotBeFollowedKeepsTheInstance(t *testing.T) {
	tests := []struct {
		name string
		wrap func(inner *cacheProbe) contract.CacheManager
	}{
		{"panicking", func(inner *cacheProbe) contract.CacheManager {
			return panicUnwrapCache{&cacheProbe{CacheManager: inner.CacheManager, rp: &retireProbe{inner: inner.CacheManager, forward: true}}}
		}},
		{"cyclic", func(inner *cacheProbe) contract.CacheManager {
			x := &cyclicCache{cacheProbe: &cacheProbe{CacheManager: inner.CacheManager, rp: &retireProbe{inner: inner.CacheManager, forward: true}}}
			y := &cyclicCache{cacheProbe: x.cacheProbe, next: x}
			x.next = y
			return x
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p *cacheProbe
			a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) { p = newCacheProbe(s, false); s.Cache = p })))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			rec := recordingErrors(a)
			if err := chainSwap(t, a, func(s *app.Services) { s.Cache = tt.wrap(p) }); err != nil {
				t.Fatalf("Bootstrap: %v", err)
			}
			if err := a.Shutdown(context.Background()); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
			if got := p.rp.closes.Load(); got != 0 {
				t.Fatalf("displaced instance closed %d times under a wrapper whose chain could not be followed", got)
			}
			if got := rec.count(); got != 1 {
				t.Fatalf("reported %d times, want once: %v", got, rec.errs)
			}
			if !strings.Contains(rec.errs[0].Error(), "Services.Cache") {
				t.Fatalf("report does not name the field: %v", rec.errs[0])
			}
		})
	}
}

// panickingCache's Shutdown panics.
type panickingCache struct{ *cacheProbe }

func (p panickingCache) Shutdown(context.Context) error {
	p.rp.closes.Add(1)
	panic("shutdown boom")
}

func TestRebind_APanickingDisplacedShutdownIsContainedAndReportedOnce(t *testing.T) {
	var p panickingCache
	a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) {
		p = panickingCache{newCacheProbe(s, false)}
		s.Cache = p
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := recordingErrors(a)
	if err := chainSwap(t, a, func(s *app.Services) {
		s.Cache = &cacheProbe{CacheManager: p.CacheManager, rp: &retireProbe{inner: p.CacheManager, forward: true}}
	}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := p.rp.closes.Load(); got != 1 {
		t.Fatalf("displaced Shutdown ran %d times, want 1", got)
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("reported %d times, want once: %v", got, rec.errs)
	}
}

// blockingCache's Shutdown blocks until release closes.
type blockingCache struct {
	*cacheProbe
	entered chan struct{}
	release chan struct{}
}

func (b *blockingCache) Shutdown(context.Context) error {
	b.rp.closes.Add(1)
	close(b.entered)
	<-b.release
	return nil
}

func TestRebind_ABlockingDisplacedShutdownIsBoundedAndAwaitedByShutdown(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	shrinkRetireWaits(t, 10*time.Millisecond)
	var b *blockingCache
	a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) {
		b = &blockingCache{cacheProbe: newCacheProbe(s, false), entered: make(chan struct{}), release: make(chan struct{})}
		s.Cache = b
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The boundary returns while the displaced Shutdown blocks.
	if err := chainSwap(t, a, func(s *app.Services) {
		s.Cache = &cacheProbe{CacheManager: b.CacheManager, rp: &retireProbe{inner: b.CacheManager, forward: true}}
	}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if !containsIdentity(a.retired.ids, any(contract.CacheManager(b))) {
		t.Fatal("the boundary did not retire the displaced instance")
	}
	<-b.entered
	// Shutdown waits for it within its ctx.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.awaitRetired(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("awaitRetired with the retirement blocked = %v, want the ctx error", err)
	}
	close(b.release)
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := b.rp.closes.Load(); got != 1 {
		t.Fatalf("displaced Shutdown ran %d times, want 1", got)
	}
}

func TestRebind_ADisplacedDatabaseClosesAfterTheQueue(t *testing.T) {
	var order []string
	var db *dbProbe
	cfg := testAppConfig()
	cfg.DB = DBConfig{Connection: "sqlite", Database: ":memory:"}
	a, err := New(WithConfig(cfg), WithModules(swapIn(func(s *app.Services) {
		db = &dbProbe{Database: s.DB, rp: &retireProbe{inner: s.DB}}
		s.DB = db
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := chainSwap(t, a, func(s *app.Services) {
		s.DB = &dbProbe{Database: db.Database, rp: &retireProbe{inner: db.Database, forward: true}}
		s.Queue = &orderQueue{QueueDriver: s.Queue, order: &order, db: db}
	}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if got := db.rp.closes.Load(); got != 0 {
		t.Fatalf("displaced database closed %d times before Shutdown: the queue still borrows its *sql.DB", got)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := db.rp.closes.Load(); got != 1 {
		t.Fatalf("displaced database closed %d times, want 1", got)
	}
	if len(order) != 1 || order[0] != "queue closed with the displaced database open" {
		t.Fatalf("order = %v", order)
	}
}

// orderQueue records whether the displaced database was still open when
// the queue closed.
type orderQueue struct {
	contract.QueueDriver
	order *[]string
	db    *dbProbe
}

func (q *orderQueue) Shutdown(ctx context.Context) error {
	if q.db.rp.closes.Load() == 0 {
		*q.order = append(*q.order, "queue closed with the displaced database open")
	} else {
		*q.order = append(*q.order, "queue closed after the displaced database")
	}
	return q.QueueDriver.Shutdown(ctx)
}

func TestRebind_ModuleFailureAfterASwapRetiresWhatItDisplaced(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	boot := &mailProbe{Mailer: &countingMailer{}, rp: &retireProbe{}}
	boom := errors.New("start failed")
	_, err := NewTestApp(WithFakeMail(boot), WithModules(swapThenFail(func(s *app.Services) {
		s.Mail = &countingMailer{}
	}, boom)))
	if !errors.Is(err, boom) {
		t.Fatalf("New = %v, want the module's failure", err)
	}
	if got := boot.rp.closes.Load(); got != 1 {
		t.Fatalf("displaced mailer shut down %d times by the failed New, want 1", got)
	}
}

func TestRebind_BootstrapFailureAfterASwapRetiresWhatItDisplaced(t *testing.T) {
	var p *cacheProbe
	a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) { p = newCacheProbe(s, false); s.Cache = p })))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	boom := errors.New("start failed")
	a.Modules(func(r *chain.ModuleRegistry) {
		r.Add(swapThenFail(func(s *app.Services) {
			s.Cache = &cacheProbe{CacheManager: p.CacheManager, rp: &retireProbe{inner: p.CacheManager, forward: true}}
		}, boom))
	})
	if err := a.Bootstrap(); !errors.Is(err, boom) {
		t.Fatalf("Bootstrap = %v, want the module's failure", err)
	}
	if got := p.rp.closes.Load(); got != 1 {
		t.Fatalf("displaced instance shut down %d times by the failed bootstrap, want 1", got)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := p.rp.closes.Load(); got != 1 {
		t.Fatalf("displaced instance shut down %d times, want 1", got)
	}
}

func TestRebind_ARetiredInstanceReassignedIsRefused(t *testing.T) {
	var p *cacheProbe
	a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) { p = newCacheProbe(s, false); s.Cache = p })))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shutdownApp(t, a)
	var displaced contract.CacheManager = p
	if err := chainSwap(t, a, func(s *app.Services) {
		s.Cache = &cacheProbe{CacheManager: p.CacheManager, rp: &retireProbe{inner: p.CacheManager, forward: true}}
	}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	// A later boundary seeing the retired instance in a field refuses it.
	a.Services.Cache = displaced
	if err := rebind(a); err == nil || !strings.Contains(err.Error(), "Services.Cache: retired instance reassigned") {
		t.Fatalf("rebind = %v, want the refusal naming the field", err)
	}
	a.Services.Cache = p.CacheManager
}

func TestCloseBorrowedRetired_ClosesOnceAndRetainsTheResult(t *testing.T) {
	boom := errors.New("close failed")
	db := &dbProbe{rp: &retireProbe{}}
	failing := &failingDB{dbProbe: db, err: boom}
	a := &App{}
	a.retired.borrowed = []any{failing}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = a.closeBorrowedRetired(context.Background())
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if !errors.Is(err, boom) {
			t.Fatalf("call %d = %v, want the first call's result", i, err)
		}
	}
	if got := db.rp.closes.Load(); got != 1 {
		t.Fatalf("displaced database closed %d times, want 1", got)
	}
}

// failingDB's Shutdown counts and fails.
type failingDB struct {
	*dbProbe
	err error
}

func (f *failingDB) Shutdown(ctx context.Context) error {
	_ = f.dbProbe.Shutdown(ctx)
	return f.err
}
