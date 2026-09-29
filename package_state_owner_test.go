package velocity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/log"
	testsync "github.com/velocitykode/velocity/testing"
	"github.com/velocitykode/velocity/trace"
)

// ownedApp is one app of a multi-app ownership test: the app, its logger
// and the reports its error handler receives.
type ownedApp struct {
	app     *App
	logger  *levelLogger
	reports *failureReports
}

func newOwnedApp(t *testing.T) ownedApp {
	t.Helper()
	a, l := newLoggerWiringApp(t, nil)
	r := &failureReports{}
	r.add(a)
	return ownedApp{app: a, logger: l, reports: r}
}

// packageBaseline returns the package logger installed before a test:
// another test's app that was never shut down may own the state, and the
// state returns to it once the test's apps shut down.
func packageBaseline() contract.Logger { return async.GetLogger() }

// assertPackageOwner checks the async and trace package loggers are want's
// logger and a panic an async helper recovers is reported to want alone.
func assertPackageOwner(t *testing.T, step string, want ownedApp, others ...ownedApp) {
	t.Helper()
	if got := async.GetLogger(); got != contract.Logger(want.logger) {
		t.Errorf("%s: async logger = %T %p, want the owner's logger %p", step, got, got, want.logger)
	}
	if got := trace.GetLogger(); got != contract.Logger(want.logger) {
		t.Errorf("%s: trace logger = %T %p, want the owner's logger %p", step, got, got, want.logger)
	}
	before := want.reports.count()
	otherBefore := make([]int, len(others))
	for i, o := range others {
		otherBefore[i] = o.reports.count()
	}
	async.Go(func() { panic("owned panic: " + step) })
	testsync.Eventually(t, func() bool { return want.reports.count() > before }, 2*time.Second, step+": panic reported to the owner")
	time.Sleep(20 * time.Millisecond)
	if n := want.reports.count() - before; n != 1 {
		t.Errorf("%s: owner got %d reports, want 1", step, n)
	}
	for i, o := range others {
		if n := o.reports.count() - otherBefore[i]; n != 0 {
			t.Errorf("%s: another app got %d reports, want 0", step, n)
		}
	}
}

// assertPreviousOwner checks both package loggers are back on baseline,
// the logger installed before the test, and a recovered panic is reported
// to none of apps; with no owner before the test it is logged through the
// fallback.
func assertPreviousOwner(t *testing.T, step string, fallback *fallbacklogtest.Output, baseline contract.Logger, apps ...ownedApp) {
	t.Helper()
	if got := async.GetLogger(); got != baseline {
		t.Errorf("%s: async logger = %T, want the logger installed before the test (%T)", step, got, baseline)
	}
	if got := trace.GetLogger(); got != baseline {
		t.Errorf("%s: trace logger = %T, want the logger installed before the test (%T)", step, got, baseline)
	}
	counts := make([]int, len(apps))
	for i, a := range apps {
		counts[i] = a.reports.count()
	}
	logged := fallback.Count("ERROR", "async: panic recovered")
	async.Go(func() { panic("unowned panic: " + step) })
	if _, unowned := baseline.(fallbacklog.Logger); unowned {
		if n := fallback.Wait("ERROR", "async: panic recovered", logged+1, 2*time.Second); n != logged+1 {
			t.Errorf("%s: fallback panic lines = %d, want %d", step, n, logged+1)
		}
	} else {
		time.Sleep(50 * time.Millisecond)
	}
	for i, a := range apps {
		if n := a.reports.count() - counts[i]; n != 0 {
			t.Errorf("%s: a shut-down app got %d reports, want 0", step, n)
		}
	}
}

// With two live apps the newer one owns the process-wide panic hook and
// package loggers. Shutting down the older one leaves the newer one's
// installation in place; shutting down the newer one then leaves none.
func TestPackageState_OlderAppShutdownKeepsTheNewerOwner(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	baseline := packageBaseline()
	a := newOwnedApp(t)
	b := newOwnedApp(t)
	assertPackageOwner(t, "A then B", b, a)

	if err := a.app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown A: %v", err)
	}
	assertPackageOwner(t, "after A's Shutdown", b, a)

	if err := b.app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown B: %v", err)
	}
	assertPreviousOwner(t, "after B's Shutdown", fallback, baseline, a, b)
}

// Shutting down the newer app hands the process-wide state back to the
// older live app, not to the fallback; the older app's Shutdown then
// leaves none.
func TestPackageState_NewerAppShutdownRestoresTheOlderOwner(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	baseline := packageBaseline()
	a := newOwnedApp(t)
	b := newOwnedApp(t)

	if err := b.app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown B: %v", err)
	}
	assertPackageOwner(t, "after B's Shutdown", a, b)

	if err := a.app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown A: %v", err)
	}
	assertPreviousOwner(t, "after A's Shutdown", fallback, baseline, a, b)
}

// failingInitModule fails its Init, after New's first wiring boundary has
// installed the app's process-wide state.
type failingInitModule struct{}

var errOwnerInitFailed = errors.New("owner test: init failed")

func (failingInitModule) Init(*app.Services) error       { return errOwnerInitFailed }
func (failingInitModule) Start(*app.Services) error      { return nil }
func (failingInitModule) Shutdown(context.Context) error { return nil }

// A New that fails after its first wiring boundary cleans up its own
// installation only: the live app that owned the state before it keeps it.
func TestPackageState_FailedNewRestoresTheLiveOwner(t *testing.T) {
	a := newOwnedApp(t)

	cfg := Config{Env: "testing", Port: "0", Cache: CacheConfig{Driver: "memory"}, Queue: QueueConfig{Driver: "memory"}}
	if _, err := New(WithConfig(cfg), WithModules(failingInitModule{})); !errors.Is(err, errOwnerInitFailed) {
		t.Fatalf("New = %v, want the module's Init error", err)
	}
	assertPackageOwner(t, "after the failed New", a)
}

// An app wiring again at a later boundary while a newer app lives keeps
// its own installation current without taking the state over.
func TestPackageState_OlderAppRewireDoesNotTakeOver(t *testing.T) {
	a := newOwnedApp(t)
	b := newOwnedApp(t)
	if err := a.app.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap A: %v", err)
	}
	assertPackageOwner(t, "after A's Bootstrap", b, a)

	if err := b.app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown B: %v", err)
	}
	assertPackageOwner(t, "after B's Shutdown", a, b)
}

// Installs and releases racing from many apps against package-logger
// readers and recovered panics leave the stack consistent: once every app
// has released, nothing is installed.
func TestPackageState_ConcurrentInstallAndRelease(t *testing.T) {
	fallbacklogtest.Capture(t)
	packageStateMu.Lock()
	before := len(packageStack)
	packageStateMu.Unlock()
	baseline := packageBaseline()
	const apps = 16
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = async.GetLogger()
				_ = trace.GetLogger()
			}
		}()
	}
	var appsWG sync.WaitGroup
	for i := 0; i < apps; i++ {
		appsWG.Add(1)
		go func() {
			defer appsWG.Done()
			a := &App{}
			for j := 0; j < 50; j++ {
				l := &levelLogger{}
				installPanicHook(a, func(context.Context, any) {})
				installPackageLoggers(a, l)
				if j%3 == 0 {
					async.Go(func() { panic("stress") })
				}
				releasePackageState(a)
			}
		}()
	}
	appsWG.Wait()
	close(stop)
	wg.Wait()

	packageStateMu.Lock()
	n := len(packageStack)
	packageStateMu.Unlock()
	if n != before {
		t.Fatalf("package stack holds %d entries after every app released, want %d", n, before)
	}
	if got := async.GetLogger(); got != baseline {
		t.Errorf("async logger = %T, want the one installed before the test", got)
	}
	if got := trace.GetLogger(); got != baseline {
		t.Errorf("trace logger = %T, want the one installed before the test", got)
	}
}

// Releasing an app that never wired, or releasing twice, changes nothing.
func TestPackageState_ReleaseWithoutEntryIsNoop(t *testing.T) {
	baseline := packageBaseline()
	owner, stranger := &App{}, &App{}
	l := &levelLogger{}
	installPackageLoggers(owner, l)
	t.Cleanup(func() { releasePackageState(owner) })
	releasePackageState(stranger)
	releasePackageState(nil)
	if got := async.GetLogger(); got != contract.Logger(l) {
		t.Fatalf("async logger = %T, want the owner's", got)
	}
	releasePackageState(owner)
	releasePackageState(owner)
	if got := async.GetLogger(); got != baseline {
		t.Errorf("async logger after release = %T, want the one installed before the test", got)
	}
}

// A goroutine that took the async package logger before an app with the
// file driver shut down, and writes after, does not reopen the closed log
// file.
func TestShutdown_LateWriteThroughTheOldPackageLoggerDoesNotReopenTheLogFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	cfg := Config{
		Env:   "testing",
		Port:  "0",
		Cache: CacheConfig{Driver: "memory"},
		Queue: QueueConfig{Driver: "memory"},
		Log:   log.LogConfig{Driver: "file", Config: map[string]any{"path": dir, "days": 0}},
	}
	a, err := New(WithConfig(cfg))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	held := async.GetLogger()
	held.Error("before shutdown")
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	held.Error("after shutdown")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(dir)
		t.Fatalf("log dir after a write past Shutdown: %v entries (err %v), want none: the file was reopened", len(entries), err)
	}
}
