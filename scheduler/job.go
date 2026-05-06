package scheduler

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
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

	// State
	running   bool
	lastRun   time.Time
	scheduler *Scheduler
	timezone  *time.Location

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

	// Mutex for preventing overlapping
	mutex *sync.Mutex
}

// getDispatch returns the event dispatch function from the parent scheduler, or nil.
func (j *Job) getDispatch() func(interface{}) {
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

// Run executes the job
func (j *Job) Run() error {
	j.mu.Lock()
	if j.withoutOverlapping && j.running {
		j.mu.Unlock()
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

	defer func() {
		j.mu.Lock()
		j.running = false
		j.mu.Unlock()
	}()

	// Create context with trace for APM
	ctx, traceID, _ := trace.StartTrace(context.Background())

	// Dispatch scheduled.starting event
	dispatchScheduledTaskStarting(j.getDispatch(), ctx, jobName)
	startTime := time.Now()

	// Run before callbacks
	for _, callback := range beforeCallbacks {
		callback()
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
					dispatchScheduledTaskFailed(j.getDispatch(), ctx, jobName, err, time.Since(startTime))
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
					dispatchScheduledTaskFailed(j.getDispatch(), ctx, jobName, err, time.Since(startTime))
					panicDispatched = true
				}
			}()
			j.callback()
		}()
	case j.command != "":
		// Execute command
		cmd := exec.Command(j.command, j.args...)

		// Handle output redirection
		if j.outputFile != "" {
			var file *os.File
			if j.appendOutput {
				file, err = os.OpenFile(j.outputFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			} else {
				file, err = os.OpenFile(j.outputFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
			}
			if err == nil {
				defer file.Close()
				cmd.Stdout = file
				cmd.Stderr = file
			}
		}

		if j.runInBackground {
			err = cmd.Start()
		} else {
			err = cmd.Run()
		}
	}

	duration := time.Since(startTime)

	// Run after callbacks
	for _, callback := range afterCallbacks {
		callback()
	}

	// Run success/failure callbacks and dispatch events
	if err != nil {
		for _, callback := range onFailureCallbacks {
			callback(err)
		}
		if !panicDispatched {
			dispatchScheduledTaskFailed(j.getDispatch(), ctx, jobName, err, duration)
		}
	} else {
		for _, callback := range onSuccessCallbacks {
			callback()
		}
		dispatchScheduledTaskFinished(j.getDispatch(), ctx, jobName, duration)
	}

	// Suppress unused variable warning
	_ = traceID

	return err
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

// WithoutOverlapping prevents job overlap. The overlap guard keys on the
// job name, so an unnamed closure (e.g. one registered via Scheduler.Call
// without a follow-up .Name(...) call) can collide with every other
// unnamed closure in the same scheduler. When the name is still the
// auto-default, log a warning so the collision hazard is surfaced at
// registration time rather than discovered later as silent skipped runs.
func (j *Job) WithoutOverlapping() *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.withoutOverlapping = true
	if j.mutex == nil {
		j.mutex = &sync.Mutex{}
	}
	if !j.nameExplicit && j.scheduler != nil {
		j.scheduler.log().Error(
			"velocity/scheduler: WithoutOverlapping on job with default name; overlap guard keys on name, so multiple unnamed closures will collide. Use Scheduler.Named(name, fn) or chain .Name(\"...\") to disambiguate.",
			"name", j.name,
		)
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
