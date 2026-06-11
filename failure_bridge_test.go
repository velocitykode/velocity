package velocity

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/scheduler"
)

// recordingReporter captures Report calls for assertions.
type recordingReporter struct {
	mu    sync.Mutex
	errs  []error
	exCtx []*contract.ExceptionContext
}

func (r *recordingReporter) Report(err error, ctx *contract.ExceptionContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
	r.exCtx = append(r.exCtx, ctx)
}

func (r *recordingReporter) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errs)
}

// TestFailureBridge_BackgroundFailuresReachReporters is the end-to-end
// guarantee of the event->Reporter bridge: a background *Failed event
// dispatched through the app's dispatcher reaches every registered
// exception Reporter, while events whose error is caller-owned (e.g.
// router.RequestFailed) do not double-report.
func TestFailureBridge_BackgroundFailuresReachReporters(t *testing.T) {
	app, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	defer app.Shutdown(context.Background())

	rec := &recordingReporter{}
	app.Services.Exceptions.AddReporter(rec)

	// Background failures: bridged.
	if err := app.Services.Events.Dispatch(context.Background(), &queue.JobFailed{
		Context: context.Background(),
		JobType: "SendEmail",
		Queue:   "default",
		Error:   "smtp exploded",
	}); err != nil {
		t.Fatalf("Dispatch JobFailed: %v", err)
	}
	if err := app.Services.Events.Dispatch(context.Background(), &scheduler.ScheduledTaskFailed{
		Context:  context.Background(),
		TaskName: "nightly-report",
		Error:    "task exploded",
	}); err != nil {
		t.Fatalf("Dispatch ScheduledTaskFailed: %v", err)
	}

	if rec.count() != 2 {
		t.Fatalf("reporter received %d reports, want 2", rec.count())
	}
	if rec.errs[0].Error() != "smtp exploded" {
		t.Errorf("first reported error = %q, want %q", rec.errs[0].Error(), "smtp exploded")
	}
	if got := rec.exCtx[0].Extra["event"]; got != "job.failed" {
		t.Errorf("first report event extra = %v, want job.failed", got)
	}
	if got := rec.exCtx[1].Extra["event"]; got != "scheduled.failed" {
		t.Errorf("second report event extra = %v, want scheduled.failed", got)
	}

	// Caller-owned failure: NOT bridged. Request errors reach Report
	// through the exceptions handler on the request path; bridging the
	// event too would double-report.
	if err := app.Services.Events.Dispatch(context.Background(), &router.RequestFailed{
		Context: context.Background(),
		Method:  "GET",
		Path:    "/x",
		Error:   errors.New("handler exploded"),
	}); err != nil {
		t.Fatalf("Dispatch RequestFailed: %v", err)
	}
	if rec.count() != 2 {
		t.Fatalf("router.RequestFailed was bridged: %d reports, want still 2", rec.count())
	}
}

// TestFailureBridge_RouterRequestFailedNotFailureEvent pins the deliberate
// exclusion at compile-meaning level: if someone implements FailureError()
// on router.RequestFailed, this test fails and forces the double-report
// discussion.
func TestFailureBridge_RouterRequestFailedNotFailureEvent(t *testing.T) {
	var ev any = &router.RequestFailed{}
	if _, ok := ev.(contract.FailureEvent); ok {
		t.Fatal("router.RequestFailed must not implement contract.FailureEvent: request errors already reach Report via the exceptions handler; bridging the event double-reports")
	}
}
