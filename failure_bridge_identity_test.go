package velocity

import (
	"context"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/scheduler"
)

// The failure bridge names the failed job (its type and queue) and the
// failed scheduled task in the report's Extra, under the keys the worker's
// and scheduler's own lines use.
func TestFailureBridgeReport_NamesTheJobAndTheTask(t *testing.T) {
	rec := &recordingReporter{}
	h := problem.NewHandler(problem.WithReporters(rec))
	report := buildFailureReporter(h)

	report(context.Background(), &queue.JobFailed{JobType: "SendMail", Queue: "mail"}, errors.New("smtp down"))
	report(context.Background(), &scheduler.ScheduledTaskFailed{TaskName: "nightly-report"}, errors.New("task broke"))

	if rec.count() != 2 {
		t.Fatalf("reports = %d, want 2", rec.count())
	}
	job := rec.exCtx[0].Extra
	if job["job_type"] != "SendMail" || job["queue"] != "mail" {
		t.Errorf("job report Extra = %v, want job_type=SendMail queue=mail", job)
	}
	if _, ok := job["job_id"]; ok {
		t.Errorf("job report Extra = %v, want no job_id for a job without one", job)
	}
	if task := rec.exCtx[1].Extra; task["task_name"] != "nightly-report" {
		t.Errorf("task report Extra = %v, want task_name=nightly-report", task)
	}

	report(context.Background(), &queue.JobFailed{JobType: "SendMail", Queue: "mail", JobID: "job-9"}, errors.New("smtp down"))
	if got := rec.exCtx[2].Extra["job_id"]; got != "job-9" {
		t.Errorf("job report job_id = %v, want job-9", got)
	}
}
