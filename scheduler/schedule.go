package scheduler

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule represents when a job should run
type Schedule struct {
	expression string
	minute     string
	hour       string
	dayOfMonth string
	month      string
	dayOfWeek  string

	// validationErr captures any invalid input passed to fluent setters
	// like Days(...). The chainable API cannot return an error directly
	// (returning *Schedule preserves builder ergonomics), so the error
	// is stored here and surfaced by Scheduler.ValidateJobs at Run /
	// boot time. M-36 added this path because Days(0) previously wrote
	// an out-of-range dayOfMonth that only failed at the first tick.
	validationErr error
}

// NewSchedule creates a new schedule
func NewSchedule() *Schedule {
	return &Schedule{
		minute:     "*",
		hour:       "*",
		dayOfMonth: "*",
		month:      "*",
		dayOfWeek:  "*",
	}
}

// buildExpression builds the cron expression from components
func (s *Schedule) buildExpression() {
	s.expression = fmt.Sprintf("%s %s %s %s %s",
		s.minute, s.hour, s.dayOfMonth, s.month, s.dayOfWeek)
}

// IsDue checks if the schedule is due at the given time. Returns
// false if the schedule has a deferred validation error (e.g. invalid
// Days() input) so a misconfigured job never fires unexpectedly; the
// error is surfaced by Scheduler.ValidateJobs at boot.
func (s *Schedule) IsDue(t time.Time) bool {
	if s.validationErr != nil {
		return false
	}
	if s.expression == "" {
		s.buildExpression()
	}

	expr, err := ParseExpression(s.expression)
	if err != nil {
		return false
	}

	return expr.IsDue(t)
}

// NextRun returns the next run time
func (s *Schedule) NextRun(from time.Time) time.Time {
	if s.expression == "" {
		s.buildExpression()
	}

	expr, err := ParseExpression(s.expression)
	if err != nil {
		return time.Time{}
	}

	return expr.Next(from)
}

// Frequency methods

// EveryMinute runs every minute
func (s *Schedule) EveryMinute() *Schedule {
	s.minute = "*"
	s.buildExpression()
	return s
}

// EveryFiveMinutes runs every five minutes
func (s *Schedule) EveryFiveMinutes() *Schedule {
	s.minute = "*/5"
	s.buildExpression()
	return s
}

// EveryTenMinutes runs every ten minutes
func (s *Schedule) EveryTenMinutes() *Schedule {
	s.minute = "*/10"
	s.buildExpression()
	return s
}

// EveryFifteenMinutes runs every fifteen minutes
func (s *Schedule) EveryFifteenMinutes() *Schedule {
	s.minute = "*/15"
	s.buildExpression()
	return s
}

// EveryThirtyMinutes runs every thirty minutes
func (s *Schedule) EveryThirtyMinutes() *Schedule {
	s.minute = "*/30"
	s.buildExpression()
	return s
}

// Hourly runs every hour
func (s *Schedule) Hourly() *Schedule {
	s.minute = "0"
	s.buildExpression()
	return s
}

// HourlyAt runs every hour at a specific minute
func (s *Schedule) HourlyAt(minute int) *Schedule {
	s.minute = strconv.Itoa(minute)
	s.buildExpression()
	return s
}

// Daily runs daily at midnight
func (s *Schedule) Daily() *Schedule {
	s.minute = "0"
	s.hour = "0"
	s.buildExpression()
	return s
}

// DailyAt runs daily at a specific time
func (s *Schedule) DailyAt(time string) *Schedule {
	parts := strings.Split(time, ":")
	if len(parts) == 2 {
		s.hour = parts[0]
		s.minute = parts[1]
	}
	s.buildExpression()
	return s
}

// At sets the time for daily/weekly jobs
func (s *Schedule) At(time string) *Schedule {
	return s.DailyAt(time)
}

// Weekly runs weekly on Sunday at midnight
func (s *Schedule) Weekly() *Schedule {
	s.minute = "0"
	s.hour = "0"
	s.dayOfWeek = "0"
	s.buildExpression()
	return s
}

// Monthly runs monthly on the first day
func (s *Schedule) Monthly() *Schedule {
	s.minute = "0"
	s.hour = "0"
	s.dayOfMonth = "1"
	s.buildExpression()
	return s
}

// MonthlyOn runs monthly on a specific day and time
func (s *Schedule) MonthlyOn(day int, time string) *Schedule {
	s.dayOfMonth = strconv.Itoa(day)
	parts := strings.Split(time, ":")
	if len(parts) == 2 {
		s.hour = parts[0]
		s.minute = parts[1]
	}
	s.buildExpression()
	return s
}

// Yearly runs yearly on January 1st
func (s *Schedule) Yearly() *Schedule {
	s.minute = "0"
	s.hour = "0"
	s.dayOfMonth = "1"
	s.month = "1"
	s.buildExpression()
	return s
}

// Cron sets a custom cron expression. The expression is parsed
// immediately so invalid syntax (e.g. */0, out-of-range fields) is
// caught at registration via ValidationError instead of silently
// surfacing at the first tick. The string is still stored verbatim
// so GetExpression returns what the caller provided.
func (s *Schedule) Cron(expression string) *Schedule {
	s.expression = expression
	// Parse expression to set components
	parts := strings.Fields(expression)
	if len(parts) >= 5 {
		s.minute = parts[0]
		s.hour = parts[1]
		s.dayOfMonth = parts[2]
		s.month = parts[3]
		s.dayOfWeek = parts[4]
	}
	if _, err := ParseExpression(expression); err != nil {
		s.validationErr = errors.Join(s.validationErr, fmt.Errorf("invalid cron expression %q: %w", expression, err))
	}
	return s
}

// Day constraints

// Days sets specific days of the month.
//
// Velocity's Days() targets the day-of-month cron field. Note this
// diverges from Laravel's days() helper, which targets day-of-week
// (and accepts Sunday=0 .. Saturday=6). Velocity exposes day-of-week
// constants via Sundays() / Mondays() / ... / Weekdays() / Weekends()
// instead. Day-of-month values outside 1-31 are rejected with
// ErrInvalidDayOfMonth; the error is stored on the schedule and
// surfaced by Scheduler.ValidateJobs (and the Run loop) so callers see
// the misconfiguration at boot time, not at the first tick.
func (s *Schedule) Days(days ...int) *Schedule {
	if len(days) == 0 {
		return s
	}
	for _, day := range days {
		if day < 1 || day > 31 {
			s.validationErr = errors.Join(s.validationErr, fmt.Errorf("%w: got %d", ErrInvalidDayOfMonth, day))
			return s
		}
	}
	strs := make([]string, len(days))
	for i, day := range days {
		strs[i] = strconv.Itoa(day)
	}
	s.dayOfMonth = strings.Join(strs, ",")
	s.buildExpression()
	return s
}

// ValidationError returns the deferred validation error captured by
// chainable setters (currently only Days). Returns nil if no input
// was rejected. ValidateJobs and the Run loop use this to surface
// invalid configurations at boot time.
func (s *Schedule) ValidationError() error {
	return s.validationErr
}

// Weekdays runs only on weekdays (Mon-Fri)
func (s *Schedule) Weekdays() *Schedule {
	s.dayOfWeek = "1-5"
	s.buildExpression()
	return s
}

// Weekends runs only on weekends (Sat-Sun)
func (s *Schedule) Weekends() *Schedule {
	s.dayOfWeek = "0,6"
	s.buildExpression()
	return s
}

// Sundays runs on Sundays
func (s *Schedule) Sundays() *Schedule {
	s.dayOfWeek = "0"
	s.buildExpression()
	return s
}

// Mondays runs on Mondays
func (s *Schedule) Mondays() *Schedule {
	s.dayOfWeek = "1"
	s.buildExpression()
	return s
}

// Tuesdays runs on Tuesdays
func (s *Schedule) Tuesdays() *Schedule {
	s.dayOfWeek = "2"
	s.buildExpression()
	return s
}

// Wednesdays runs on Wednesdays
func (s *Schedule) Wednesdays() *Schedule {
	s.dayOfWeek = "3"
	s.buildExpression()
	return s
}

// Thursdays runs on Thursdays
func (s *Schedule) Thursdays() *Schedule {
	s.dayOfWeek = "4"
	s.buildExpression()
	return s
}

// Fridays runs on Fridays
func (s *Schedule) Fridays() *Schedule {
	s.dayOfWeek = "5"
	s.buildExpression()
	return s
}

// Saturdays runs on Saturdays
func (s *Schedule) Saturdays() *Schedule {
	s.dayOfWeek = "6"
	s.buildExpression()
	return s
}

// GetExpression returns the cron expression
func (s *Schedule) GetExpression() string {
	if s.expression == "" {
		s.buildExpression()
	}
	return s.expression
}
