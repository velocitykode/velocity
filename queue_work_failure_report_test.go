package velocity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	_ "github.com/mattn/go-sqlite3"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/console"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/internal/eventqueue"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/queue"
	queueredis "github.com/velocitykode/velocity/queue/redis"
	testsync "github.com/velocitykode/velocity/testing"
)

// These tests run the worker `vel queue work` runs (console.NewQueueWorker
// with the options queueWorkOptions attaches from the app) against a real
// app built by NewTestApp: its memory queue, its events dispatcher with the
// failure-report bridge, its error handler, and the queued-listener failure
// reporter New installs. A counting reporter on the error handler counts
// every report that passes the report gate.

// permanentFailureJob is a plain job that fails every run.
type permanentFailureJob struct {
	ID string `json:"id"`
}

func (j *permanentFailureJob) Handle() error { return errors.New("plain job exploded") }
func (j *permanentFailureJob) Failed(error)  {}

// clientStatusJob is a plain job that fails every run with an error
// carrying a client-error HTTP status, which the error handler ignores for
// requests.
type clientStatusJob struct {
	ID string `json:"id"`
}

func (j *clientStatusJob) Handle() error {
	return contract.NewHTTPError(404, "upstream record gone")
}
func (j *clientStatusJob) Failed(error) {}

// explodingQueuedListener is a queued listener whose every run fails.
type explodingQueuedListener struct{}

func (explodingQueuedListener) Handle(context.Context, interface{}) error {
	return errors.New("queued listener exploded")
}
func (explodingQueuedListener) Async() bool { return true }

// clientStatusQueuedListener is a queued listener whose every run fails
// with an error carrying a client-error HTTP status.
type clientStatusQueuedListener struct{}

func (clientStatusQueuedListener) Handle(context.Context, interface{}) error {
	return contract.NewHTTPError(404, "listener record gone")
}
func (clientStatusQueuedListener) Async() bool { return true }

// hookedJobRuns counts hookedFailureJob hook runs per job ID, across the
// instances a durable driver rehydrates.
var hookedJobRuns sync.Map // job ID -> *atomic.Int32

// hookedFailureJob is a plain job that fails every run and counts its
// Failed hook runs.
type hookedFailureJob struct {
	ID string `json:"id"`
}

func (j *hookedFailureJob) Handle() error { return errors.New("hooked job exploded") }
func (j *hookedFailureJob) Failed(error) {
	v, _ := hookedJobRuns.LoadOrStore(j.ID, new(atomic.Int32))
	v.(*atomic.Int32).Add(1)
}

func hookedRuns(id string) int32 {
	v, ok := hookedJobRuns.Load(id)
	if !ok {
		return 0
	}
	return v.(*atomic.Int32).Load()
}

// hookedJobSeq numbers hookedFailureJob IDs so no two test runs share one.
var hookedJobSeq atomic.Int64

func init() {
	// The database and redis drivers rebuild a popped job from its wire form.
	queue.RegisterJob(func(data []byte) (*permanentFailureJob, error) {
		j := &permanentFailureJob{}
		return j, json.Unmarshal(data, j)
	})
	queue.RegisterJob(func(data []byte) (*hookedFailureJob, error) {
		j := &hookedFailureJob{}
		return j, json.Unmarshal(data, j)
	})
}

// jobFailedWatch counts job.failed events delivered to listeners. The
// dispatcher runs the failure-report bridge before listeners, so a delivery
// means the bridge has already had its say for that event.
type jobFailedWatch struct{ n atomic.Int32 }

func (w *jobFailedWatch) Handle(context.Context, interface{}) error { w.n.Add(1); return nil }
func (w *jobFailedWatch) Async() bool                               { return false }

// failureReports is a reporter counting the reports an error handler makes.
type failureReports struct {
	mu   sync.Mutex
	errs []error
	ctxs []*contract.ErrorContext
}

func (r *failureReports) reporter() contract.Reporter {
	return problem.NewCallbackReporter(func(err error, ctx *problem.ErrorContext) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.errs = append(r.errs, err)
		r.ctxs = append(r.ctxs, ctx)
	})
}

func (r *failureReports) add(app *App) {
	app.Services.Errors.AddReporter(r.reporter())
}

func (r *failureReports) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errs)
}

// queuedListenerJob builds the job the queued listener l becomes on the
// queue, registered under listenerType and hydrated from its wire form as
// a worker in another process would see it, with a retry budget of one
// attempt.
func queuedListenerJob(t *testing.T, listenerType string, l events.Listener) queue.Job {
	t.Helper()
	events.RegisterListenerFactory(listenerType, func() events.Listener { return l })
	t.Cleanup(func() { events.UnregisterListenerFactory(listenerType) })
	data, err := json.Marshal(map[string]any{"listener_type": listenerType, "max_retries": 1})
	if err != nil {
		t.Fatalf("marshal listener job: %v", err)
	}
	job, err := events.EventJobFactory(data)
	if err != nil {
		t.Fatalf("hydrate listener job: %v", err)
	}
	return job
}

func explodingListenerJob(t *testing.T) queue.Job {
	return queuedListenerJob(t, "velocity.explodingQueuedListener", explodingQueuedListener{})
}

func clientStatusListenerJob(t *testing.T) queue.Job {
	return queuedListenerJob(t, "velocity.clientStatusQueuedListener", clientStatusQueuedListener{})
}

// workerQueue is a queue driver a test worker runs against, with a probe
// that turns true once the driver has recorded a failed job.
type workerQueue struct {
	driver         queue.Driver
	failedRecorded func() bool
}

// memoryQueue is the app's own memory queue.
func memoryQueue(t *testing.T, app *App) workerQueue {
	t.Helper()
	mem, ok := app.Queue.(*queue.MemoryDriver)
	if !ok {
		t.Fatalf("app queue is %T, want *queue.MemoryDriver", app.Queue)
	}
	return workerQueue{driver: mem, failedRecorded: func() bool {
		failed, err := mem.GetFailed("default")
		return err == nil && len(failed) > 0
	}}
}

// redisQueue is a redis queue on a miniredis server.
func redisQueue(t *testing.T, _ *App) workerQueue {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	d, err := queueredis.NewRedisDriver(queue.RedisConfig{Host: mr.Host(), Port: mr.Port(), DB: "0"})
	if err != nil {
		t.Fatalf("new redis driver: %v", err)
	}
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
	return workerQueue{driver: d, failedRecorded: func() bool {
		failed, err := mr.List("velocity:queue:default:failed")
		return err == nil && len(failed) > 0
	}}
}

// databaseQueue is a database queue on a file-backed sqlite database with
// the jobs and failed_jobs tables the driver expects.
func databaseQueue(t *testing.T, _ *App) workerQueue {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/queue.db?_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`CREATE TABLE jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			queue TEXT NOT NULL,
			payload TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			scheduled_at DATETIME NOT NULL,
			reserved_at DATETIME,
			reserved_by TEXT,
			failed_at DATETIME,
			failed_reason TEXT,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE failed_jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			queue TEXT NOT NULL,
			payload TEXT NOT NULL,
			exception TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return workerQueue{driver: queue.NewDatabaseDriver(db, "sqlite"), failedRecorded: func() bool {
		var n int
		return db.QueryRow("SELECT COUNT(*) FROM failed_jobs").Scan(&n) == nil && n > 0
	}}
}

// runStockWorker pushes job onto q, runs a queue worker built from opts
// until done reports true, and stops it. Stop waits for the in-flight job,
// so every report its failure causes has been made when runStockWorker
// returns.
func runStockWorker(t *testing.T, q queue.Driver, opts console.QueueWorkOptions, job queue.Job, done func() bool) {
	t.Helper()
	if err := q.PushCtx(context.Background(), job, "default"); err != nil {
		t.Fatalf("push: %v", err)
	}
	w := console.NewQueueWorker(q, opts)
	w.Start(context.Background())
	defer w.Stop()
	testsync.Eventually(t, done, 5*time.Second, "job failure handled")
}

// TestQueueWork_PermanentFailureReportedOnce asserts how often a job the
// stock queue worker fails permanently is reported, on the memory, redis
// and database drivers. With the dispatcher wired: a plain job once,
// through the job.failed event's failure-report bridge, a job error
// carrying a client-error status included; a queued listener once,
// through its own Failed hook (the report carries the listener type),
// with the bridge skipping the job.failed event that follows; a queued
// listener whose error the handler's report gate drops (a client-error
// status) once, through the bridge's text error, because the hook did not
// report it. Without a dispatcher: a queued listener once, through its
// hook, and a queued listener whose error the gate drops not at all, as
// before: no bridge runs, and the hook's report is dropped.
func TestQueueWork_PermanentFailureReportedOnce(t *testing.T) {
	type newQueue func(t *testing.T, app *App) workerQueue
	listener404 := contract.NewHTTPError(404, "listener record gone").Error()
	tests := []struct {
		name           string
		queue          newQueue
		job            func(t *testing.T) queue.Job
		dispatcher     bool
		wantReports    int
		wantErr        string
		wantEvent      string // Extra["event"] of the report, "" for none
		wantListener   bool   // report carries Extra["listener_type"]
		wantJobFailedN int32
	}{
		{
			name: "memory: plain job, dispatcher", queue: memoryQueue,
			job:        func(*testing.T) queue.Job { return &permanentFailureJob{ID: "p1"} },
			dispatcher: true, wantReports: 1, wantErr: "plain job exploded", wantEvent: "job.failed", wantJobFailedN: 1,
		},
		{
			name: "memory: plain job with a client-error status, dispatcher", queue: memoryQueue,
			job:        func(*testing.T) queue.Job { return &clientStatusJob{ID: "c1"} },
			dispatcher: true, wantReports: 1, wantErr: contract.NewHTTPError(404, "upstream record gone").Error(), wantEvent: "job.failed", wantJobFailedN: 1,
		},
		{
			name: "memory: queued listener, dispatcher", queue: memoryQueue, job: explodingListenerJob,
			dispatcher: true, wantReports: 1, wantErr: "queued listener exploded", wantListener: true, wantJobFailedN: 1,
		},
		{
			name: "memory: queued listener, no dispatcher", queue: memoryQueue, job: explodingListenerJob,
			wantReports: 1, wantErr: "queued listener exploded", wantListener: true,
		},
		{
			name: "memory: queued listener with a client-error status, dispatcher", queue: memoryQueue, job: clientStatusListenerJob,
			dispatcher: true, wantReports: 1, wantErr: listener404, wantEvent: "job.failed", wantJobFailedN: 1,
		},
		{
			name: "memory: queued listener with a client-error status, no dispatcher", queue: memoryQueue, job: clientStatusListenerJob,
			wantReports: 0,
		},
		{
			name: "redis: queued listener, dispatcher", queue: redisQueue, job: explodingListenerJob,
			dispatcher: true, wantReports: 1, wantErr: "queued listener exploded", wantListener: true, wantJobFailedN: 1,
		},
		{
			name: "redis: queued listener, no dispatcher", queue: redisQueue, job: explodingListenerJob,
			wantReports: 1, wantErr: "queued listener exploded", wantListener: true,
		},
		{
			name: "redis: queued listener with a client-error status, dispatcher", queue: redisQueue, job: clientStatusListenerJob,
			dispatcher: true, wantReports: 1, wantErr: listener404, wantEvent: "job.failed", wantJobFailedN: 1,
		},
		{
			name: "database: plain job, dispatcher", queue: databaseQueue,
			job:        func(*testing.T) queue.Job { return &permanentFailureJob{ID: "d1"} },
			dispatcher: true, wantReports: 1, wantErr: "plain job exploded", wantEvent: "job.failed", wantJobFailedN: 1,
		},
		{
			name: "database: queued listener, dispatcher", queue: databaseQueue, job: explodingListenerJob,
			dispatcher: true, wantReports: 1, wantErr: "queued listener exploded", wantListener: true, wantJobFailedN: 1,
		},
		{
			name: "database: queued listener, no dispatcher", queue: databaseQueue, job: explodingListenerJob,
			wantReports: 1, wantErr: "queued listener exploded", wantListener: true,
		},
		{
			name: "database: queued listener with a client-error status, dispatcher", queue: databaseQueue, job: clientStatusListenerJob,
			dispatcher: true, wantReports: 1, wantErr: listener404, wantEvent: "job.failed", wantJobFailedN: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, err := NewTestApp()
			if err != nil {
				t.Fatalf("NewTestApp: %v", err)
			}
			defer app.Shutdown(context.Background())
			reports := &failureReports{}
			reports.add(app)
			watch := &jobFailedWatch{}
			app.Services.Events.Listen("job.failed", watch)
			q := tt.queue(t, app)

			opts := queueWorkOptions(app, console.QueueWorkOptions{Tries: 1})
			if opts.Dispatcher == nil {
				t.Fatal("queueWorkOptions attached no dispatcher for an app with events")
			}
			done := func() bool { return watch.n.Load() > 0 }
			if !tt.dispatcher {
				opts.Dispatcher = nil
				done = q.failedRecorded
			}
			runStockWorker(t, q.driver, opts, tt.job(t), done)

			if n := reports.count(); n != tt.wantReports {
				t.Fatalf("failure reported %d times, want %d (errors %v)", n, tt.wantReports, reports.errs)
			}
			if got := watch.n.Load(); got != tt.wantJobFailedN {
				t.Errorf("job.failed delivered %d times, want %d", got, tt.wantJobFailedN)
			}
			if tt.wantReports == 0 {
				return
			}
			if got := reports.errs[0].Error(); got != tt.wantErr {
				t.Errorf("reported error = %q, want %q", got, tt.wantErr)
			}
			extra := reports.ctxs[0].Extra
			if got, _ := extra["event"].(string); got != tt.wantEvent {
				t.Errorf("report Extra[event] = %q, want %q", got, tt.wantEvent)
			}
			if _, got := extra["listener_type"]; got != tt.wantListener {
				t.Errorf("report carries listener_type = %v, want %v (extra %v)", got, tt.wantListener, extra)
			}
		})
	}
}

// TestQueueWork_DatabaseDriverRunsJobFailedHook asserts a plain job with a
// Failed hook, failed permanently by the stock worker on the database
// driver, has its hook run exactly once.
func TestQueueWork_DatabaseDriverRunsJobFailedHook(t *testing.T) {
	app, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	defer app.Shutdown(context.Background())
	q := databaseQueue(t, app)
	id := fmt.Sprintf("hooked-%d", hookedJobSeq.Add(1))

	runStockWorker(t, q.driver, queueWorkOptions(app, console.QueueWorkOptions{Tries: 1}),
		&hookedFailureJob{ID: id}, q.failedRecorded)

	if got := hookedRuns(id); got != 1 {
		t.Errorf("Failed hook ran %d times, want 1", got)
	}
}

// errorsReplaceModule is a chain module whose Start replaces the app's
// error handler.
type errorsReplaceModule struct{ replacement contract.ErrorHandler }

func (m *errorsReplaceModule) Init(*app.Services) error { return nil }
func (m *errorsReplaceModule) Start(s *app.Services) error {
	s.Errors = m.replacement
	return nil
}
func (m *errorsReplaceModule) Shutdown(context.Context) error { return nil }

// TestQueueWork_ListenerFailureReachesReplacedHandler asserts background
// failures are reported to the error handler the app holds once bootstrap
// has run, when that handler replaced the one New built: from a chain
// module's Start, or from the Errors callback. The stock worker's failed
// queued listener is reported once, on the replacement, through the
// listener's hook (the report carries listener_type), and a failed plain
// job once more, on the replacement, through the job.failed bridge; the
// handler New built reports nothing.
func TestQueueWork_ListenerFailureReachesReplacedHandler(t *testing.T) {
	tests := []struct {
		name    string
		replace func(a *App, h contract.ErrorHandler)
	}{
		{
			name: "chain module Start",
			replace: func(a *App, h contract.ErrorHandler) {
				a.Modules(func(r *chain.ModuleRegistry) { r.Add(&errorsReplaceModule{replacement: h}) })
			},
		},
		{
			name: "Errors callback",
			replace: func(a *App, h contract.ErrorHandler) {
				a.Errors(func(contract.ErrorHandler) { a.Services.Errors = h })
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := NewTestApp()
			if err != nil {
				t.Fatalf("NewTestApp: %v", err)
			}
			defer a.Shutdown(context.Background())
			original := &failureReports{}
			original.add(a)
			replacement := &failureReports{}
			handler := problem.NewHandler(problem.WithReporters(replacement.reporter()))
			tt.replace(a, handler)
			if err := a.Bootstrap(); err != nil {
				t.Fatalf("Bootstrap: %v", err)
			}
			if a.Services.Errors != contract.ErrorHandler(handler) {
				t.Fatalf("Services.Errors after Bootstrap is %T, not the replacement", a.Services.Errors)
			}
			watch := &jobFailedWatch{}
			a.Services.Events.Listen("job.failed", watch)
			opts := queueWorkOptions(a, console.QueueWorkOptions{Tries: 1})

			runStockWorker(t, a.Queue, opts, explodingListenerJob(t), func() bool { return watch.n.Load() == 1 })
			if n := replacement.count(); n != 1 {
				t.Fatalf("replacement handler reported the listener %d times, want 1 (errors %v)", n, replacement.errs)
			}
			if _, ok := replacement.ctxs[0].Extra["listener_type"]; !ok {
				t.Errorf("replacement report did not come from the listener's hook (extra %v)", replacement.ctxs[0].Extra)
			}

			runStockWorker(t, a.Queue, opts, &permanentFailureJob{ID: "replaced"}, func() bool { return watch.n.Load() == 2 })
			if n := replacement.count(); n != 2 {
				t.Fatalf("replacement handler made %d reports after the plain job, want 2 (errors %v)", n, replacement.errs)
			}
			if got, _ := replacement.ctxs[1].Extra["event"].(string); got != "job.failed" {
				t.Errorf("plain job report Extra[event] = %q, want job.failed", got)
			}
			if n := original.count(); n != 0 {
				t.Errorf("original handler reported %d times, want 0 (errors %v)", n, original.errs)
			}
		})
	}
}

// slowExplodingListener is a queued listener whose every run fails after a
// short pause, so the test goroutine acts while a worker is failing jobs.
type slowExplodingListener struct{}

func (slowExplodingListener) Handle(context.Context, interface{}) error {
	time.Sleep(time.Millisecond)
	return errors.New("slow queued listener exploded")
}
func (slowExplodingListener) Async() bool { return true }

// TestWireFailureReporters_HandlerSwapWhileWorkerFails asserts replacing
// the error handler and re-running wireFailureReporters while a worker is
// failing queued listeners is free of data races (run under -race) and
// loses no report: a worker without a dispatcher fails every listener
// job, each reported through its hook, while the test goroutine keeps
// swapping Services.Errors for a new handler and re-wiring until every job
// has failed; the reports across every handler used add up to the failed
// jobs. A job failed after the last swap is reported on the last handler.
func TestWireFailureReporters_HandlerSwapWhileWorkerFails(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	defer a.Shutdown(context.Background())
	first := &failureReports{}
	first.add(a)
	handlers := []*failureReports{first}

	q := memoryQueue(t, a)
	mem := a.Queue.(*queue.MemoryDriver)
	failedJobs := func() int {
		failed, err := mem.GetFailed("default")
		if err != nil {
			return -1
		}
		return len(failed)
	}
	const jobs = 12
	for i := 0; i < jobs; i++ {
		job := queuedListenerJob(t, "velocity.slowExplodingListener", slowExplodingListener{})
		if err := q.driver.PushCtx(context.Background(), job, "default"); err != nil {
			t.Fatalf("push: %v", err)
		}
	}
	opts := queueWorkOptions(a, console.QueueWorkOptions{Tries: 1})
	opts.Dispatcher = nil
	w := console.NewQueueWorker(q.driver, opts)
	w.Start(context.Background())
	stopped := false
	defer func() {
		if !stopped {
			w.Stop()
		}
	}()

	// Swap until every job has failed (the worker backs off after each
	// failure, so swaps land before, during and after each job's hook).
	for i := 0; i < 2000 && failedJobs() < jobs; i++ {
		r := &failureReports{}
		handlers = append(handlers, r)
		a.Services.Errors = problem.NewHandler(problem.WithReporters(r.reporter()))
		wireFailureReporters(a)
		time.Sleep(2 * time.Millisecond)
	}
	testsync.Eventually(t, func() bool { return failedJobs() == jobs }, 10*time.Second, "every listener job failed")
	w.Stop()
	stopped = true

	total, reporting := 0, 0
	for _, r := range handlers {
		if n := r.count(); n > 0 {
			total += n
			reporting++
		}
	}
	if total != jobs {
		t.Fatalf("handlers made %d reports in all, want %d (one per failed job)", total, jobs)
	}
	t.Logf("%d failed jobs reported across %d of %d handlers", total, reporting, len(handlers))

	last := handlers[len(handlers)-1]
	before := last.count()
	runStockWorker(t, q.driver, opts, queuedListenerJob(t, "velocity.slowExplodingListener", slowExplodingListener{}),
		func() bool { return failedJobs() == jobs+1 })
	if got := last.count(); got != before+1 {
		t.Errorf("last handler made %d reports for the job failed after the swaps, want 1", got-before)
	}
}

// contextPanicError is a server error implementing contract.Contextual
// whose Context panics.
type contextPanicError struct{}

func (contextPanicError) Error() string           { return "listener context exploded" }
func (contextPanicError) Context() map[string]any { panic("listener context") }

// contextPanicQueuedListener is a queued listener whose every run fails
// with a contextPanicError.
type contextPanicQueuedListener struct{}

func (contextPanicQueuedListener) Handle(context.Context, interface{}) error {
	return contextPanicError{}
}
func (contextPanicQueuedListener) Async() bool { return true }

// TestQueueWork_ListenerReportThatPanicsIsReportedByBridge asserts a queued
// listener failure whose report the error handler abandons (the error's
// Context panics before any reporter runs) is not taken as reported: the
// stock worker leaves the job.failed event's error unmarked, and the
// failure-report bridge reports it once, as the event's text error, which
// has no Context to panic.
func TestQueueWork_ListenerReportThatPanicsIsReportedByBridge(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	defer a.Shutdown(context.Background())
	reports := &failureReports{}
	reports.add(a)
	watch := &jobFailedWatch{}
	a.Services.Events.Listen("job.failed", watch)

	runStockWorker(t, a.Queue, queueWorkOptions(a, console.QueueWorkOptions{Tries: 1}),
		queuedListenerJob(t, "velocity.contextPanicQueuedListener", contextPanicQueuedListener{}),
		func() bool { return watch.n.Load() > 0 })

	if n := reports.count(); n != 1 {
		t.Fatalf("failure reported %d times, want 1 (errors %v)", n, reports.errs)
	}
	if got := reports.errs[0].Error(); got != "listener context exploded" {
		t.Errorf("reported error = %q, want the listener error's text", got)
	}
	if _, ok := reports.errs[0].(contract.Contextual); ok {
		t.Errorf("reported error %T is Contextual, want the bridge's text error", reports.errs[0])
	}
	extra := reports.ctxs[0].Extra
	if got, _ := extra["event"].(string); got != "job.failed" {
		t.Errorf("report Extra[event] = %q, want job.failed", got)
	}
	if _, ok := extra["listener_type"]; ok {
		t.Errorf("report carries listener_type, want the bridge's report (extra %v)", extra)
	}
}

// TestQueueWorkOptions_DispatcherFollowsEvents asserts queue work gets the
// app's event dispatcher, whose dispatches reach the app's listeners, and
// none when events are disabled.
func TestQueueWorkOptions_DispatcherFollowsEvents(t *testing.T) {
	app, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	defer app.Shutdown(context.Background())
	watch := &jobFailedWatch{}
	app.Services.Events.Listen("job.failed", watch)
	opts := queueWorkOptions(app, console.QueueWorkOptions{Queue: "emails"})
	if opts.Queue != "emails" || opts.Logger == nil || opts.Dispatcher == nil {
		t.Fatalf("options = {Queue: %q, Logger: %v, Dispatcher set: %v}, want emails, the app logger and a dispatcher", opts.Queue, opts.Logger, opts.Dispatcher != nil)
	}
	if err := opts.Dispatcher(context.Background(), &queue.JobFailed{JobType: "x"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if watch.n.Load() != 1 {
		t.Errorf("job.failed delivered %d times through the dispatcher, want 1", watch.n.Load())
	}

	quiet, err := NewTestApp(WithoutEvents())
	if err != nil {
		t.Fatalf("NewTestApp(WithoutEvents): %v", err)
	}
	defer quiet.Shutdown(context.Background())
	if opts := queueWorkOptions(quiet, console.QueueWorkOptions{}); opts.Dispatcher != nil {
		t.Error("queueWorkOptions attached a dispatcher although events are disabled")
	}
}

// listenerFactoryModule is a module whose Start registers its own
// EventListenerJob factory, as an app normalising legacy payloads would.
type listenerFactoryModule struct{}

func (listenerFactoryModule) Init(*app.Services) error { return nil }
func (listenerFactoryModule) Start(*app.Services) error {
	queue.RegisterJob(func(data []byte) (*events.EventListenerJob, error) {
		var job events.EventListenerJob
		if err := json.Unmarshal(data, &job); err != nil {
			return nil, err
		}
		job.MaxRetries = 7
		return &job, nil
	})
	return nil
}
func (listenerFactoryModule) Shutdown(context.Context) error { return nil }

// TestWireFailureReporters_KeepsModuleListenerJobFactory asserts the
// failure-reporter re-installs that follow the module lifecycle and
// bootstrap leave an EventListenerJob factory a module's Start registered
// in place: a listener job hydrated from its wire form afterwards comes
// through that factory.
func TestWireFailureReporters_KeepsModuleListenerJobFactory(t *testing.T) {
	// The job registry is package-wide: put the default factory back for
	// the tests that follow.
	t.Cleanup(func() { eventqueue.InitializeQueueIntegration(nil, nil, nil) })
	a, err := NewTestApp(WithModules(listenerFactoryModule{}))
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	defer a.Shutdown(context.Background())
	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	payload, err := queue.MarshalJob(&events.EventListenerJob{ListenerType: "velocity.factoryProbe"}, "default")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	job, err := queue.HydrateJob(payload)
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if got := job.(*events.EventListenerJob).MaxRetries; got != 7 {
		t.Errorf("hydrated listener job MaxRetries = %d, want 7 from the module's factory", got)
	}
}
