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
	"github.com/velocitykode/velocity/trace"
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
	// The hook goes back to the app that owned it before this one (an
	// app another test never shut down), or none: with none, the panic
	// is logged through the fallback.
	packageStateMu.Lock()
	unowned := len(packageStack) == 0
	packageStateMu.Unlock()
	async.Go(func() { panic("after shutdown") })
	if unowned {
		if n := fallback.Wait("ERROR", "async: panic recovered", 1, 2*time.Second); n != 1 {
			t.Errorf("panic after Shutdown logged through the fallback %d times, want 1", n)
		}
	} else {
		time.Sleep(50 * time.Millisecond)
	}
	if n := reports.count(); n != 1 {
		t.Errorf("panic after Shutdown reported: %d reports, want 1", n)
	}
}

// A panic recovered by a context-aware helper is reported with the
// request, trace and span ids of the context the helper was given;
// a contextless helper reports with none.
func TestAsyncPanic_ContextHelpersReportTheirContext(t *testing.T) {
	app, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	reports := &failureReports{}
	reports.add(app)

	ctx := trace.WithRequestID(trace.WithTrace(context.Background(), "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"), "req-panic-1")
	check := func(name string, want int, run func()) {
		t.Helper()
		run()
		testsync.Eventually(t, func() bool { return reports.count() >= want }, 2*time.Second, name+": panic reported")
		reports.mu.Lock()
		ec := reports.ctxs[want-1]
		reports.mu.Unlock()
		if ec.RequestID != "req-panic-1" || ec.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || ec.SpanID != "00f067aa0ba902b7" {
			t.Errorf("%s: report ids = request %q trace %q span %q, want the helper's context", name, ec.RequestID, ec.TraceID, ec.SpanID)
		}
		if ec.Source != contract.ErrorSourceGoroutine || !ec.Recovered {
			t.Errorf("%s: report = source %v recovered %v, want a recovered goroutine panic", name, ec.Source, ec.Recovered)
		}
	}
	check("GoCtx", 1, func() { async.GoCtx(ctx, func(context.Context) { panic("goctx exploded") }) })
	check("RunWithContext", 2, func() {
		_, _ = async.RunWithContext(ctx, func() int { panic("run exploded") }).Get()
	})

	async.Go(func() { panic("contextless") })
	testsync.Eventually(t, func() bool { return reports.count() >= 3 }, 2*time.Second, "Go: panic reported")
	reports.mu.Lock()
	ec := reports.ctxs[2]
	reports.mu.Unlock()
	if ec.RequestID != "" || ec.TraceID != "" {
		t.Errorf("Go: report ids = request %q trace %q, want none", ec.RequestID, ec.TraceID)
	}
}
