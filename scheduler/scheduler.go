package scheduler

import (
	"context"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/drain"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/nilval"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/trace"
)

// TaskScheduler is the interface satisfied by *Scheduler. It covers the
// methods used through app.Services and router.Context for job scheduling
// and lifecycle management.
//
// Configuration methods (SetTimezone, MaintenanceMode, Before and After,
// which return *Scheduler for chaining, and SetLogger) are intentionally
// excluded -- they are only called on the concrete type during bootstrap.
type TaskScheduler interface {
	Add(job *Job) *Job
	Call(callback func()) *Job
	CallE(callback func() error) *Job
	Named(name string, callback func()) *Job
	NamedE(name string, callback func() error) *Job
	Command(command string, args ...string) *Job
	Run(ctx context.Context) error
	Shutdown(ctx context.Context) error
	Jobs() []*Job
	SetEventDispatcher(fn func(ctx context.Context, event interface{}) error)
	SetEnv(env string)
}

// Verify *Scheduler implements TaskScheduler at compile time.
var _ TaskScheduler = (*Scheduler)(nil)

// Scheduler manages and executes scheduled jobs
type Scheduler struct {
	mu      sync.RWMutex
	jobs    []*Job
	running bool
	// started records whether the scheduler has entered Run at least
	// once. It distinguishes a genuine reuse (Run -> Shutdown -> Run,
	// which must work) from a Shutdown that races ahead of a
	// goroutine-spawned Run that never started (e.g. Serve fails fast on
	// ListenAndServe and tears the app down before the scheduler
	// goroutine entered Run).
	started bool
	// terminated blocks Run only when Shutdown was called before the
	// scheduler ever ran (the Serve fail-fast race above). A normal
	// reusable scheduler that has actually run is NOT terminated by
	// Shutdown, so it can Run again afterward.
	terminated      bool
	timezone        *time.Location
	maintenanceMode bool
	beforeCallbacks []func()
	afterCallbacks  []func()

	// appEnv holds the normalised application environment (lowercased +
	// trimmed) used by Job.ShouldRun's Environments() filter. Stored via
	// atomic.Pointer so ShouldRun can read it lock-free: ShouldRun runs
	// under Job.mu, and ValidateJobs takes s.mu THEN Job.mu, so reading
	// appEnv under s.mu from inside ShouldRun would invert that lock order
	// and risk deadlock. A nil pointer means SetEnv was never called.
	appEnv atomic.Pointer[string]

	// logger is stored via atomic.Value so the Run/runDueJobs hot paths
	// can read it lock-free and SetLogger doesn't contend with s.mu.
	logger atomic.Value // holds schedLoggerHolder{contract.Logger}

	// events holds the event dispatcher and handles a failed dispatch
	// through the scheduler's logger.
	events eventemit.Emitter

	// own holds the goroutines running the scheduler's work: a tick
	// (runDueJobs), a task's run, a RunInBackground task's completion, a
	// stop's lines. Each enters before it runs any user code and leaves
	// after the last (for a task, after its release), so Shutdown, which
	// would wait on them, refuses a call from one of them (an error
	// wrapping contract.ErrStopFromOwnWork).
	own drain.Owner

	// run is the current run, or the last one once it stopped; nil before
	// the first Run. Guarded by mu. Each tick and task is a unit admitted
	// into the run it belongs to, and a run's loop acts on that run only,
	// so an older run's loop never ticks into, or stops, a newer one.
	run *schedRun

	// adhoc is the run the ticks of a scheduler that never ran are
	// admitted into (runDueJobs called directly, outside Run). Guarded by
	// mu.
	adhoc *drain.Run

	// locker acquires named distributed locks for WithoutOverlapping() and
	// OnOneServer() jobs. Defaults to an InMemoryLocker (process-local) so
	// single-instance deployments and tests work out of the box. Production
	// HA deployments MUST install a shared-backend Locker via
	// SetLocker(...) (e.g. a cache-backed adapter) before Run() is called;
	// otherwise the "one server" / "no overlap" guarantees degrade to
	// single-process semantics.
	locker Locker

	// oneServerTTL is the TTL for OnOneServer() locks. Short by design --
	// each cron tick gets a fresh contest (the minute is embedded in the
	// key). Default 1h.
	oneServerTTL time.Duration

	// overlapTTL is the default TTL for WithoutOverlapping() locks when
	// the job does not specify its own via WithoutOverlappingFor(d).
	// Default 24h (1440 minutes).
	overlapTTL time.Duration

	// shutdownGrace is how long a RunInBackground process gets after
	// SIGTERM before SIGKILL when the scheduler is shutting down.
	// Configurable for tests; defaults to 5s.
	shutdownGrace time.Duration
}

// schedRun is one run of the scheduler, from Run to the end of the stop
// that drains it.
type schedRun struct {
	run *drain.Run
	// ctx is the run's context. Run derives it from its caller's ctx and
	// the stop cancels it. The ticks pass it into Locker.Acquire so a slow
	// remote backend (e.g. a Redis network hiccup) does not let a lock
	// acquisition outlive the stop: once it is cancelled, a pending
	// Acquire returns ctx.Err() promptly and the task is not dispatched.
	ctx    context.Context
	cancel context.CancelFunc
	// stop is closed when the run stops, ending its loop.
	stop   chan struct{}
	ticker *time.Ticker
}

// schedLoggerHolder wraps a contract.Logger so atomic.Value stores a single
// type.
type schedLoggerHolder struct{ contract.Logger }

// SetEventDispatcher sets the function used to dispatch events; nil
// removes it. Safe to call while the scheduler runs.
func (s *Scheduler) SetEventDispatcher(fn func(ctx context.Context, event interface{}) error) {
	s.events.Set(fn)
}

// New creates a new scheduler instance
func New() *Scheduler {
	s := &Scheduler{
		jobs:          make([]*Job, 0),
		timezone:      time.Local,
		locker:        NewInMemoryLocker(),
		oneServerTTL:  1 * time.Hour,
		overlapTTL:    24 * time.Hour,
		shutdownGrace: 5 * time.Second,
	}
	s.logger.Store(schedLoggerHolder{Logger: fallbacklog.Logger{}})
	s.events.UseLogger(s.log)
	return s
}

// SetLocker installs a distributed Locker used by WithoutOverlapping() and
// OnOneServer() jobs. Pass nil to fall back to a process-local
// InMemoryLocker. Production HA deployments must install a shared-backend
// Locker (cache-backed, advisory lock, etc.) so cluster-wide guarantees
// hold; otherwise both flags degrade to single-process semantics.
//
// Safe to call before Run(); not safe to call concurrently with
// runDueJobs (Run takes a read lock on s.mu to snapshot the locker per
// tick, but the setter takes a write lock so the read won't observe a
// torn value).
func (s *Scheduler) SetLocker(l Locker) *Scheduler {
	s.mu.Lock()
	defer s.mu.Unlock()
	if nilval.Is(l) {
		s.locker = NewInMemoryLocker()
		return s
	}
	s.locker = l
	return s
}

// Locker returns the currently installed Locker. Exposed so the
// bootstrap layer and diagnostics can confirm which backend (the
// process-local InMemoryLocker default or a shared-backend adapter)
// will gate WithoutOverlapping() / OnOneServer() contests. Always
// non-nil after New().
func (s *Scheduler) Locker() Locker {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.locker
}

// log returns the installed logger, or the framework's standalone
// fallback logger when none is installed (including a Scheduler not built
// with New).
func (s *Scheduler) log() contract.Logger {
	v := s.logger.Load()
	if v == nil {
		return fallbacklog.Logger{}
	}
	return fallbacklog.Resolve(v.(schedLoggerHolder).Logger)
}

// SetEnv sets the application environment (e.g. "production", "staging") used by
// jobs with environment constraints. Called during app initialization.
// The value is normalised (lowercased + trimmed) so the Job.Environments
// filter does a like-for-like compare regardless of casing on either side.
func (s *Scheduler) SetEnv(env string) {
	normalised := strings.ToLower(strings.TrimSpace(env))
	s.appEnv.Store(&normalised)
}

// env returns the normalised application environment, or "" when SetEnv
// has never been called. Read lock-free by Job.ShouldRun; see the appEnv
// field comment for the lock-order rationale.
func (s *Scheduler) env() string {
	if p := s.appEnv.Load(); p != nil {
		return *p
	}
	return ""
}

// SetTimezone sets the timezone for the scheduler
func (s *Scheduler) SetTimezone(tz *time.Location) *Scheduler {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timezone = tz
	return s
}

// Timezone returns the timezone cron expressions evaluate in.
func (s *Scheduler) Timezone() *time.Location {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.timezone
}

// SetLogger sets the logger for scheduler diagnostics (start and stop,
// recovered panics, lock-acquire failures, job runs). Nil restores the
// default, the framework's standalone fallback logger, which writes
// warnings and errors to standard error. Safe to call concurrently.
func (s *Scheduler) SetLogger(logger contract.Logger) {
	s.logger.Store(schedLoggerHolder{Logger: fallbacklog.Resolve(logger)})
}

var _ contract.LoggerAware = (*Scheduler)(nil)

// MaintenanceMode enables or disables maintenance mode
func (s *Scheduler) MaintenanceMode(enabled bool) *Scheduler {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maintenanceMode = enabled
	return s
}

// Before registers a callback to run before job execution
func (s *Scheduler) Before(callback func()) *Scheduler {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.beforeCallbacks = append(s.beforeCallbacks, callback)
	return s
}

// After registers a callback to run after job execution
func (s *Scheduler) After(callback func()) *Scheduler {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.afterCallbacks = append(s.afterCallbacks, callback)
	return s
}

// Add registers a new job with the scheduler. Multiple jobs with the same name
// may be added (append semantics -- duplicates are intentional, not an error).
// Panics with *contract.RegistrationError if job is nil.
func (s *Scheduler) Add(job *Job) *Job {
	if job == nil {
		panic(contract.NewRegistrationError("scheduler", "nil job"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job.scheduler = s
	job.timezone = s.timezone
	s.jobs = append(s.jobs, job)
	return job
}

// Call creates a new job that executes a closure. The job's name is best-
// effort derived from runtime.FuncForPC so distinct closures registered via
// Call get distinct default names; unresolvable closures fall back to
// "closure". Note: the auto-derived name is treated as a default (not an
// explicitly-set name) so WithoutOverlapping still surfaces a warning when
// the consumer relies on it without calling .Name(). Use Named(name, fn)
// when you need a stable, human-readable identifier.
func (s *Scheduler) Call(callback func()) *Job {
	job := &Job{
		name:     deriveClosureName(callback),
		callback: callback,
		schedule: &Schedule{},
	}
	return s.Add(job)
}

// CallE creates a new job that executes an error-returning closure. Unlike
// Call (whose closure has no error return), the returned err feeds the
// OnFailure callbacks and the scheduler.task.failed event, so per-task error
// alerting works without forcing the closure to panic. Naming follows the
// same heuristic as Call.
func (s *Scheduler) CallE(callback func() error) *Job {
	job := &Job{
		name:        deriveErrCallbackName(callback),
		errCallback: callback,
		schedule:    &Schedule{},
	}
	return s.Add(job)
}

// Named creates a new job that executes a closure with the given explicit
// name. Prefer this over Call when WithoutOverlapping will be used: the
// overlap guard keys on the job name, so unnamed closures collide with
// each other and silently skip executions.
func (s *Scheduler) Named(name string, callback func()) *Job {
	job := &Job{
		name:         name,
		nameExplicit: true,
		callback:     callback,
		schedule:     &Schedule{},
	}
	return s.Add(job)
}

// NamedE is the error-returning sibling of Named. Combines an explicit
// name (suitable for WithoutOverlapping) with an error-returning closure
// whose returned err feeds OnFailure and scheduler.task.failed.
func (s *Scheduler) NamedE(name string, callback func() error) *Job {
	job := &Job{
		name:         name,
		nameExplicit: true,
		errCallback:  callback,
		schedule:     &Schedule{},
	}
	return s.Add(job)
}

// deriveClosureName returns a best-effort name for a func() closure using
// runtime.FuncForPC. Anonymous funcs get names like "pkg.funcName.func1",
// which is more useful than a literal "closure" but still not stable
// across builds; consumers who care about stable names should use Named.
func deriveClosureName(fn func()) string {
	if fn == nil {
		return "closure"
	}
	return funcNameForPC(reflect.ValueOf(fn).Pointer())
}

// deriveErrCallbackName is the func() error variant of deriveClosureName.
func deriveErrCallbackName(fn func() error) string {
	if fn == nil {
		return "closure"
	}
	return funcNameForPC(reflect.ValueOf(fn).Pointer())
}

// funcNameForPC resolves a function pointer to a human-readable name,
// falling back to "closure" when the symbol is not available.
func funcNameForPC(pc uintptr) string {
	f := runtime.FuncForPC(pc)
	if f == nil {
		return "closure"
	}
	name := f.Name()
	if name == "" {
		return "closure"
	}
	// Trim the package path so the name is short enough to be a useful
	// log/event field; the full path is rarely needed.
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	return name
}

// Command creates a new job that executes a command
func (s *Scheduler) Command(command string, args ...string) *Job {
	job := &Job{
		name:     command,
		command:  command,
		args:     args,
		schedule: &Schedule{},
	}
	return s.Add(job)
}

// Run starts the scheduler. It returns nil immediately when the
// scheduler is already running, or when Shutdown was called before the
// scheduler ever ran (a Run that loses the race against Shutdown: the
// in-process scheduler goroutine spawned by Serve, with Serve failing
// fast and tearing down, must not start ticking against already-closed
// services). A scheduler that has run before can Run again after
// Shutdown: each Run is a run of its own. While the tasks a Shutdown
// admitted are still draining (it timed out), Run waits for them before it
// starts, or returns ctx's error; called from one of those tasks it would
// wait on itself, and returns an error wrapping
// contract.ErrStopFromOwnWork.
//
// When ctx ends, the run stops as if Shutdown had been called, and Run
// returns ctx's error; the stop only ever ends this run, never a later
// one. Run derives the run's context from ctx before it takes the
// scheduler's lock, so a ctx that calls back into the scheduler cannot
// deadlock it.
func (s *Scheduler) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	rr, err := s.beginRun(ctx, runCtx, cancel)
	if rr == nil {
		cancel()
		return err
	}

	s.ValidateJobs()

	fallbacklog.Write(s.log(), func(w contract.Logger) { w.Info("Scheduler started") })

	// Run immediately on start
	s.tick(rr.run, rr.ctx)

	for {
		select {
		case <-ctx.Done():
			_ = s.stopRun(ctx, rr)
			return ctx.Err()
		case <-rr.stop:
			return nil
		case <-rr.ticker.C:
			s.tick(rr.run, rr.ctx)
		}
	}
}

// beginRun publishes a new run with context runCtx, or returns nil and
// the error Run returns. It waits, unlocked, for a previous run still
// draining.
func (s *Scheduler) beginRun(ctx, runCtx context.Context, cancel context.CancelFunc) (*schedRun, error) {
	s.mu.Lock()
	for {
		if s.running || s.terminated {
			s.mu.Unlock()
			return nil, nil
		}
		prev := s.run
		if prev == nil || drain.Closed(prev.run.Finished()) {
			break
		}
		s.mu.Unlock()
		if s.own.Nested() {
			return nil, errchain.Errorf("velocity/scheduler: Run called from inside a run it would wait for: %w", contract.ErrStopFromOwnWork)
		}
		select {
		case <-prev.run.Finished():
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		s.mu.Lock()
	}
	rr := &schedRun{
		run:    s.own.NewRun(),
		ctx:    runCtx,
		cancel: cancel,
		stop:   make(chan struct{}),
		ticker: time.NewTicker(1 * time.Minute), // Check every minute
	}
	s.run = rr
	s.running = true
	s.started = true
	s.mu.Unlock()
	return rr, nil
}

// ValidateJobs scans registered jobs and logs warnings for hazards that
// can only be assessed once the registration chain has settled. Today
// it surfaces:
//
//   - WithoutOverlapping on default-named (auto-derived) jobs, where
//     multiple unnamed closures would collide on the same overlap-guard
//     key and skip silently. Logged as Error.
//   - Deferred validation errors from chainable setters (Schedule.Days
//     out of range, Schedule.Cron with invalid syntax such as */0).
//     Logged as Error so the misconfiguration is loud at boot instead
//     of silently no-op'ing at the first tick.
//
// Called automatically at the top of Run; callers can invoke it
// earlier (e.g. during boot) to fail-fast.
func (s *Scheduler) ValidateJobs() {
	s.mu.RLock()
	jobs := make([]*Job, len(s.jobs))
	copy(jobs, s.jobs)
	s.mu.RUnlock()

	collisions := make(map[string]int)
	for _, j := range jobs {
		j.mu.RLock()
		if j.withoutOverlapping && !j.nameExplicit {
			collisions[j.name]++
		}
		schedErr := j.schedule.ValidationError()
		jobName := j.name
		j.mu.RUnlock()

		if schedErr != nil {
			fallbacklog.Write(s.log(), func(w contract.Logger) {
				w.Error(
					"velocity/scheduler: invalid schedule configuration; job will never fire",
					"task_name", jobName,
					"error", schedErr,
				)
			})
		}
	}
	for name, count := range collisions {
		fallbacklog.Write(s.log(), func(w contract.Logger) {
			w.Error(
				"velocity/scheduler: WithoutOverlapping on job with default name; overlap guard keys on name, so unnamed closures will collide. Use Scheduler.Named(name, fn) or chain .Name(\"...\") to disambiguate.",
				"task_name", name,
				"count", count,
			)
		})
	}
}

// OwnsCaller reports whether the scheduler owns the calling goroutine:
// whether the goroutine is running the scheduler's own work, in any of its
// runs, from a tick (its callbacks, Locker and lines) or a task (its hooks,
// listeners and logger) to a RunInBackground task's completion and a
// stop's diagnostic lines.
// The answer is for this instance only: another scheduler's work, or
// another app's, is not this one's. App.Shutdown asks it because its
// teardown stops the scheduler and waits for that work, so a Shutdown
// called from the work would wait on itself; it is refused instead.
func (s *Scheduler) OwnsCaller() bool {
	return s.own.Nested()
}

// Shutdown stops the scheduler and waits for in-flight jobs to finish,
// honoring the context deadline. Returns ctx.Err() if the context expires
// before all jobs complete. A Shutdown that overlaps one or follows one
// that timed out waits the same way for the run that one stopped, so nil
// always means its jobs have finished and its stopped line was written.
// The shutting-down and stopped lines are written on the goroutine that
// waits for the jobs, so a logger that blocks cannot hold Shutdown past
// ctx; at the deadline the stopped line comes when the jobs really end,
// which can be after Shutdown returned.
//
// Called from inside the scheduler's own work (a task's run, hooks,
// listeners, logger or lock release, a RunInBackground task's completion,
// a tick's callbacks, Locker or lines, or a stop's lines), it returns an
// error wrapping contract.ErrStopFromOwnWork and changes nothing: it would
// wait on its caller.
//
// Past that check, the run stops admitting tasks, and then its context is
// cancelled: any Locker.Acquire in flight on a slow remote backend, plus
// any RunInBackground waiter goroutine, observe the cancellation and
// unwind promptly. Without this, a stuck Acquire could let a job start
// AFTER Shutdown's caller believed shutdown completed.
func (s *Scheduler) Shutdown(ctx context.Context) error {
	if s.own.Nested() {
		return errchain.Errorf("velocity/scheduler: shutdown called from inside a task or tick it would wait for: %w", contract.ErrStopFromOwnWork)
	}
	s.mu.Lock()
	if !s.running && !s.started {
		// Shutdown before the scheduler ever ran: a Run that arrives
		// after this point (goroutine scheduled late) must see the flag
		// and refuse to start against torn-down services. Once the
		// scheduler has actually run, Shutdown leaves it reusable. See
		// the terminated field comment.
		s.terminated = true
	}
	rr := s.run
	s.mu.Unlock()
	if rr == nil {
		return nil
	}
	return s.stopRun(ctx, rr)
}

// stopRun stops rr, when it is the running run, and waits for its drain
// or ctx. The first stop of rr owns the drain: on a goroutine of its own,
// as the scheduler's work, it cancels the run's context, writes the
// shutting-down line, waits for every tick and task the run admitted, and
// writes the stopped line. A stop of a run that is not the current one
// (an older run's loop whose ctx ended) changes nothing: it awaits that
// run only.
func (s *Scheduler) stopRun(ctx context.Context, rr *schedRun) error {
	s.mu.Lock()
	if s.run == rr && s.running {
		s.running = false
		rr.ticker.Stop()
		close(rr.stop)
	}
	s.mu.Unlock()
	return s.own.Stop(ctx, rr.run, func() error {
		rr.cancel()
		fallbacklog.Write(s.log(), func(w contract.Logger) { w.Info("Scheduler shutting down") })
		<-rr.run.Idle()
		fallbacklog.Write(s.log(), func(w contract.Logger) { w.Info("Scheduler stopped") })
		return nil
	}, nil)
}

// runDueJobs runs one tick outside Run's loop: in the current run, or in
// the last one (a stopped run admits nothing, so the tick dispatches
// nothing), or, for a scheduler that never ran, in a run of its own that
// no Shutdown waits for.
func (s *Scheduler) runDueJobs() {
	s.mu.Lock()
	var (
		run    *drain.Run
		runCtx = context.Background()
	)
	switch {
	case s.run != nil:
		run, runCtx = s.run.run, s.run.ctx
	default:
		if s.adhoc == nil {
			s.adhoc = s.own.NewRun()
		}
		run = s.adhoc
	}
	s.mu.Unlock()
	s.tick(run, runCtx)
}

// tick executes all jobs that are due, each a task admitted into run. The
// timezone is snapshotted under the read lock so it cannot be observed
// mid-swap with SetTimezone, and the tick never waits for the tasks it
// starts: the ticker loop must remain non-blocking so slow jobs cannot
// delay subsequent tick evaluation. The stop waits for them.
//
// Maintenance-mode handling: previously this method returned early when
// MaintenanceMode is enabled, which silently no-op'd jobs flagged
// EvenInMaintenanceMode(). Now the gate is per-job -- only jobs that
// opted in run during maintenance.
//
// Distributed locking: jobs flagged WithoutOverlapping() or OnOneServer()
// must contest a Locker before dispatch. Acquisition happens here (NOT
// inside the goroutine) so the ticker loop synchronously gates the
// per-tick contest -- exactly one host wins per minute for OnOneServer
// jobs. The acquired Lock is then passed to the run goroutine which
// releases it via deferred panic-safe Unlock so a panicking hook cannot
// leak the lock for its full TTL.
func (s *Scheduler) tick(run *drain.Run, runCtx context.Context) {
	// The tick runs user code (the scheduler-level callbacks, the Locker,
	// the lines it writes); see own.
	id := s.own.Enter()
	defer s.own.Leave(id)

	// The tick is a unit of its own until it has dispatched every task
	// (the Locker, the callbacks and the lines it writes included), so a
	// stop that starts meanwhile waits for it and the tasks it starts, and
	// every task joins while it holds its unit. A run that is stopping
	// admits no tick: its stop may be waiting already.
	if !run.Admit() {
		return
	}
	defer run.Release()

	s.mu.RLock()
	maintenance := s.maintenanceMode
	jobs := make([]*Job, len(s.jobs))
	copy(jobs, s.jobs)
	beforeCallbacks := s.beforeCallbacks
	afterCallbacks := s.afterCallbacks
	tz := s.timezone // snapshot under RLock, SetTimezone writes under full Lock
	locker := s.locker
	oneServerTTL := s.oneServerTTL
	overlapTTL := s.overlapTTL
	shutdownGrace := s.shutdownGrace
	s.mu.RUnlock()

	if tz == nil {
		tz = time.Local
	}
	now := time.Now().In(tz)

	// Scheduler-level Before/After callbacks run on the ticker goroutine
	// (runDueJobs is driven by Run's select loop). A bare panic in one
	// would kill the whole scheduler, so each is isolated; there is no
	// per-job context here, so a panic is logged rather than dispatched
	// as scheduler.task.failed. A logger that panics while writing that
	// line is contained too, and the line goes to the fallback.
	onCallbackPanic := func(err error) {
		fallbacklog.Write(s.log(), func(w contract.Logger) {
			w.Error("velocity/scheduler: scheduler-level callback panicked", "error", err)
		})
	}

	// Run before callbacks
	for _, callback := range beforeCallbacks {
		runHookIsolated(onCallbackPanic, callback)
	}

	// Check and run each job. Each task is a unit of the run so the stop
	// can wait for it; the loop itself never waits for them. Job.Run
	// already recovers internally; the outer recover below protects
	// against panics in logger.Debug or other surrounding calls so the
	// task's unit is always released.
	for _, job := range jobs {
		if !(job.IsDue(now) && job.ShouldRun()) {
			continue
		}

		// DST fall-back suppression: when the local clock rewinds (e.g.
		// 02:00 -> 01:00 on Nov 1 in America/New_York), the 01:xx wall
		// minutes recur at a different UTC instant. IsDue is purely
		// pattern-matched against the local wall-clock so it returns
		// true on both occurrences. Compare against the last fired wall
		// minute (in tz) and skip the duplicate. Spring-forward (02:00
		// skipped) needs no extra logic -- the minute does not occur,
		// which matches cron(8). markFired is called BEFORE the task
		// joins the run so a follow-up tick within the same wall minute
		// (rare; double-tick races) is suppressed by the next IsDue check.
		if job.alreadyFiredAt(now) {
			continue
		}
		job.markFired(now)

		// Per-job maintenance gate: skip unless the job opted in via
		// EvenInMaintenanceMode().
		job.mu.RLock()
		evenInMaintenance := job.evenInMaintenanceMode
		onOneServer := job.onOneServer
		withoutOverlapping := job.withoutOverlapping
		jobName := job.name
		job.mu.RUnlock()

		if maintenance && !evenInMaintenance {
			continue
		}

		// The task joins the run BEFORE the (possibly slow) Locker
		// acquire calls so the run's count covers the acquire window.
		// Without this, a Locker.Acquire stuck on a remote backend could
		// complete AFTER the stop's wait returned, and the resulting job
		// dispatch would outlive the scheduler. On any skip / acquire
		// error path the unit MUST be released.
		run.Join()

		// Acquire distributed locks BEFORE dispatching the goroutine so
		// the per-tick contest is synchronous. Order: OnOneServer first
		// (short TTL, minute-keyed; gates the per-tick winner across
		// hosts), then WithoutOverlapping (long TTL; gates concurrent
		// overlap of long-running jobs across processes).
		//
		// Both Acquire calls use the scheduler's runCtx so a remote
		// backend hiccup unwinds promptly on Shutdown.
		var oneServerLock, overlapLock Lock
		if onOneServer && locker != nil {
			key := job.oneServerLockKey(now)
			lk, err := acquireLockSafely(locker, runCtx, key, oneServerTTL)
			if err != nil {
				// Release the task's unit on every skip path. ErrLockHeld is quiet contention; anything else
				// is a backend outage / misconfiguration / ctx cancel
				// and operators need to see it at WARN so a Redis
				// outage doesn't look identical to "another host is
				// healthily running this".
				s.skipAfterAcquireFailure(run, "OnOneServer", jobName, key, err)
				continue
			}
			oneServerLock = lk
		}
		if withoutOverlapping && locker != nil {
			key := job.overlapLockKey()
			ttl := job.effectiveOverlapTTL(overlapTTL)
			lk, err := acquireLockSafely(locker, runCtx, key, ttl)
			if err != nil {
				// Pre-dispatch failure: the job has NOT started on
				// this host, so the minute's OnOneServer slot must
				// be returned to the cluster. Holding it would let a
				// host with a stale overlap lock suppress every
				// other host for the rest of the minute. Releasing
				// here is symmetric with the post-dispatch path
				// where the OnOneServer lock is intentionally left
				// to expire by TTL.
				if oneServerLock != nil {
					_ = releaseLockSafely(oneServerLock)
				}
				s.skipAfterAcquireFailure(run, "WithoutOverlapping", jobName, key, err)
				continue
			}
			overlapLock = lk
		}

		// release wraps the overlap-lock release plus the task's unit into
		// a single callback the job goroutine (or, for RunInBackground
		// commands, its waiter goroutine) calls exactly once. The
		// OnOneServer lock is intentionally NOT released: its key
		// embeds the scheduled minute and the next tick gets a fresh
		// contest naturally. Releasing on completion would let a fast
		// host A let host B re-acquire the same minute's slot.
		//
		// The first call claims the release and runs it with nothing held:
		// Lock.Release is the Locker backend's code, user code, which must
		// not run inside a sync.Once that other callers would wait on.
		// Later calls return at once.
		var released atomic.Bool
		release := func() {
			if !released.CompareAndSwap(false, true) {
				return
			}
			if overlapLock != nil {
				_ = releaseLockSafely(overlapLock)
			}
			run.Release()
		}

		// Not async.Go: must call release() on panic so the
		// overlap-lock and the task's unit are freed even if the framing
		// panics outside Job.runInternal's own recovery.
		go func(j *Job, jobName string, oneServerLock Lock, release func()) { //safe-goroutine: release() on panic frees overlap-lock + the task's unit, see comment above
			// Recover any panic from the framing (starting the span,
			// binding the logger, which runs redactors, logger.Debug) so
			// the release path always runs. It is installed first, and
			// releases after the panic line is written (deferred, so a
			// logger that panics while writing it cannot skip the
			// release): the run stays counted until its diagnostic is
			// done, so Shutdown cannot return, and the app close the
			// logger, under the line. Note: Job.runInternal's inner
			// panics are already recovered by Job.Run itself.
			//
			// The goroutine is the scheduler's work from before the first
			// user code until after the release (deferred first, so it
			// runs last).
			id := s.own.Enter()
			defer s.own.Leave(id)
			var log contract.Logger
			defer func() {
				if r := recover(); r != nil {
					defer release()
					logRunPanic(s, log, jobName, r)
				}
			}()
			// The run is a root span, started here so the run's lines
			// and its events carry the same trace (see runInternal).
			tctx := trace.StartSpan(context.Background(), trace.Parent{})
			log = s.log().With(append([]any{"task_name", jobName}, trace.LogFields(tctx)...)...)
			log.Debug("Running job")
			// runInternal owns the release callback. For synchronous
			// jobs it invokes release before returning. For
			// RunInBackground commands that successfully started, it
			// transfers ownership to a waiter goroutine that calls
			// release after cmd.Wait (or after the runCtx-driven
			// SIGTERM+SIGKILL grace period).
			j.runInternal(runCtx, tctx, shutdownGrace, release, &s.own)
			// oneServerLock retained until TTL expiry (see note above).
			_ = oneServerLock
		}(job, jobName, oneServerLock, release)
	}

	// Run after callbacks, these fire per tick, not per job, and must not
	// block on in-flight job goroutines (see docstring). Isolated like the
	// before callbacks so a panic cannot tear down the ticker goroutine.
	for _, callback := range afterCallbacks {
		runHookIsolated(onCallbackPanic, callback)
	}
}

// logRunPanic writes the line for a panic recovered in a due task's run
// framing: through log, the run's bound logger, or through the scheduler's
// logger with the task name when binding it is what panicked. A logger
// that panics while writing it is contained and the line goes to the
// framework's standalone fallback logger instead.
func logRunPanic(s *Scheduler, log contract.Logger, jobName string, r any) {
	const msg = "velocity/scheduler: run due jobs panic recovered"
	err := panicerr.FromRecovered(r)
	if log == nil {
		fallbacklog.Write(s.log(), func(w contract.Logger) { w.Error(msg, "task_name", jobName, "error", err) })
		return
	}
	fallbacklog.Write(log, func(w contract.Logger) { w.Error(msg, "error", err) }, "task_name", jobName)
}

// skipAfterAcquireFailure writes the line for a due task skipped because
// a Locker.Acquire for guard failed, then releases the task's unit of run.
// The unit is released only after the line is written, so Shutdown cannot
// return, and the app close the logger, under it. A logger that panics
// while writing is contained (it would otherwise escape the tick and kill
// the ticker goroutine): the line goes to the framework's standalone
// fallback logger, and the unit is still released.
func (s *Scheduler) skipAfterAcquireFailure(run *drain.Run, guard, jobName, key string, err error) {
	defer run.Release()
	fallbacklog.Write(s.log(), func(w contract.Logger) { logAcquireFailure(w, guard, jobName, key, err) })
}

// acquireLockSafely calls locker.Acquire, converting a panic in it
// (panicerr.FromRecovered) into the returned error: a misbehaving backend
// is then a failed acquire, skipped and warned about like any other,
// instead of a panic that would kill the ticker goroutine.
func acquireLockSafely(locker Locker, ctx context.Context, key string, ttl time.Duration) (lk Lock, err error) {
	defer func() {
		if r := recover(); r != nil {
			lk, err = nil, panicerr.FromRecovered(r)
		}
	}()
	return locker.Acquire(ctx, key, ttl)
}

// releaseLockSafely releases a scheduler Lock and contains any panic
// raised by a misbehaving Locker backend. The caller is the deferred
// release path in a task's goroutine; a panic here would otherwise skip
// the task's unit and could be observed as a goroutine leak.
// Returns the backend's error (if any) so callers may log it; the
// scheduler currently swallows the value, since a release failure is not
// actionable and the lock will expire at TTL.
func releaseLockSafely(lk Lock) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = panicerr.FromRecovered(r)
		}
	}()
	return lk.Release(context.Background())
}

// Jobs returns all registered jobs
func (s *Scheduler) Jobs() []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	jobs := make([]*Job, len(s.jobs))
	copy(jobs, s.jobs)
	return jobs
}

// logAcquireFailure picks the right log level for a Locker.Acquire
// error: ErrLockHeld is healthy contention (Debug, normal at every
// tick when another host is running the job) and everything else is a
// backend outage / runCtx cancel / misconfiguration that ops need to
// see (Warn). Pre-fix this code path used Debug for everything, which
// hid Redis outages behind silent skip behaviour identical to
// "another host owns the lock".
//
// The kind argument names which guard (OnOneServer or
// WithoutOverlapping) failed so the log line is actionable.
func logAcquireFailure(log contract.Logger, kind, jobName, key string, err error) {
	if errchain.Is(err, ErrLockHeld) {
		log.Debug(
			"Skipping job: distributed lock held",
			"guard", kind,
			"task_name", jobName,
			"key", key,
		)
		return
	}
	log.Warn(
		"Skipping job: Locker.Acquire backend error",
		"guard", kind,
		"task_name", jobName,
		"key", key,
		"error", err,
	)
}
