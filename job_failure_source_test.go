package velocity

import (
	"context"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/console"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/queue"
)

// recordGoneError is a job error type an application ignores for requests.
type recordGoneError struct{ id string }

func (e *recordGoneError) Error() string { return "record " + e.id + " gone" }

// recordGoneJob fails every run with a *recordGoneError.
type recordGoneJob struct {
	ID string `json:"id"`
}

func (j *recordGoneJob) Handle() error { return &recordGoneError{id: j.ID} }
func (j *recordGoneJob) Failed(error)  {}

// TestQueueWork_JobErrorIgnoredForRequestsIsReported asserts a job failing
// with an error type a request rule ignores still reaches the Reporter
// once, carrying its original type, which a rule written for jobs sees.
func TestQueueWork_JobErrorIgnoredForRequestsIsReported(t *testing.T) {
	app, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	defer app.Shutdown(context.Background())
	reports := &failureReports{}
	reports.add(app)
	problem.Ignore[*recordGoneError](app.Services.Errors)
	app.Services.Errors.AddLevelRule(contract.LevelRule{
		Match: func(err error) bool {
			var gone *recordGoneError
			return errors.As(err, &gone)
		},
		Level:   contract.LogLevelWarn,
		Sources: contract.ErrorSourceJob,
	})

	watch := &jobFailedWatch{}
	app.Services.Events.Listen(events.OfType[*queue.JobFailed](), watch)
	q := memoryQueue(t, app)
	runStockWorker(t, q.driver, queueWorkOptions(app, console.QueueWorkOptions{Tries: 1}),
		&recordGoneJob{ID: "r1"}, func() bool { return watch.n.Load() > 0 })

	if n := reports.count(); n != 1 {
		t.Fatalf("job failure reported %d times, want 1", n)
	}
	var gone *recordGoneError
	if !errors.As(reports.errs[0], &gone) || gone.id != "r1" {
		t.Errorf("reported error = %#v, want the job's *recordGoneError", reports.errs[0])
	}
	if got := reports.ctxs[0].Source; got != contract.ErrorSourceJob {
		t.Errorf("report source = %v, want ErrorSourceJob", got)
	}
	if got := reports.ctxs[0].Level; got != contract.LogLevelWarn {
		t.Errorf("report level = %v, want warn from the rule written for jobs", got)
	}
}

var _ queue.Job = (*recordGoneJob)(nil)
