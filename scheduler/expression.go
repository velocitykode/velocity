package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Expression represents a parsed cron expression
type Expression struct {
	minute     []int
	hour       []int
	dayOfMonth []int
	month      []int
	dayOfWeek  []int
	raw        string
}

// ParseExpression parses a cron expression string
func ParseExpression(expr string) (*Expression, error) {
	// Handle special expressions
	switch expr {
	case "@yearly", "@annually":
		expr = "0 0 1 1 *"
	case "@monthly":
		expr = "0 0 1 * *"
	case "@weekly":
		expr = "0 0 * * 0"
	case "@daily", "@midnight":
		expr = "0 0 * * *"
	case "@hourly":
		expr = "0 * * * *"
	}

	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("invalid cron expression: expected 5 fields, got %d", len(fields))
	}

	e := &Expression{raw: expr}

	var err error
	e.minute, err = parseField(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("invalid minute field: %w", err)
	}

	e.hour, err = parseField(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("invalid hour field: %w", err)
	}

	e.dayOfMonth, err = parseField(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("invalid day of month field: %w", err)
	}

	e.month, err = parseField(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("invalid month field: %w", err)
	}

	e.dayOfWeek, err = parseField(fields[4], 0, 6)
	if err != nil {
		return nil, fmt.Errorf("invalid day of week field: %w", err)
	}

	return e, nil
}

// parseField parses a single cron field
func parseField(field string, min, max int) ([]int, error) {
	if field == "*" {
		return makeRange(min, max, 1), nil
	}

	// Handle step values (e.g., */5)
	if strings.HasPrefix(field, "*/") {
		step, err := strconv.Atoi(field[2:])
		if err != nil {
			return nil, err
		}
		if step <= 0 {
			// makeRange divides by step; step==0 would panic the
			// library (CLAUDE.md rule 10 violation). Surface a
			// typed error so callers can react at registration
			// time instead of at first-tick evaluation.
			return nil, ErrInvalidCronStep
		}
		return makeRange(min, max, step), nil
	}

	// Handle ranges (e.g., 1-5)
	if strings.Contains(field, "-") {
		parts := strings.Split(field, "-")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid range: %s", field)
		}

		start, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, err
		}

		end, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, err
		}

		if start < min || end > max || start > end {
			return nil, fmt.Errorf("range out of bounds: %s", field)
		}

		return makeRange(start, end, 1), nil
	}

	// Handle lists (e.g., 1,3,5)
	if strings.Contains(field, ",") {
		parts := strings.Split(field, ",")
		values := make([]int, 0, len(parts))
		for _, part := range parts {
			val, err := strconv.Atoi(part)
			if err != nil {
				return nil, err
			}
			if val < min || val > max {
				return nil, fmt.Errorf("value out of bounds: %d", val)
			}
			values = append(values, val)
		}
		return values, nil
	}

	// Single value
	val, err := strconv.Atoi(field)
	if err != nil {
		return nil, err
	}

	if val < min || val > max {
		return nil, fmt.Errorf("value out of bounds: %d", val)
	}

	return []int{val}, nil
}

// makeRange creates a range of integers
func makeRange(min, max, step int) []int {
	values := make([]int, 0, (max-min)/step+1)
	for i := min; i <= max; i += step {
		values = append(values, i)
	}
	return values
}

// IsDue checks if the expression matches the given time.
//
// DST / leap behaviour:
//   - The caller is responsible for passing t in the scheduler's configured
//     timezone (Scheduler.runDueJobs uses time.Now().In(tz)). Go's time.Time
//     carries its own *time.Location and time.Time.In does not shift the
//     underlying instant - only how Hour/Minute/Month/Day are reported. This
//     matches cron(8) semantics.
//   - Spring-forward: during the spring-forward DST transition the hour 02:00
//     is skipped in the local representation, so a job scheduled exactly at
//     02:00 in that zone will not fire that day. This matches cron(8) /
//     cronie's behaviour: a missing wall minute simply does not fire.
//   - Fall-back: during the fall-back transition the 01:00 hour repeats; the
//     ticker evaluates IsDue once per repeated minute and Expression.IsDue
//     itself returns true on both occurrences (it is purely pattern-matched
//     against the local wall-clock). The duplicate dispatch is suppressed
//     one layer up in Scheduler.runDueJobs by comparing the wall-clock
//     minute against Job.lastFiredWallMinute; the job fires exactly once
//     per distinct local minute, matching cronie which de-duplicates
//     ambiguous-hour repeats in the same way.
//   - Leap seconds are transparent to time.Time - Go never surfaces them -
//     so no special handling is required here.
func (e *Expression) IsDue(t time.Time) bool {
	// Check minute
	if !contains(e.minute, t.Minute()) {
		return false
	}

	// Check hour
	if !contains(e.hour, t.Hour()) {
		return false
	}

	// Check month
	if !contains(e.month, int(t.Month())) {
		return false
	}

	// Check day of month and day of week
	// If both are specified, either can match (OR logic)
	dayOfMonthMatch := len(e.dayOfMonth) == 0 || contains(e.dayOfMonth, t.Day())
	dayOfWeekMatch := len(e.dayOfWeek) == 0 || contains(e.dayOfWeek, int(t.Weekday()))

	// Special case: if both are wildcards, both must match
	if e.raw != "" {
		fields := strings.Fields(e.raw)
		if len(fields) >= 5 {
			if fields[2] == "*" && fields[4] == "*" {
				return dayOfMonthMatch && dayOfWeekMatch
			}
			if fields[2] != "*" && fields[4] != "*" {
				return dayOfMonthMatch || dayOfWeekMatch
			}
		}
	}

	return dayOfMonthMatch && dayOfWeekMatch
}

// Next returns the next time the expression will match after the given time
func (e *Expression) Next(from time.Time) time.Time {
	// Start from the next minute
	next := from.Truncate(time.Minute).Add(time.Minute)

	// Try to find a match within the next 4 years
	maxIterations := 4 * 365 * 24 * 60 // minutes in 4 years
	for i := 0; i < maxIterations; i++ {
		if e.IsDue(next) {
			return next
		}
		next = next.Add(time.Minute)
	}

	return time.Time{} // No match found
}

// contains checks if a slice contains a value
func contains(slice []int, val int) bool {
	for _, v := range slice {
		if v == val {
			return true
		}
	}
	return false
}
