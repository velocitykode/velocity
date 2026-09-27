package velocity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	testsync "github.com/velocitykode/velocity/testing"
)

// TestAsyncPanic_ReachesReporterNotStdlibLog asserts a panic recovered in
// an async.Go goroutine of a bootstrapped app reaches the app's Reporter
// chain once, as a recovered panic, and nothing is written through the
// standard library logger, slog.Default or the standalone fallback logger;
// and that the app's Shutdown removes the hook, so a later panic is logged
// through the fallback instead.
func TestAsyncPanic_ReachesReporterNotStdlibLog(t *testing.T) {
	stdout := fallbacklogtest.CaptureStdlib(t)
	fallback := fallbacklogtest.Capture(t)
	app, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	if err := app.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	reports := &failureReports{}
	reports.add(app)

	async.Go(func() { panic("background goroutine exploded") })
	testsync.Eventually(t, func() bool { return reports.count() >= 1 }, 2*time.Second, "recovered panic reported")
	time.Sleep(50 * time.Millisecond)

	if n := reports.count(); n != 1 {
		t.Fatalf("panic reported %d times, want 1 (%v)", n, reports.errs)
	}
	var rp contract.RecoveredPanic
	if !errors.As(reports.errs[0], &rp) || rp.Recovered() != "background goroutine exploded" {
		t.Errorf("reported error = %#v, want the recovered panic", reports.errs[0])
	}
	if !reports.ctxs[0].Recovered || reports.ctxs[0].PanicStack == "" {
		t.Errorf("report context = %+v, want a recovered panic with its stack", reports.ctxs[0])
	}
	if got := reports.ctxs[0].Source; got != contract.ErrorSourceGoroutine {
		t.Errorf("report source = %v, want ErrorSourceGoroutine", got)
	}
	if out := stdout.String(); out != "" {
		t.Errorf("standard library log written: %q", out)
	}
	if n := fallback.Count("ERROR", "async: panic recovered"); n != 0 {
		t.Errorf("fallback logger wrote the reported panic %d times, want 0", n)
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	async.Go(func() { panic("after shutdown") })
	if n := fallback.Wait("ERROR", "async: panic recovered", 1, 2*time.Second); n != 1 {
		t.Errorf("panic after Shutdown logged through the fallback %d times, want 1", n)
	}
	if n := reports.count(); n != 1 {
		t.Errorf("panic after Shutdown reported: %d reports, want 1", n)
	}
}
