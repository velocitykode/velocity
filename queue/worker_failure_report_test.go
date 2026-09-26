package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	testsync "github.com/velocitykode/velocity/testing"
)

// errSelfReportedBoom is the error selfReportingJob and plainFailingJob
// return from every run.
var errSelfReportedBoom = errors.New("self-reported job exploded")

// hookRuns counts Failed hook runs per job ID, across the instances a
// driver rehydrates from a job's wire form.
var (
	hookRuns  sync.Map // job ID -> *atomic.Int32
	hookJobID atomic.Int64
)

// nextHookJobID returns an ID no other test run has used.
func nextHookJobID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, hookJobID.Add(1))
}

func countHookRun(id string) {
	v, _ := hookRuns.LoadOrStore(id, new(atomic.Int32))
	v.(*atomic.Int32).Add(1)
}

func hookRunsFor(id string) int32 {
	v, ok := hookRuns.Load(id)
	if !ok {
		return 0
	}
	return v.(*atomic.Int32).Load()
}

// selfReportingJob fails every run. Its Failed hook records that it
// reported the failure itself, the way a queued event listener's hook does,
// and counts its runs.
type selfReportingJob struct {
	ID       string `json:"id"`
	reported atomic.Bool
}

func (j *selfReportingJob) Handle() error { return errSelfReportedBoom }

func (j *selfReportingJob) Failed(error) {
	countHookRun(j.ID)
	j.reported.Store(true)
}

func (j *selfReportingJob) FailureReported() bool { return j.reported.Load() }

// plainFailingJob fails every run and does not report its own failures;
// its Failed hook only counts its runs.
type plainFailingJob struct {
	ID string `json:"id"`
}

func (j *plainFailingJob) Handle() error { return errSelfReportedBoom }
func (j *plainFailingJob) Failed(error)  { countHookRun(j.ID) }

func init() {
	RegisterJob(func(data []byte) (*selfReportingJob, error) {
		j := &selfReportingJob{}
		if err := json.Unmarshal(data, j); err != nil {
			return nil, err
		}
		return j, nil
	})
	RegisterJob(func(data []byte) (*plainFailingJob, error) {
		j := &plainFailingJob{}
		if err := json.Unmarshal(data, j); err != nil {
			return nil, err
		}
		return j, nil
	})
}

// deleteOnPopDriver hides every optional capability of the driver it wraps
// (reservations, trace-aware pops), so the worker pops with PopCtx and fails
// a job through Driver.Failed, the path a delete-on-pop driver such as redis
// takes.
type deleteOnPopDriver struct {
	Driver
}

// runUntilJobFailed pushes job onto d, runs a single-attempt worker with a
// dispatcher until it dispatches one job.failed event, stops the worker and
// returns that event.
func runUntilJobFailed(t *testing.T, d Driver, job Job) *JobFailed {
	t.Helper()
	const queueName = "failure-report"
	if err := d.PushCtx(context.Background(), job, queueName); err != nil {
		t.Fatalf("push: %v", err)
	}
	var (
		mu     sync.Mutex
		failed []*JobFailed
	)
	w := NewWorker(d, queueName, func(j Job) error { return j.Handle() },
		WithInterval(5*time.Millisecond), WithMaxRetries(1), WithWorkerLogger(nullLogger{}))
	w.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		if e, ok := event.(*JobFailed); ok {
			mu.Lock()
			failed = append(failed, e)
			mu.Unlock()
		}
		return nil
	})
	w.Start(context.Background())
	defer w.Stop()
	testsync.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(failed) > 0
	}, 5*time.Second, "worker dispatches job.failed")
	w.Stop()

	mu.Lock()
	defer mu.Unlock()
	if len(failed) != 1 {
		t.Fatalf("job.failed dispatched %d times, want 1", len(failed))
	}
	return failed[0]
}

// TestWorker_JobFailedCarriesFailureMarkedWhenHookReported asserts every
// driver runs the job's Failed hook exactly once on a terminal failure (the
// database driver on the instance it rehydrated), and the job.failed event
// carries the job's own error, marked reported exactly when that hook
// reported it: a self-reporting job is marked on the memory reservation
// path, a delete-on-pop driver and the database driver, and a job that does
// not report its own failures is never marked.
func TestWorker_JobFailedCarriesFailureMarkedWhenHookReported(t *testing.T) {
	memory := func(t *testing.T) Driver { return newStartedMemoryDriver(t) }
	database := func(t *testing.T) Driver {
		d, cleanup := newSQLiteQueueDB(t)
		t.Cleanup(cleanup)
		return d
	}
	tests := []struct {
		name       string
		driver     func(t *testing.T) Driver
		job        func(id string) Job
		wantMarked bool
	}{
		{
			name:       "memory reservation path",
			driver:     memory,
			job:        func(id string) Job { return &selfReportingJob{ID: id} },
			wantMarked: true,
		},
		{
			name:       "delete-on-pop driver",
			driver:     func(t *testing.T) Driver { return deleteOnPopDriver{newStartedMemoryDriver(t)} },
			job:        func(id string) Job { return &selfReportingJob{ID: id} },
			wantMarked: true,
		},
		{
			name:       "database driver",
			driver:     database,
			job:        func(id string) Job { return &selfReportingJob{ID: id} },
			wantMarked: true,
		},
		{
			name:       "job without a self-reporting hook on memory",
			driver:     memory,
			job:        func(id string) Job { return &plainFailingJob{ID: id} },
			wantMarked: false,
		},
		{
			name:       "job without a self-reporting hook on the database driver",
			driver:     database,
			job:        func(id string) Job { return &plainFailingJob{ID: id} },
			wantMarked: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := nextHookJobID("hook")
			event := runUntilJobFailed(t, tt.driver(t), tt.job(id))
			if got := hookRunsFor(id); got != 1 {
				t.Errorf("Failed hook ran %d times, want 1", got)
			}

			if event.Error != errSelfReportedBoom.Error() {
				t.Errorf("Error = %q, want %q", event.Error, errSelfReportedBoom.Error())
			}
			if !errors.Is(event.Err, errSelfReportedBoom) {
				t.Errorf("Err = %v, want the job's own error", event.Err)
			}
			if got := contract.IsReported(event.Err); got != tt.wantMarked {
				t.Errorf("IsReported(Err) = %v, want %v", got, tt.wantMarked)
			}
			failure := event.FailureError()
			if tt.wantMarked && failure != event.Err {
				t.Errorf("FailureError() = %v, want the marked Err itself", failure)
			}
			if failure == nil || failure.Error() != errSelfReportedBoom.Error() || contract.IsReported(failure) != tt.wantMarked {
				t.Errorf("FailureError() = %v (reported %v), want the failure's text, reported %v", failure, contract.IsReported(failure), tt.wantMarked)
			}
		})
	}
}

// TestWorker_JobFailedNotMarkedWhenHookDidNotReport asserts a job whose
// hook ran but did not report (FailureReported false) leaves the failure
// unmarked, so the failure-report bridge reports it.
func TestWorker_JobFailedNotMarkedWhenHookDidNotReport(t *testing.T) {
	job := &unreportedHookJob{}
	event := runUntilJobFailed(t, newStartedMemoryDriver(t), job)
	if job.hookRuns.Load() != 1 {
		t.Fatalf("Failed hook ran %d times, want 1", job.hookRuns.Load())
	}
	if contract.IsReported(event.Err) {
		t.Error("failure marked reported although the hook did not report it")
	}
}

// unreportedHookJob fails every run; its hook runs but reports nothing.
type unreportedHookJob struct {
	hookRuns atomic.Int32
}

func (j *unreportedHookJob) Handle() error         { return errSelfReportedBoom }
func (j *unreportedHookJob) Failed(error)          { j.hookRuns.Add(1) }
func (j *unreportedHookJob) FailureReported() bool { return false }

func newStartedMemoryDriver(t *testing.T) *MemoryDriver {
	t.Helper()
	d := NewMemoryDriver()
	d.Start()
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
	return d
}

// TestJobFailed_FailureError asserts FailureError returns Err when it
// carries the report-once marker, and otherwise a new error with the Error
// text (Err or not), so the bridge's report of an unreported failure does
// not depend on the job error's type; and that Err never reaches the
// event's JSON form.
func TestJobFailed_FailureError(t *testing.T) {
	cause := contract.NewHTTPError(404)
	marked := contract.MarkReported(cause)
	if got := (&JobFailed{Error: cause.Error(), Err: marked}).FailureError(); got != marked {
		t.Errorf("FailureError() with a marked Err = %v, want Err itself", got)
	}
	got := (&JobFailed{Error: cause.Error(), Err: cause}).FailureError()
	if got == nil || got.Error() != cause.Error() || errors.Is(got, cause) {
		t.Errorf("FailureError() with an unmarked Err = %#v, want a new error with the Error text", got)
	}
	if _, _, ok := contract.StatusOf(got); ok {
		t.Errorf("FailureError() with an unmarked Err carries the job error's HTTP status: %v", got)
	}
	if got := (&JobFailed{Error: "smtp exploded"}).FailureError(); got == nil || got.Error() != "smtp exploded" {
		t.Errorf("FailureError() without Err = %v, want the Error text", got)
	}
	if got := (&JobFailed{}).FailureError(); got != nil {
		t.Errorf("FailureError() of an empty event = %v, want nil", got)
	}

	data, err := json.Marshal(&JobFailed{JobType: "SendEmail", Error: "smtp exploded", Err: errors.New("smtp exploded")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := fields["Err"]; ok {
		t.Errorf("JSON carries Err: %s", data)
	}
	if fields["Error"] != "smtp exploded" {
		t.Errorf("JSON Error = %v, want %q (%s)", fields["Error"], "smtp exploded", data)
	}
}

// TestDatabaseDriver_FailedHookRunsOnceAfterRecording asserts the database
// driver runs a job's Failed hook exactly once, on the instance it was
// handed, after the failure is recorded, through FailReservedCtx (a lease
// and a zero token) and Failed; and never when the failure was not
// recorded: a lost lease, a failed transaction, a cancelled context (with a
// lease or a zero token, neither writing a failed_jobs row), a failed
// insert.
func TestDatabaseDriver_FailedHookRunsOnceAfterRecording(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)
	boom := errors.New("terminal")
	const queueName = "hook"

	// reserve pushes a job and leases it, returning the token.
	reserve := func(t *testing.T, d *DatabaseDriver) ReservationToken {
		t.Helper()
		if err := d.PushCtx(context.Background(), &TestJob{ID: "hooked"}, queueName); err != nil {
			t.Fatalf("push: %v", err)
		}
		popped, token, _, err := d.PopCtxReserved(context.Background(), queueName)
		if err != nil || popped == nil || token.IsZero() {
			t.Fatalf("pop: job=%v token=%+v err=%v", popped, token, err)
		}
		return token
	}
	dropFailedJobs := func(t *testing.T, d *DatabaseDriver) {
		t.Helper()
		if _, err := d.db.Exec("DROP TABLE failed_jobs"); err != nil {
			t.Fatalf("drop failed_jobs: %v", err)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name     string
		fail     func(t *testing.T, d *DatabaseDriver, job Job) error
		wantErr  bool
		wantRuns int32
		// noFailedRow asserts failed_jobs holds no row afterwards.
		noFailedRow bool
	}{
		{
			name: "FailReservedCtx commit",
			fail: func(t *testing.T, d *DatabaseDriver, job Job) error {
				return d.FailReservedCtx(context.Background(), reserve(t, d), job, boom, queueName)
			},
			wantRuns: 1,
		},
		{
			name: "FailReservedCtx with a zero token",
			fail: func(_ *testing.T, d *DatabaseDriver, job Job) error {
				return d.FailReservedCtx(context.Background(), ReservationToken{}, job, boom, queueName)
			},
			wantRuns: 1,
		},
		{
			name: "Failed",
			fail: func(_ *testing.T, d *DatabaseDriver, job Job) error {
				return d.Failed(job, boom, queueName)
			},
			wantRuns: 1,
		},
		{
			name: "FailReservedCtx with a lost lease",
			fail: func(t *testing.T, d *DatabaseDriver, job Job) error {
				token := reserve(t, d)
				token.ReservedBy = "another-worker"
				err := d.FailReservedCtx(context.Background(), token, job, boom, queueName)
				if !errors.Is(err, ErrLeaseLost) {
					t.Errorf("err = %v, want ErrLeaseLost", err)
				}
				return err
			},
			wantErr: true,
		},
		{
			name: "FailReservedCtx with a failed transaction",
			fail: func(t *testing.T, d *DatabaseDriver, job Job) error {
				token := reserve(t, d)
				dropFailedJobs(t, d)
				return d.FailReservedCtx(context.Background(), token, job, boom, queueName)
			},
			wantErr: true,
		},
		{
			name: "FailReservedCtx with a cancelled context",
			fail: func(t *testing.T, d *DatabaseDriver, job Job) error {
				return d.FailReservedCtx(cancelled, reserve(t, d), job, boom, queueName)
			},
			wantErr:     true,
			noFailedRow: true,
		},
		{
			name: "FailReservedCtx with a zero token and a cancelled context",
			fail: func(_ *testing.T, d *DatabaseDriver, job Job) error {
				return d.FailReservedCtx(cancelled, ReservationToken{}, job, boom, queueName)
			},
			wantErr:     true,
			noFailedRow: true,
		},
		{
			name: "Failed with a failed insert",
			fail: func(t *testing.T, d *DatabaseDriver, job Job) error {
				dropFailedJobs(t, d)
				return d.Failed(job, boom, queueName)
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, cleanup := newSQLiteQueueDB(t)
			defer cleanup()
			var runs atomic.Int32
			job := &TestJob{ID: "hooked", OnFail: func(err error) {
				runs.Add(1)
				if !errors.Is(err, boom) {
					t.Errorf("hook got %v, want the job's error", err)
				}
			}}

			err := tt.fail(t, d, job)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error %v", err, tt.wantErr)
			}
			if got := runs.Load(); got != tt.wantRuns {
				t.Errorf("Failed hook ran %d times, want %d", got, tt.wantRuns)
			}
			if tt.noFailedRow {
				var n int
				if err := d.db.QueryRow("SELECT COUNT(*) FROM failed_jobs").Scan(&n); err != nil {
					t.Fatalf("count failed_jobs: %v", err)
				}
				if n != 0 {
					t.Errorf("failed_jobs holds %d rows, want 0", n)
				}
			}
		})
	}
}
