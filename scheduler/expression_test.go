package scheduler

import (
	"testing"
	"time"
)

func TestParseExpression(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"every minute", "* * * * *", false},
		{"every 5 minutes", "*/5 * * * *", false},
		{"daily at midnight", "0 0 * * *", false},
		{"daily at 2:30", "30 2 * * *", false},
		{"range", "0-10 * * * *", false},
		{"list", "0,15,30,45 * * * *", false},
		{"@yearly", "@yearly", false},
		{"@monthly", "@monthly", false},
		{"@weekly", "@weekly", false},
		{"@daily", "@daily", false},
		{"@hourly", "@hourly", false},
		{"invalid fields", "* * *", true},
		{"invalid minute", "60 * * * *", true},
		{"invalid hour", "* 24 * * *", true},
		{"invalid range", "10-5 * * * *", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseExpression(tt.expr)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseExpression() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExpressionIsDue(t *testing.T) {
	tests := []struct {
		name  string
		expr  string
		time  time.Time
		isDue bool
	}{
		{
			name:  "every minute",
			expr:  "* * * * *",
			time:  time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
			isDue: true,
		},
		{
			name:  "specific minute",
			expr:  "30 * * * *",
			time:  time.Date(2024, 1, 1, 12, 30, 0, 0, time.UTC),
			isDue: true,
		},
		{
			name:  "specific minute not matching",
			expr:  "30 * * * *",
			time:  time.Date(2024, 1, 1, 12, 31, 0, 0, time.UTC),
			isDue: false,
		},
		{
			name:  "every 5 minutes",
			expr:  "*/5 * * * *",
			time:  time.Date(2024, 1, 1, 12, 15, 0, 0, time.UTC),
			isDue: true,
		},
		{
			name:  "every 5 minutes not matching",
			expr:  "*/5 * * * *",
			time:  time.Date(2024, 1, 1, 12, 16, 0, 0, time.UTC),
			isDue: false,
		},
		{
			name:  "daily at midnight",
			expr:  "0 0 * * *",
			time:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			isDue: true,
		},
		{
			name:  "daily at midnight not matching",
			expr:  "0 0 * * *",
			time:  time.Date(2024, 1, 1, 0, 1, 0, 0, time.UTC),
			isDue: false,
		},
		{
			name:  "specific day of week (Monday)",
			expr:  "0 0 * * 1",
			time:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), // Monday
			isDue: true,
		},
		{
			name:  "specific day of week not matching",
			expr:  "0 0 * * 1",
			time:  time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), // Tuesday
			isDue: false,
		},
		{
			name:  "range of minutes",
			expr:  "10-15 * * * *",
			time:  time.Date(2024, 1, 1, 12, 12, 0, 0, time.UTC),
			isDue: true,
		},
		{
			name:  "list of minutes",
			expr:  "0,15,30,45 * * * *",
			time:  time.Date(2024, 1, 1, 12, 30, 0, 0, time.UTC),
			isDue: true,
		},
		{
			name:  "specific month",
			expr:  "0 0 1 6 *",
			time:  time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
			isDue: true,
		},
		{
			name:  "specific month not matching",
			expr:  "0 0 1 6 *",
			time:  time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC),
			isDue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, err := ParseExpression(tt.expr)
			if err != nil {
				t.Fatalf("ParseExpression() error = %v", err)
			}
			if got := expr.IsDue(tt.time); got != tt.isDue {
				t.Errorf("Expression.IsDue() = %v, want %v", got, tt.isDue)
			}
		})
	}
}

func TestExpressionNext(t *testing.T) {
	tests := []struct {
		name     string
		expr     string
		from     time.Time
		wantNext time.Time
	}{
		{
			name:     "every minute",
			expr:     "* * * * *",
			from:     time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
			wantNext: time.Date(2024, 1, 1, 12, 1, 0, 0, time.UTC),
		},
		{
			name:     "every 5 minutes",
			expr:     "*/5 * * * *",
			from:     time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
			wantNext: time.Date(2024, 1, 1, 12, 5, 0, 0, time.UTC),
		},
		{
			name:     "daily at midnight",
			expr:     "0 0 * * *",
			from:     time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
			wantNext: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
		},
		{
			name:     "hourly",
			expr:     "0 * * * *",
			from:     time.Date(2024, 1, 1, 12, 30, 0, 0, time.UTC),
			wantNext: time.Date(2024, 1, 1, 13, 0, 0, 0, time.UTC),
		},
		{
			name:     "specific time daily",
			expr:     "30 14 * * *",
			from:     time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
			wantNext: time.Date(2024, 1, 1, 14, 30, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, err := ParseExpression(tt.expr)
			if err != nil {
				t.Fatalf("ParseExpression() error = %v", err)
			}
			if got := expr.Next(tt.from); !got.Equal(tt.wantNext) {
				t.Errorf("Expression.Next() = %v, want %v", got, tt.wantNext)
			}
		})
	}
}

func TestScheduleBuilder(t *testing.T) {
	tests := []struct {
		name     string
		builder  func(*Schedule) *Schedule
		wantExpr string
	}{
		{
			"every minute",
			func(s *Schedule) *Schedule { return s.EveryMinute() },
			"* * * * *",
		},
		{
			"every 5 minutes",
			func(s *Schedule) *Schedule { return s.EveryFiveMinutes() },
			"*/5 * * * *",
		},
		{
			"every 10 minutes",
			func(s *Schedule) *Schedule { return s.EveryTenMinutes() },
			"*/10 * * * *",
		},
		{
			"every 15 minutes",
			func(s *Schedule) *Schedule { return s.EveryFifteenMinutes() },
			"*/15 * * * *",
		},
		{
			"every 30 minutes",
			func(s *Schedule) *Schedule { return s.EveryThirtyMinutes() },
			"*/30 * * * *",
		},
		{
			"hourly",
			func(s *Schedule) *Schedule { return s.Hourly() },
			"0 * * * *",
		},
		{
			"hourly at 15",
			func(s *Schedule) *Schedule { return s.HourlyAt(15) },
			"15 * * * *",
		},
		{
			"daily",
			func(s *Schedule) *Schedule { return s.Daily() },
			"0 0 * * *",
		},
		{
			"daily at 14:30",
			func(s *Schedule) *Schedule { return s.DailyAt("14:30") },
			"30 14 * * *",
		},
		{
			"weekly",
			func(s *Schedule) *Schedule { return s.Weekly() },
			"0 0 * * 0",
		},
		{
			"monthly",
			func(s *Schedule) *Schedule { return s.Monthly() },
			"0 0 1 * *",
		},
		{
			"yearly",
			func(s *Schedule) *Schedule { return s.Yearly() },
			"0 0 1 1 *",
		},
		{
			"weekdays",
			func(s *Schedule) *Schedule { return s.Weekdays() },
			"* * * * 1-5",
		},
		{
			"weekends",
			func(s *Schedule) *Schedule { return s.Weekends() },
			"* * * * 0,6",
		},
		{
			"mondays",
			func(s *Schedule) *Schedule { return s.Mondays() },
			"* * * * 1",
		},
		{
			"sundays",
			func(s *Schedule) *Schedule { return s.Sundays() },
			"* * * * 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSchedule()
			tt.builder(s)
			if got := s.GetExpression(); got != tt.wantExpr {
				t.Errorf("Schedule expression = %v, want %v", got, tt.wantExpr)
			}
		})
	}
}

func TestParseField(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		min     int
		max     int
		want    []int
		wantErr bool
	}{
		{"wildcard", "*", 0, 59, makeRange(0, 59, 1), false},
		{"single value", "5", 0, 59, []int{5}, false},
		{"range", "10-15", 0, 59, []int{10, 11, 12, 13, 14, 15}, false},
		{"step", "*/10", 0, 59, []int{0, 10, 20, 30, 40, 50}, false},
		{"list", "0,15,30,45", 0, 59, []int{0, 15, 30, 45}, false},
		{"invalid value", "60", 0, 59, nil, true},
		{"invalid range", "30-20", 0, 59, nil, true},
		{"out of bounds", "100", 0, 59, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseField(tt.field, tt.min, tt.max)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseField() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !equalSlices(got, tt.want) {
				t.Errorf("parseField() = %v, want %v", got, tt.want)
			}
		})
	}
}

func equalSlices(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
