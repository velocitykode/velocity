package velocity

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
)

// blockingModule's Shutdown blocks, ignoring its ctx, until release is
// closed.
type blockingModule struct {
	entered   chan struct{}
	release   chan struct{}
	shutdowns atomic.Int32
}

func newBlockingModule() *blockingModule {
	return &blockingModule{entered: make(chan struct{}, 8), release: make(chan struct{})}
}

func (*blockingModule) Init(*app.Services) error  { return nil }
func (*blockingModule) Start(*app.Services) error { return nil }
func (m *blockingModule) Shutdown(context.Context) error {
	m.shutdowns.Add(1)
	m.entered <- struct{}{}
	<-m.release
	return nil
}

// queueCloseProbe and cacheCloseProbe count the Shutdown of the service
// they wrap. A teardown step that drains (teardown.Drain) calls a
// Shutdown that returned at its done ctx once more, detached from the
// ctx's cancellation (ctx.Done() is nil), for the result the first call
// retained: that re-wait is counted apart, in rewaits, since it closes
// nothing again.
type queueCloseProbe struct {
	contract.QueueDriver
	shutdowns, rewaits atomic.Int32
}

func (p *queueCloseProbe) Shutdown(ctx context.Context) error {
	countClose(ctx, &p.shutdowns, &p.rewaits)
	return p.QueueDriver.Shutdown(ctx)
}

type cacheCloseProbe struct {
	contract.CacheManager
	shutdowns, rewaits atomic.Int32
}

func (p *cacheCloseProbe) Shutdown(ctx context.Context) error {
	countClose(ctx, &p.shutdowns, &p.rewaits)
	return p.CacheManager.Shutdown(ctx)
}

// countClose counts a Shutdown call in shutdowns, or in rewaits when ctx
// is a detached re-wait.
func countClose(ctx context.Context, shutdowns, rewaits *atomic.Int32) {
	if ctx.Done() == nil {
		rewaits.Add(1)
		return
	}
	shutdowns.Add(1)
}

// assertRewaits fails t when a probe was re-waited more than once.
func assertRewaits(t *testing.T, b *blockedApp) {
	t.Helper()
	if q, c := b.queue.rewaits.Load(), b.cache.rewaits.Load(); q > 1 || c > 1 {
		t.Errorf("re-waits: queue %d, cache %d; want at most one each", q, c)
	}
}

// blockedApp is a test app whose one module blocks in Shutdown, with the
// queue, cache and view engine closes counted.
type blockedApp struct {
	a     *App
	mod   *blockingModule
	queue *queueCloseProbe
	cache *cacheCloseProbe
	view  *viewShutdownProbe
}

func newBlockedApp(t *testing.T) *blockedApp {
	t.Helper()
	b := &blockedApp{mod: newBlockingModule(), view: &viewShutdownProbe{}}
	a, err := NewTestApp(WithModules(b.mod))
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	b.a = a
	b.queue = &queueCloseProbe{QueueDriver: a.Queue}
	b.cache = &cacheCloseProbe{CacheManager: a.Cache}
	a.Queue, a.Cache, a.Services.View = b.queue, b.cache, b.view
	t.Cleanup(func() {
		select {
		case <-b.mod.release:
		default:
			close(b.mod.release)
		}
	})
	return b
}

// closes returns how many of the queue, cache and view closes ran.
func (b *blockedApp) closes() int32 {
	return b.queue.shutdowns.Load() + b.cache.shutdowns.Load() + b.view.shutdowns.Load()
}

// shutdownWithin runs App.Shutdown with deadline d and fails the test when
// it outlives d by much.
func shutdownWithin(t *testing.T, a *App, d time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- a.Shutdown(ctx) }()
	select {
	case err := <-errc:
		return err
	case <-time.After(d + 2*time.Second):
		t.Fatalf("App.Shutdown did not return within its %v deadline", d)
		return nil
	}
}

// A module Shutdown that ignores its ctx does not hold App.Shutdown past
// the deadline, and the teardown after it waits for it: the queue, cache
// and view engine close only once the module returns.
func TestShutdown_BlockingModuleDoesNotHoldTheDeadline(t *testing.T) {
	b := newBlockedApp(t)
	if err := shutdownWithin(t, b.a, 100*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("App.Shutdown = %v, want its deadline", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := b.closes(); n != 0 {
		t.Fatalf("%d service closes ran while the module's Shutdown still ran", n)
	}
	close(b.mod.release)
	deadline := time.Now().Add(2 * time.Second)
	for b.queue.shutdowns.Load() != 1 || b.cache.shutdowns.Load() != 1 || b.view.shutdowns.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("closes after the module returned: queue %d, cache %d, view %d; want one each",
				b.queue.shutdowns.Load(), b.cache.shutdowns.Load(), b.view.shutdowns.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	// A later Shutdown returns the ended teardown's result at once (its
	// steps saw the first caller's expired ctx, so it may carry errors).
	shutdownWithin(t, b.a, time.Second)
	if b.mod.shutdowns.Load() != 1 || b.queue.shutdowns.Load() != 1 {
		t.Error("a Shutdown after the teardown ended ran a step again")
	}
	assertRewaits(t, b)
}

// Overlapping Shutdowns share the one teardown: each returns at its own
// deadline while the module blocks, a later one returns the teardown's
// result, and every step ran once.
func TestShutdown_OverlappingCallsShareOneTeardown(t *testing.T) {
	b := newBlockedApp(t)
	errs := make(chan error, 3)
	for range 3 {
		go func() { errs <- shutdownWithin(t, b.a, 100*time.Millisecond) }()
	}
	for range 3 {
		if err := <-errs; !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("an overlapping App.Shutdown = %v, want its deadline", err)
		}
	}
	close(b.mod.release)
	if err := shutdownWithin(t, b.a, 2*time.Second); err == context.DeadlineExceeded {
		t.Errorf("the Shutdown that saw the teardown end returned its own deadline: %v", err)
	}
	if n := b.mod.shutdowns.Load(); n != 1 {
		t.Errorf("the module shut down %d times, want 1", n)
	}
	if b.queue.shutdowns.Load() != 1 || b.cache.shutdowns.Load() != 1 || b.view.shutdowns.Load() != 1 {
		t.Errorf("closes: queue %d, cache %d, view %d; want one each",
			b.queue.shutdowns.Load(), b.cache.shutdowns.Load(), b.view.shutdowns.Load())
	}
	assertRewaits(t, b)
}

// reenteringModule calls App.Shutdown from its own Shutdown.
type reenteringModule struct {
	a   *App
	err error
}

func (*reenteringModule) Init(*app.Services) error  { return nil }
func (*reenteringModule) Start(*app.Services) error { return nil }
func (m *reenteringModule) Shutdown(ctx context.Context) error {
	m.err = m.a.Shutdown(ctx)
	return nil
}

// An App.Shutdown from inside the teardown returns an error at once
// instead of waiting on itself, and the teardown completes.
func TestShutdown_FromTheTeardownReturnsAtOnce(t *testing.T) {
	m := &reenteringModule{}
	a, err := NewTestApp(WithModules(m))
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	m.a = a
	if err := shutdownWithin(t, a, 2*time.Second); err != nil {
		t.Fatalf("App.Shutdown = %v, want nil", err)
	}
	if !errors.Is(m.err, contract.ErrStopFromOwnWork) {
		t.Errorf("the nested App.Shutdown = %v, want an error wrapping contract.ErrStopFromOwnWork", m.err)
	}
}
