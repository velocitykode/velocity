package scheduler

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/trace"
)

// Job represents a scheduled task
type Job struct {
	mu sync.RWMutex
	// nameExplicit is true once Name() has been called by the consumer. Used
	// to distinguish a default name (e.g. the "closure" auto-name set by
	// Scheduler.Call) from a user-chosen name. WithoutOverlapping uses this
	// to surface a collision warning when multiple unnamed closures all
	// share the default name.
	nameExplicit bool
	name         string
	callback     func()
	// errCallback is the error-returning variant of callback. When set, it
	// takes precedence: Run() invokes errCallback and feeds its returned
	// error into OnFailure callbacks and the scheduled.failed event. This
	// is the path Scheduler.CallE / Scheduler.NamedE construct.
	errCallback func() error
	command     string
	args        []string
	schedule    *Schedule

	// Execution control
	withoutOverlapping    bool
	onOneServer           bool
	evenInMaintenanceMode bool
	runInBackground       bool

	// withoutOverlappingTTL is the TTL of the distributed lock acquired
	// when WithoutOverlapping() is set. Zero means use the scheduler's
	// default (24h, matching Laravel's $expiresAt = 1440 minutes). Set via
	// WithoutOverlappingFor(d) for finer-grained control. The lock is
	// always released on Job.Run exit (normal, error, or panic); the TTL
	// is the upper bound the lock can be held by a crashed process.
	withoutOverlappingTTL time.Duration

	// State
	running   bool
	lastRun   time.Time
	scheduler *Scheduler
	timezone  *time.Location

	// lastFiredWallMinute is the wall-clock minute string (YYYY-MM-DDTHH:MM
	// in the scheduler timezone) of the most recent successful IsDue+dispatch
	// for this job. Used to suppress the fall-back DST double-fire: when the
	// local clock rewinds from 02:00 -> 01:00, the 01:xx wall minutes repeat
	// at a different UTC instant. The ticker hits each repeated minute and
	// IsDue evaluates true a second time; comparing against this field
	// suppresses the second dispatch so the job fires exactly once per
	// distinct local minute. Spring-forward (02:00 skipped) needs no
	// suppression -- the minute simply does not occur, which mirrors cron(8).
	//
	// Stored as a string (not time.Time) because two distinct instants share
	// the same local wall-clock during fall-back; the string is the
	// equivalence-class key the suppression rule keys on.
	lastFiredWallMinute string

	// Constraints
	when          func() bool
	skip          func() bool
	between       [2]string // [start, end] times
	unlessBetween [2]string
	environments  []string

	// Hooks
	beforeCallbacks    []func()
	afterCallbacks     []func()
	onSuccessCallbacks []func()
	onFailureCallbacks []func(error)

	// Output
	outputFile   string
	appendOutput bool
	emailOutput  string
}

// getDispatch returns the event dispatch function from the parent scheduler,
// or nil. The returned closure captures the scheduler's dispatchEvent method so
// callers pass the per-job ctx through to listeners.
func (j *Job) getDispatch() func(context.Context, interface{}) {
	if j.scheduler != nil {
		return j.scheduler.dispatchEvent
	}
	return nil
}

// IsDue checks if the job should run at the given time
func (j *Job) IsDue(t time.Time) bool {
	j.mu.RLock()
	defer j.mu.RUnlock()

	return j.schedule.IsDue(t)
}

// wallMinuteKey returns the dedup key used by the DST fall-back
// suppression rule: the YYYY-MM-DDTHH:MM wall-clock representation in
// the scheduler timezone. Two distinct UTC instants sharing the same
// local wall-clock (the repeated hour during fall-back) collapse to the
// same key, so the second dispatch is suppressed. The format mirrors
// the oneServerLockKey time component for consistency.
func wallMinuteKey(t time.Time) string {
	return t.Format("2006-01-02T15:04")
}

// alreadyFiredAt reports whether this job has already been dispatched
// for the wall-clock minute of t. Used to suppress the fall-back DST
// double-fire (when the local clock rewinds 02:00 -> 01:00, the 01:xx
// minutes recur at a different UTC instant). Pure read; markFired is
// the writer.
func (j *Job) alreadyFiredAt(t time.Time) bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.lastFiredWallMinute != "" && j.lastFiredWallMinute == wallMinuteKey(t)
}

// markFired records that this job dispatched at the wall-clock minute
// of t. Must be called before dispatching the run goroutine; the
// follow-up tick (the second occurrence of the same wall minute during
// DST fall-back) observes this via alreadyFiredAt and skips.
func (j *Job) markFired(t time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lastFiredWallMinute = wallMinuteKey(t)
}

// ShouldRun checks if the job should run based on constraints
func (j *Job) ShouldRun() bool {
	j.mu.RLock()
	defer j.mu.RUnlock()

	// Check if job is already running and withoutOverlapping is set
	if j.withoutOverlapping && j.running {
		return false
	}

	// Check when condition
	if j.when != nil && !j.when() {
		return false
	}

	// Check skip condition
	if j.skip != nil && j.skip() {
		return false
	}

	// Check time constraints
	now := time.Now().In(j.timezone)
	nowStr := now.Format("15:04")

	// Check between constraint
	if j.between[0] != "" && j.between[1] != "" {
		if nowStr < j.between[0] || nowStr > j.between[1] {
			return false
		}
	}

	// Check unlessBetween constraint
	if j.unlessBetween[0] != "" && j.unlessBetween[1] != "" {
		if nowStr >= j.unlessBetween[0] && nowStr <= j.unlessBetween[1] {
			return false
		}
	}

	// Check environment constraints
	if len(j.environments) > 0 {
		currentEnv := ""
		if j.scheduler != nil {
			currentEnv = j.scheduler.appEnv
		}
		found := false
		for _, env := range j.environments {
			if env == currentEnv {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}

// Run executes the job. This is the legacy entry point preserved for
// direct callers (tests, ad-hoc invocations). The scheduler itself uses
// runInternal, which threads a runCtx + release callback so the
// scheduler can drain locks and runWg accurately even when the job is a
// RunInBackground command whose OS process outlives Job.Run.
func (j *Job) Run() error {
	return j.runInternal(context.Background(), 0, nil)
}

// runInternal is the scheduler-facing entry point.
//
//	ctx           - the scheduler's run-context. Used by RunInBackground
//	                command jobs to drive a graceful SIGTERM+SIGKILL of
//	                the spawned OS process when the scheduler is
//	                shutting down. Synchronous job paths (closure /
//	                non-background command) currently ignore ctx; the
//	                callback itself decides whether to respect it.
//	shutdownGrace - how long the RunInBackground waiter waits between
//	                SIGTERM and SIGKILL when ctx is cancelled. Zero
//	                means "no SIGTERM, just wait for cmd.Wait until
//	                Shutdown's deadline elapses".
//	release       - a callback the scheduler uses to release the
//	                WithoutOverlapping lock and decrement runWg. Called
//	                EXACTLY ONCE: inline before return for synchronous
//	                paths, OR by the RunInBackground waiter goroutine
//	                after cmd.Wait returns. May be nil for direct (test)
//	                callers that have no scheduler-side bookkeeping.
//
// Background ownership transfer: for a RunInBackground command that
// successfully started, ownership of `release` moves into the waiter
// goroutine. runInternal returns nil to the caller in that case so
// the scheduler's dispatch goroutine exits promptly; the waiter holds
// the WithoutOverlapping lock until the OS process exits.
func (j *Job) runInternal(ctx context.Context, shutdownGrace time.Duration, release func()) error {
	if ctx == nil {
		ctx = context.Background()
	}

	j.mu.Lock()
	if j.withoutOverlapping && j.running {
		j.mu.Unlock()
		// In-process overlap gate fired. The scheduler should not have
		// gotten here (Locker.Acquire is the cross-process guard), but
		// if it did we must still balance the release callback.
		if release != nil {
			release()
		}
		return fmt.Errorf("velocity/scheduler: job %s: %w", j.name, ErrJobRunning)
	}
	j.running = true
	j.lastRun = time.Now()
	beforeCallbacks := j.beforeCallbacks
	afterCallbacks := j.afterCallbacks
	onSuccessCallbacks := j.onSuccessCallbacks
	onFailureCallbacks := j.onFailureCallbacks
	jobName := j.name
	j.mu.Unlock()

	clearRunningFlag := func() {
		j.mu.Lock()
		j.running = false
		j.mu.Unlock()
	}

	// Create context with trace for APM. We don't propagate ctx into
	// trace.StartTrace because that API expects a fresh context; future
	// work could thread the runCtx for cancellation propagation into
	// closures, but the closure-API itself does not accept a ctx.
	tctx, traceID, _ := trace.StartTrace(context.Background())

	// Dispatch scheduled.starting event
	dispatchScheduledTaskStarting(j.getDispatch(), tctx, jobName)
	startTime := time.Now()

	// Run before callbacks
	for _, callback := range beforeCallbacks {
		callback()
	}

	// finishSync runs the after-callbacks, success/failure callbacks,
	// completion events, then clears the running flag and calls release.
	// Used by every synchronous return path. RunInBackground's waiter
	// goroutine builds its own finish path (it must defer event
	// dispatch until cmd.Wait completes).
	finishSync := func(err error, panicDispatched bool) {
		duration := time.Since(startTime)
		for _, callback := range afterCallbacks {
			callback()
		}
		if err != nil {
			for _, callback := range onFailureCallbacks {
				callback(err)
			}
			if !panicDispatched {
				dispatchScheduledTaskFailed(j.getDispatch(), tctx, jobName, err, duration)
			}
		} else {
			for _, callback := range onSuccessCallbacks {
				callback()
			}
			dispatchScheduledTaskFinished(j.getDispatch(), tctx, jobName, duration)
		}
		clearRunningFlag()
		if release != nil {
			release()
		}
	}

	// Execute the job
	var (
		err             error
		panicDispatched bool
	)
	switch {
	case j.errCallback != nil:
		// Error-returning closure. Capture both panic-recovered errors AND
		// the closure's returned err so OnFailure / scheduled.failed fire
		// for normal-error paths, not just panics.
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = panicerr.FromRecovered(r)
					dispatchScheduledTaskFailed(j.getDispatch(), tctx, jobName, err, time.Since(startTime))
					panicDispatched = true
				}
			}()
			err = j.errCallback()
		}()
	case j.callback != nil:
		// Execute closure. On panic, dispatch scheduled.failed eagerly so the
		// event is fired before any later path has the chance to swallow err.
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = panicerr.FromRecovered(r)
					dispatchScheduledTaskFailed(j.getDispatch(), tctx, jobName, err, time.Since(startTime))
					panicDispatched = true
				}
			}()
			j.callback()
		}()
	case j.command != "":
		// Execute command. RunInBackground splits into (start now,
		// wait elsewhere) so the WithoutOverlapping lock follows the
		// OS process lifetime, not just the cmd.Start success.
		cmd := exec.Command(j.command, j.args...)

		// Handle output redirection. For RunInBackground the file
		// must outlive Job.Run -- the OS process still writes to it.
		// Ownership of the file handle transfers to the waiter
		// goroutine in that branch; the synchronous path keeps the
		// existing defer Close behaviour.
		var outFile *os.File
		if j.outputFile != "" {
			var openErr error
			if j.appendOutput {
				outFile, openErr = os.OpenFile(j.outputFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			} else {
				outFile, openErr = os.OpenFile(j.outputFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
			}
			if openErr == nil && outFile != nil {
				cmd.Stdout = outFile
				cmd.Stderr = outFile
			} else if openErr != nil {
				err = openErr
			}
		}

		if j.runInBackground && err == nil {
			if startErr := cmd.Start(); startErr != nil {
				err = startErr
				if outFile != nil {
					_ = outFile.Close()
				}
				// Start failed -- fall through to the synchronous
				// finish path so callbacks fire and release is called.
				break
			}
			// Start succeeded. Spawn the waiter and TRANSFER ownership
			// of release + outFile to it. We MUST NOT call finishSync
			// or release in this goroutine -- doing so would let the
			// next scheduler tick re-acquire the lock while the
			// process is still running.
			j.spawnBackgroundWaiter(ctx, shutdownGrace, cmd, outFile, jobName, tctx, startTime, afterCallbacks, onSuccessCallbacks, onFailureCallbacks, clearRunningFlag, release)
			// Release ownership transferred; suppress unused traceID
			// warning and return without invoking finishSync.
			_ = traceID
			return nil
		}

		// Synchronous foreground command.
		if err == nil {
			err = cmd.Run()
		}
		if outFile != nil {
			_ = outFile.Close()
		}
	}

	_ = traceID
	finishSync(err, panicDispatched)
	return err
}

// spawnBackgroundWaiter owns the lock + runWg + completion bookkeeping
// for a RunInBackground command that successfully started. It runs in
// its own goroutine so Job.runInternal can return promptly to the
// scheduler's dispatch goroutine.
//
// Shutdown handling: on ctx.Done it sends SIGTERM, waits up to
// shutdownGrace, then SIGKILL. cmd.Wait is always called so the OS
// process is fully reaped before release fires.
func (j *Job) spawnBackgroundWaiter(
	ctx context.Context,
	shutdownGrace time.Duration,
	cmd *exec.Cmd,
	outFile *os.File,
	jobName string,
	tctx context.Context,
	startTime time.Time,
	afterCallbacks []func(),
	onSuccessCallbacks []func(),
	onFailureCallbacks []func(error),
	clearRunningFlag func(),
	release func(),
) {
	go func() {
		// Panic-safe: a misbehaving callback must not leak the lock.
		defer func() {
			if r := recover(); r != nil {
				if dispatch := j.getDispatch(); dispatch != nil {
					dispatchScheduledTaskFailed(dispatch, tctx, jobName, panicerr.FromRecovered(r), time.Since(startTime))
				}
			}
			if outFile != nil {
				_ = outFile.Close()
			}
			clearRunningFlag()
			if release != nil {
				release()
			}
		}()

		// Wait for the command in a sub-goroutine so the outer select
		// can observe ctx.Done concurrently.
		waitDone := make(chan error, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					waitDone <- panicerr.FromRecovered(r)
					return
				}
			}()
			waitDone <- cmd.Wait()
		}()

		var err error
		select {
		case err = <-waitDone:
			// Normal completion (success or exec error).
		case <-ctx.Done():
			// Shutdown in progress. SIGTERM first; SIGKILL after
			// the grace period if cmd has not exited. cmd.Wait is
			// still observed so the process is reaped.
			j.signalShutdown(cmd, shutdownGrace, jobName)
			err = <-waitDone
			if err == nil {
				err = ctx.Err()
			}
		}

		duration := time.Since(startTime)
		for _, callback := range afterCallbacks {
			callback()
		}
		if err != nil {
			for _, callback := range onFailureCallbacks {
				callback(err)
			}
			dispatchScheduledTaskFailed(j.getDispatch(), tctx, jobName, err, duration)
		} else {
			for _, callback := range onSuccessCallbacks {
				callback()
			}
			dispatchScheduledTaskFinished(j.getDispatch(), tctx, jobName, duration)
		}
	}()
}

// signalShutdown delivers SIGTERM to the running command and waits up
// to shutdownGrace before escalating to SIGKILL. shutdownGrace <= 0
// skips the SIGTERM phase (cmd.Wait will simply observe the natural
// process exit, OR the caller's deadline). Errors from Signal/Kill are
// logged via the scheduler's event dispatcher; they're rarely
// actionable (process may have already exited).
func (j *Job) signalShutdown(cmd *exec.Cmd, shutdownGrace time.Duration, jobName string) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if shutdownGrace <= 0 {
		_ = cmd.Process.Kill()
		return
	}
	// Best-effort SIGTERM. On platforms without signal support (Windows
	// historically returns ENOTSUPP for os.Interrupt to a running
	// process) we fall back to Kill below.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = cmd.Process.Kill()
		return
	}
	// Wait for grace period in a non-blocking way: a timer that fires
	// SIGKILL if cmd is still running. We can't observe cmd.Wait here
	// (only the waiter goroutine does), so the timer always fires; a
	// process that exited cleanly between SIGTERM and the timer will
	// see Kill on a finished process, which is a harmless no-op
	// (returns os.ErrProcessDone on modern Go).
	time.AfterFunc(shutdownGrace, func() {
		_ = cmd.Process.Kill()
	})
}

// Schedule methods (fluent API)

// EveryMinute runs the job every minute
func (j *Job) EveryMinute() *Job {
	j.schedule.EveryMinute()
	return j
}

// EveryFiveMinutes runs the job every five minutes
func (j *Job) EveryFiveMinutes() *Job {
	j.schedule.EveryFiveMinutes()
	return j
}

// EveryTenMinutes runs the job every ten minutes
func (j *Job) EveryTenMinutes() *Job {
	j.schedule.EveryTenMinutes()
	return j
}

// EveryFifteenMinutes runs the job every fifteen minutes
func (j *Job) EveryFifteenMinutes() *Job {
	j.schedule.EveryFifteenMinutes()
	return j
}

// EveryThirtyMinutes runs the job every thirty minutes
func (j *Job) EveryThirtyMinutes() *Job {
	j.schedule.EveryThirtyMinutes()
	return j
}

// Hourly runs the job every hour
func (j *Job) Hourly() *Job {
	j.schedule.Hourly()
	return j
}

// HourlyAt runs the job every hour at a specific minute
func (j *Job) HourlyAt(minute int) *Job {
	j.schedule.HourlyAt(minute)
	return j
}

// Daily runs the job daily at midnight
func (j *Job) Daily() *Job {
	j.schedule.Daily()
	return j
}

// DailyAt runs the job daily at a specific time
func (j *Job) DailyAt(time string) *Job {
	j.schedule.DailyAt(time)
	return j
}

// Weekly runs the job weekly
func (j *Job) Weekly() *Job {
	j.schedule.Weekly()
	return j
}

// Monthly runs the job monthly
func (j *Job) Monthly() *Job {
	j.schedule.Monthly()
	return j
}

// Yearly runs the job yearly
func (j *Job) Yearly() *Job {
	j.schedule.Yearly()
	return j
}

// Cron sets a custom cron expression
func (j *Job) Cron(expression string) *Job {
	j.schedule.Cron(expression)
	return j
}

// At sets the time for daily/weekly jobs
func (j *Job) At(time string) *Job {
	j.schedule.At(time)
	return j
}

// Days sets specific days for the job
func (j *Job) Days(days ...int) *Job {
	j.schedule.Days(days...)
	return j
}

// Weekdays runs the job only on weekdays
func (j *Job) Weekdays() *Job {
	j.schedule.Weekdays()
	return j
}

// Weekends runs the job only on weekends
func (j *Job) Weekends() *Job {
	j.schedule.Weekends()
	return j
}

// Sundays runs the job on Sundays
func (j *Job) Sundays() *Job {
	j.schedule.Sundays()
	return j
}

// Mondays runs the job on Mondays
func (j *Job) Mondays() *Job {
	j.schedule.Mondays()
	return j
}

// Tuesdays runs the job on Tuesdays
func (j *Job) Tuesdays() *Job {
	j.schedule.Tuesdays()
	return j
}

// Wednesdays runs the job on Wednesdays
func (j *Job) Wednesdays() *Job {
	j.schedule.Wednesdays()
	return j
}

// Thursdays runs the job on Thursdays
func (j *Job) Thursdays() *Job {
	j.schedule.Thursdays()
	return j
}

// Fridays runs the job on Fridays
func (j *Job) Fridays() *Job {
	j.schedule.Fridays()
	return j
}

// Saturdays runs the job on Saturdays
func (j *Job) Saturdays() *Job {
	j.schedule.Saturdays()
	return j
}

// Constraint methods

// WithoutOverlapping prevents job overlap. The overlap guard is the
// scheduler's distributed Locker (per-process by default via
// InMemoryLocker, cluster-wide when a shared-backend Locker is installed
// via Scheduler.SetLocker). The Locker acquire happens in
// Scheduler.runDueJobs BEFORE the run goroutine is dispatched, so the
// per-tick contest is synchronous and cross-process.
//
// The overlap guard keys on the job name, so an unnamed closure (e.g.
// one registered via Scheduler.Call without a follow-up .Name(...) call)
// can collide with every other unnamed closure in the same scheduler.
// The collision-hazard warning fires at scheduler start (Run /
// ValidateJobs), not here, so chains like
// s.Call(fn).WithoutOverlapping().Name("nightly") settle before being
// inspected.
func (j *Job) WithoutOverlapping() *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.withoutOverlapping = true
	return j
}

// WithoutOverlappingFor is the TTL-configurable form of WithoutOverlapping.
// The distributed lock acquired before running the job auto-expires after
// ttl; a crashed process therefore cannot wedge the job forever. Default
// (when WithoutOverlapping() is used without this method) is 24h, matching
// Laravel's $expiresAt = 1440 minutes.
func (j *Job) WithoutOverlappingFor(ttl time.Duration) *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.withoutOverlapping = true
	if ttl > 0 {
		j.withoutOverlappingTTL = ttl
	}
	return j
}

// OnOneServer ensures job runs on only one server (requires distributed lock)
func (j *Job) OnOneServer() *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.onOneServer = true
	return j
}

// EvenInMaintenanceMode allows job to run in maintenance mode
func (j *Job) EvenInMaintenanceMode() *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.evenInMaintenanceMode = true
	return j
}

// RunInBackground runs the command in background
func (j *Job) RunInBackground() *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.runInBackground = true
	return j
}

// When adds a condition for job execution
func (j *Job) When(callback func() bool) *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.when = callback
	return j
}

// Skip adds a skip condition
func (j *Job) Skip(callback func() bool) *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.skip = callback
	return j
}

// Between limits execution to a time range
func (j *Job) Between(start, end string) *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.between = [2]string{start, end}
	return j
}

// UnlessBetween prevents execution in a time range
func (j *Job) UnlessBetween(start, end string) *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.unlessBetween = [2]string{start, end}
	return j
}

// Environments limits execution to specific environments
func (j *Job) Environments(environments ...string) *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.environments = environments
	return j
}

// Hooks

// Before registers a callback to run before the job
func (j *Job) Before(callback func()) *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.beforeCallbacks = append(j.beforeCallbacks, callback)
	return j
}

// After registers a callback to run after the job
func (j *Job) After(callback func()) *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.afterCallbacks = append(j.afterCallbacks, callback)
	return j
}

// OnSuccess registers a callback for successful execution
func (j *Job) OnSuccess(callback func()) *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.onSuccessCallbacks = append(j.onSuccessCallbacks, callback)
	return j
}

// OnFailure registers a callback for failed execution
func (j *Job) OnFailure(callback func(error)) *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.onFailureCallbacks = append(j.onFailureCallbacks, callback)
	return j
}

// Output methods

// SendOutputTo redirects output to a file
func (j *Job) SendOutputTo(filename string) *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.outputFile = filename
	j.appendOutput = false
	return j
}

// AppendOutputTo appends output to a file
func (j *Job) AppendOutputTo(filename string) *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.outputFile = filename
	j.appendOutput = true
	return j
}

// EmailOutputTo emails the output (requires mail configuration)
func (j *Job) EmailOutputTo(email string) *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.emailOutput = email
	return j
}

// Name sets the job name. Marking the name as explicitly set silences
// the WithoutOverlapping collision warning (the warning only fires when a
// closure registered via Call() / CallE() retains its default auto-name).
func (j *Job) Name(name string) *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.name = name
	j.nameExplicit = true
	return j
}

// GetName returns the job name
func (j *Job) GetName() string {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.name
}

// GetLastRun returns the last run time
func (j *Job) GetLastRun() time.Time {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.lastRun
}

// GetNextRun returns the next run time
func (j *Job) GetNextRun() time.Time {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.schedule.NextRun(time.Now())
}

// IsRunning returns whether the job is currently running
func (j *Job) IsRunning() bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.running
}

// overlapLockKey returns the distributed-lock key used by
// WithoutOverlapping(). The key is derived from the job name and is shared
// across hosts so the lock is mutually exclusive cluster-wide for the
// configured TTL. Callers MUST hold no Job mutex; this method takes its
// own RLock.
func (j *Job) overlapLockKey() string {
	j.mu.RLock()
	name := j.name
	j.mu.RUnlock()
	return "velocity/scheduler/overlap:" + name
}

// oneServerLockKey returns the distributed-lock key used by OnOneServer().
// The key embeds the scheduled minute (in the scheduler's timezone) so
// each cron tick gets a fresh contest -- exactly one host wins per minute,
// any host that misses the tick (e.g. due to load) cannot starve future
// ticks. Matches Laravel's CacheSchedulingMutex (`<mutexName><time->Hi>`).
// Callers MUST hold no Job mutex; this method takes its own RLock.
func (j *Job) oneServerLockKey(scheduledMinute time.Time) string {
	j.mu.RLock()
	name := j.name
	j.mu.RUnlock()
	// Format like Laravel's `Hi` (HHMM) but include the date so a stuck
	// 1h-TTL lock from a different day cannot accidentally gate today's
	// run. RFC 3339 minute precision is sufficient and unambiguous.
	return "velocity/scheduler/oneserver:" + name + ":" + scheduledMinute.Format("2006-01-02T15:04")
}

// effectiveOverlapTTL returns the TTL to use for WithoutOverlapping's
// distributed lock. Per-job override via WithoutOverlappingFor(ttl) takes
// precedence; otherwise the scheduler-level default (or 24h fallback)
// applies. Callers MUST hold no Job mutex; this method takes its own RLock.
func (j *Job) effectiveOverlapTTL(schedulerDefault time.Duration) time.Duration {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.withoutOverlappingTTL > 0 {
		return j.withoutOverlappingTTL
	}
	if schedulerDefault > 0 {
		return schedulerDefault
	}
	return 24 * time.Hour
}
