package velocity

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/chain"
)

// shutdownRecorder is a Module that records whether its Shutdown
// was invoked. It is used by New() failure-path tests to prove that a
// module opened earlier in the lifecycle is torn down when a later
// module's Init/Start fails.
type shutdownRecorder struct {
	initErr     error
	startErr    error
	initialized atomic.Bool
	started     atomic.Bool
	shutdowns   atomic.Int32
}

func (p *shutdownRecorder) Init(_ *app.Services) error {
	p.initialized.Store(true)
	return p.initErr
}

func (p *shutdownRecorder) Start(_ *app.Services) error {
	p.started.Store(true)
	return p.startErr
}

func (p *shutdownRecorder) Shutdown(_ context.Context) error {
	p.shutdowns.Add(1)
	return nil
}

// TestNew_ModuleStartFailure_UnwindsInitializedModules proves that when
// a module's Start returns an error, every module that previously
// completed its Init/Start gets its Shutdown invoked before New()
// returns. Before the fix, New() leaked resources on the failure path:
// modules that had already bound their services were never unwound.
func TestNew_ModuleStartFailure_UnwindsInitializedModules(t *testing.T) {
	good := &shutdownRecorder{}
	bad := &shutdownRecorder{startErr: errors.New("start kaboom")}

	_, err := NewTestApp(WithModules(good, bad))
	if err == nil {
		t.Fatal("expected start failure to propagate from New()")
	}
	if !errors.Is(err, bad.startErr) {
		t.Fatalf("expected wrapped start error, got: %v", err)
	}

	// Both modules initialized in order (two-phase lifecycle runs Init
	// for every module before any Start).
	if !good.initialized.Load() || !bad.initialized.Load() {
		t.Fatal("expected both modules to have initialized")
	}
	if !good.started.Load() {
		t.Fatal("expected first module to have started before second failed")
	}

	// Both modules should have Shutdown called on the failure path.
	if got := good.shutdowns.Load(); got != 1 {
		t.Errorf("good module Shutdown called %d times, want 1", got)
	}
	if got := bad.shutdowns.Load(); got != 1 {
		t.Errorf("bad module Shutdown called %d times, want 1", got)
	}
}

// TestNew_ModuleInitFailure_UnwindsEarlierModules is the sibling
// of the start test: if the second module's Init fails, the first
// module, which already completed Init, must still see its
// Shutdown called.
func TestNew_ModuleInitFailure_UnwindsEarlierModules(t *testing.T) {
	good := &shutdownRecorder{}
	bad := &shutdownRecorder{initErr: errors.New("init kaboom")}

	_, err := NewTestApp(WithModules(good, bad))
	if err == nil {
		t.Fatal("expected init failure to propagate from New()")
	}
	if !errors.Is(err, bad.initErr) {
		t.Fatalf("expected wrapped init error, got: %v", err)
	}

	if !good.initialized.Load() {
		t.Fatal("expected first module to have initialized")
	}
	// Modules whose Init completed receive Shutdown during unwind
	// even if their own Start never ran (resources they opened during
	// Init still need releasing). The module whose Init FAILED
	// must not: it is required to clean up before returning the error,
	// and calling Shutdown on it would tear down state it never owned.
	if got := good.shutdowns.Load(); got != 1 {
		t.Errorf("good module Shutdown called %d times, want 1 on init failure", got)
	}
	if got := bad.shutdowns.Load(); got != 0 {
		t.Errorf("failing module Shutdown called %d times, want 0 on its own init failure", got)
	}
}

// TestNew_FailurePath_ClosesQueueWorkers is a concrete
// leak regression: the memory queue driver starts a worker goroutine in
// its Start() method; before the fix, a module failure after queue init
// left that goroutine running. We compare goroutine counts from before
// New() to after the failure to prove the worker was stopped.
func TestNew_FailurePath_ClosesQueueWorkers(t *testing.T) {
	// Let any background goroutines from prior tests settle so the delta
	// we measure is attributable to this test alone.
	waitForGoroutinesToSettle(t, time.Second)
	before := runtime.NumGoroutine()

	bad := &shutdownRecorder{startErr: errors.New("start kaboom")}
	_, err := NewTestApp(WithModules(bad))
	if err == nil {
		t.Fatal("expected start failure to propagate from New()")
	}

	// Poll until the runtime reclaims the torn-down workers; memory queue
	// Shutdown signals its worker loop and the scheduler takes a moment to
	// unwind the G onto its P.
	waitForGoroutinesToSettle(t, 2*time.Second)
	after := runtime.NumGoroutine()

	// Allow a tiny positive slack (test-internal goroutines from poll loops
	// can jitter by one or two) but block meaningful leaks.
	if delta := after - before; delta > 2 {
		t.Errorf("goroutine leak on New() failure path: before=%d after=%d delta=%d",
			before, after, delta)
	}
}

// TestServe_CancelsShutdownCtxOnCLIDispatch proves Serve()'s deferred
// shutdownCancel runs on every exit path — including the CLI delegation
// path (os.Args triggers Run(), which returns before Serve starts the
// HTTP listener). Before Fix 1, this path leaked the context goroutine
// created in New().
func TestServe_CancelsShutdownCtxOnCLIDispatch(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}

	// "help" is the cheapest CLI command — runs without bootstrap and
	// returns immediately. We only care that Serve() goes through the
	// Run() delegation path and that shutdownCancel fires on return.
	saved := os.Args
	os.Args = []string{"vel", "help"}
	t.Cleanup(func() { os.Args = saved })

	// Capture the context before Serve so we can observe Done() after.
	ctx := a.shutdownCtx
	if ctx == nil {
		t.Fatal("expected shutdownCtx to be non-nil after New()")
	}

	// Pre-condition: context must be live before Serve().
	select {
	case <-ctx.Done():
		t.Fatal("shutdownCtx already cancelled before Serve()")
	default:
	}

	if err := a.Serve(); err != nil {
		t.Fatalf("Serve() returned error: %v", err)
	}

	// Post-condition: deferred a.shutdownCancel() must have fired, so
	// ctx.Done() is now closed.
	select {
	case <-ctx.Done():
		// expected
	default:
		t.Fatal("shutdownCtx still open after Serve() returned via CLI dispatch")
	}
}

// TestServeHTTP_BootstrapFailure_ShutsDownSubsystems proves the bootstrap
// error path in serveHTTP does not skip graceful shutdown. If bootstrap
// fails after wiring modules, serveHTTP must call App.Shutdown so those
// modules' Shutdown hooks run. Before Fix 4, serveHTTP returned the
// bootstrap error directly, leaving partially-wired modules dangling.
func TestServeHTTP_BootstrapFailure_ShutsDownSubsystems(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}

	// Bootstrap runs chain modules' Init/Start. A chain module whose
	// Start returns an error is the simplest way to force bootstrap failure
	// after other modules have already initialized.
	good := &shutdownRecorder{}
	bad := &shutdownRecorder{startErr: errors.New("chain start kaboom")}
	a.Modules(func(r *chain.ModuleRegistry) {
		r.Add(good, bad)
	})

	if err := a.serveHTTP(); err == nil {
		t.Fatal("expected serveHTTP() to fail when chain module Start returns error")
	}

	// "good" initialized/started and must be Shutdown'd via App.Shutdown on
	// the bootstrap-failure path.
	if !good.initialized.Load() || !good.started.Load() {
		t.Fatal("good module did not complete Init+Start before failure")
	}
	if got := good.shutdowns.Load(); got != 1 {
		t.Errorf("good chain module Shutdown called %d times after bootstrap failure, want 1", got)
	}
}
