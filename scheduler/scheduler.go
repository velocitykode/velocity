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
	"github.com/velocitykode/velocity/internal/panicerr"
)

// TaskScheduler is the interface satisfied by *Scheduler. It covers the
// methods used through app.Services and router.Context for job scheduling
// and lifecycle management.
//
// Configuration methods that return *Scheduler for chaining (SetTimezone,
// SetLogger, MaintenanceMode, Before, After) are intentionally excluded --
// they are only called on the concrete type during bootstrap.
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
	mu              sync.RWMutex
	jobs            []*Job
	ticker          *time.Ticker
	stop            chan struct{}
	stopped         chan struct{} // closed when Run() exits
	running         bool
	timezone        *time.Location
	maintenanceMode bool
	appEnv          string
	beforeCallbacks []func()
	afterCallbacks  []func()

	// logger is stored via atomic.Value so the Run/runDueJobs hot paths
	// can read it lock-free and SetLogger doesn't contend with s.mu.
	logger atomic.Value // holds schedLoggerHolder{Logger}

	eventDispatcher func(ctx context.Context, event interface{}) error
	runWg           sync.WaitGroup // tracks in-flight job goroutines
}

// schedLoggerHolder wraps a Logger so atomic.Value stores a single type.
type schedLoggerHolder struct{ Logger }

// SetEventDispatcher sets the function used to dispatch events.
func (s *Scheduler) SetEventDispatcher(fn func(ctx context.Context, event interface{}) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventDispatcher = fn
}

// dispatchEvent dispatches an event if a dispatcher is configured. The
// caller-supplied ctx is propagated so listeners observe scheduler-job
// scoped values.
//
// The dispatcher reference is snapshotted under s.mu.RLock() and released
// before invocation so a concurrent SetEventDispatcher cannot race with
// the read, and so the dispatcher itself runs without holding s.mu (the
// dispatcher may take arbitrary time and must not block scheduler
// operations).
func (s *Scheduler) dispatchEvent(ctx context.Context, event interface{}) {
	s.mu.RLock()
	fn := s.eventDispatcher
	s.mu.RUnlock()
	if fn == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	fn(ctx, event)
}

// Logger is the minimal logging interface used by the scheduler. The
// framework's log.Logger satisfies this shape; keeping the contract local
// allows scheduler/ to remain a log-free leaf.
type Logger interface {
	Info(msg string, keysAndValues ...interface{})
	Error(msg string, keysAndValues ...interface{})
	Debug(msg string, keysAndValues ...interface{})
}

// nullLogger is the silent default when SetLogger has not been called.
// It is deliberately inert so the scheduler never emits log output
// through stdlib log, all diagnostic logging flows through the
// framework logger installed at boot.
type nullLogger struct{}

func (nullLogger) Info(string, ...interface{})  {}
func (nullLogger) Error(string, ...interface{}) {}
func (nullLogger) Debug(string, ...interface{}) {}

// New creates a new scheduler instance
func New() *Scheduler {
	s := &Scheduler{
		jobs:     make([]*Job, 0),
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
		timezone: time.Local,
	}
	s.logger.Store(schedLoggerHolder{Logger: nullLogger{}})
	return s
}

// log returns the installed logger. Always non-nil after New().
func (s *Scheduler) log() Logger {
	v := s.logger.Load()
	if v == nil {
		return nullLogger{}
	}
	if l := v.(schedLoggerHolder).Logger; l != nil {
		return l
	}
	return nullLogger{}
}

// SetEnv sets the application environment (e.g. "production", "staging") used by
// jobs with environment constraints. Called during app initialization.
func (s *Scheduler) SetEnv(env string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appEnv = env
}

// SetTimezone sets the timezone for the scheduler
func (s *Scheduler) SetTimezone(tz *time.Location) *Scheduler {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timezone = tz
	return s
}

// SetLogger sets a custom logger
func (s *Scheduler) SetLogger(logger Logger) *Scheduler {
	if logger == nil {
		s.logger.Store(schedLoggerHolder{Logger: nullLogger{}})
	} else {
		s.logger.Store(schedLoggerHolder{Logger: logger})
	}
	return s
}

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
// OnFailure callbacks and the scheduled.failed event, so per-task error
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
// whose returned err feeds OnFailure and scheduled.failed.
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
	// log/event field; full path is rarely needed and Laravel's scheduler
	// also uses short names.
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

// Run starts the scheduler
func (s *Scheduler) Run(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = true
	s.ticker = time.NewTicker(1 * time.Minute) // Check every minute
	s.mu.Unlock()

	s.ValidateJobs()

	s.log().Info("Scheduler started")

	// Run immediately on start
	s.runDueJobs()

	for {
		select {
		case <-ctx.Done():
			_ = s.Shutdown(ctx)
			return ctx.Err()
		case <-s.stop:
			return nil
		case <-s.ticker.C:
			s.runDueJobs()
		}
	}
}

// ValidateJobs scans registered jobs and logs warnings for hazards that
// can only be assessed once the registration chain has settled. Today it
// surfaces WithoutOverlapping on default-named (auto-derived) jobs, where
// multiple unnamed closures would collide on the same overlap-guard key
// and skip silently. Called automatically at the top of Run; callers can
// invoke it earlier (e.g. during boot) to fail-fast.
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
		j.mu.RUnlock()
	}
	for name, count := range collisions {
		s.log().Error(
			"velocity/scheduler: WithoutOverlapping on job with default name; overlap guard keys on name, so unnamed closures will collide. Use Scheduler.Named(name, fn) or chain .Name(\"...\") to disambiguate.",
			"name", name,
			"count", count,
		)
	}
}

// Shutdown stops the scheduler and waits for in-flight jobs to finish,
// honoring the context deadline. Returns ctx.Err() if the context expires
// before all jobs complete.
func (s *Scheduler) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}

	s.running = false
	if s.ticker != nil {
		s.ticker.Stop()
	}
	close(s.stop)
	s.mu.Unlock()

	s.log().Info("Scheduler shutting down")

	// Wait for in-flight jobs with ctx deadline. Recover from panics so
	// Shutdown always signals completion via done.
	done := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.log().Error("velocity/scheduler: shutdown wait panic recovered", "error", panicerr.FromRecovered(r))
			}
			close(done)
		}()
		s.runWg.Wait()
	}()

	select {
	case <-done:
		s.log().Info("Scheduler stopped")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runDueJobs executes all jobs that are due. The timezone is snapshotted
// under the read lock so it cannot be observed mid-swap with SetTimezone,
// and runWg.Wait() is intentionally NOT invoked here, the ticker loop
// must remain non-blocking so slow jobs cannot delay subsequent tick
// evaluation. Shutdown() waits on runWg after the ticker has stopped.
func (s *Scheduler) runDueJobs() {
	s.mu.RLock()
	if s.maintenanceMode {
		s.mu.RUnlock()
		return
	}
	jobs := make([]*Job, len(s.jobs))
	copy(jobs, s.jobs)
	beforeCallbacks := s.beforeCallbacks
	afterCallbacks := s.afterCallbacks
	tz := s.timezone // snapshot under RLock, SetTimezone writes under full Lock
	s.mu.RUnlock()

	if tz == nil {
		tz = time.Local
	}
	now := time.Now().In(tz)

	// Run before callbacks
	for _, callback := range beforeCallbacks {
		callback()
	}

	// Check and run each job. runWg tracks in-flight goroutines so Shutdown()
	// can wait for them; the loop itself must not block on runWg.Wait().
	// Job.Run already recovers internally; the outer recover below protects
	// against panics in logger.Debug or other surrounding calls so
	// runWg.Done always fires.
	for _, job := range jobs {
		if job.IsDue(now) && job.ShouldRun() {
			s.runWg.Add(1)
			go func(j *Job) {
				defer s.runWg.Done()
				defer func() {
					if r := recover(); r != nil {
						s.log().Error("velocity/scheduler: run due jobs panic recovered", "name", j.name, "error", panicerr.FromRecovered(r))
					}
				}()
				s.log().Debug("Running job", "name", j.name)
				_ = j.Run()
			}(job)
		}
	}

	// Run after callbacks, these fire per tick, not per job, and must not
	// block on in-flight job goroutines (see docstring).
	for _, callback := range afterCallbacks {
		callback()
	}
}

// Jobs returns all registered jobs
func (s *Scheduler) Jobs() []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	jobs := make([]*Job, len(s.jobs))
	copy(jobs, s.jobs)
	return jobs
}
