package scheduler

import "errors"

var (
	ErrJobRunning = errors.New("velocity/scheduler: job already running")

	// ErrShutdownFromTask is returned by Scheduler.Shutdown, or by a
	// Scheduler.Run waiting on a Shutdown's drain, called from inside work
	// it would wait for: a task's run (its callback, its hooks, the
	// listeners of its events, the logger writing its lines, the release
	// of its lock), a RunInBackground task's completion, a tick (the
	// scheduler-level Before and After callbacks, the Locker, the lines it
	// writes) or a Shutdown's own lines. Nothing changes then: waiting
	// would wait on the caller itself. Stop or restart the scheduler from
	// outside its work, for example from a goroutine the task starts.
	ErrShutdownFromTask = errors.New("velocity/scheduler: shutdown called from inside a task or tick it would wait for")

	// ErrInvalidCronStep is returned by ParseExpression when a step
	// pattern */n has n<=0. The pre-fix code path called
	// makeRange(min,max,0) which divided by zero and panicked the
	// process - a CLAUDE.md rule-10 violation in library code, since
	// the cron expression is usually caller-supplied.
	ErrInvalidCronStep = errors.New("velocity/scheduler: invalid cron step (must be > 0)")

	// ErrInvalidDayOfMonth is returned by Schedule.Days / Job.Days
	// when an argument is outside the 1-31 day-of-month range.
	// Velocity's Days() targets the day-of-month field, not day-of-week;
	// the documented contract is day-of-month so we validate against
	// 1-31. Zero in particular wrote an invalid cron field that silently
	// surfaced as
	// "value out of bounds" at the first tick - now we fail at
	// registration.
	ErrInvalidDayOfMonth = errors.New("velocity/scheduler: invalid day of month (must be 1-31)")
)
