package scheduler

import "errors"

var (
	ErrJobRunning = errors.New("velocity/scheduler: job already running")

	// ErrInvalidCronStep is returned by ParseExpression when a step
	// pattern */n has n<=0. The pre-fix code path called
	// makeRange(min,max,0) which divided by zero and panicked the
	// process - a CLAUDE.md rule-10 violation in library code, since
	// the cron expression is usually caller-supplied.
	ErrInvalidCronStep = errors.New("velocity/scheduler: invalid cron step (must be > 0)")

	// ErrInvalidDayOfMonth is returned by Schedule.Days / Job.Days
	// when an argument is outside the 1-31 day-of-month range.
	// Velocity's Days() targets the day-of-month field (diverges from
	// Laravel's days() which targets day-of-week); the documented
	// contract is day-of-month so we validate against 1-31. Zero in
	// particular wrote an invalid cron field that silently surfaced as
	// "value out of bounds" at the first tick - now we fail at
	// registration.
	ErrInvalidDayOfMonth = errors.New("velocity/scheduler: invalid day of month (must be 1-31)")
)
