package queue

import (
	"testing"
	"time"
)

func TestExponentialBackoff(t *testing.T) {
	tests := []struct {
		name    string
		base    time.Duration
		max     time.Duration
		attempt int
		want    time.Duration
	}{
		{"attempt 1", time.Second, 5 * time.Minute, 1, time.Second},
		{"attempt 2", time.Second, 5 * time.Minute, 2, 2 * time.Second},
		{"attempt 3", time.Second, 5 * time.Minute, 3, 4 * time.Second},
		{"attempt 4", time.Second, 5 * time.Minute, 4, 8 * time.Second},
		{"attempt 5", time.Second, 5 * time.Minute, 5, 16 * time.Second},
		{"attempt 10", time.Second, 5 * time.Minute, 10, 5 * time.Minute},
		{"capped at max", time.Second, 5 * time.Minute, 20, 5 * time.Minute},
		{"zero attempt", time.Second, 5 * time.Minute, 0, time.Second},
		{"negative attempt", time.Second, 5 * time.Minute, -3, time.Second},
		{"base larger than max", 10 * time.Second, 5 * time.Second, 1, 5 * time.Second},
		{"base equals max", 5 * time.Second, 5 * time.Second, 1, 5 * time.Second},
		{"base equals max attempt 2", 5 * time.Second, 5 * time.Second, 2, 5 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			strategy := ExponentialBackoff(tt.base, tt.max)
			got := strategy(tt.attempt)
			if got != tt.want {
				t.Errorf("ExponentialBackoff(%v, %v)(%d) = %v, want %v", tt.base, tt.max, tt.attempt, got, tt.want)
			}
		})
	}
}

func TestLinearBackoff(t *testing.T) {
	tests := []struct {
		name    string
		step    time.Duration
		max     time.Duration
		attempt int
		want    time.Duration
	}{
		{"attempt 1", 10 * time.Second, time.Minute, 1, 10 * time.Second},
		{"attempt 2", 10 * time.Second, time.Minute, 2, 20 * time.Second},
		{"attempt 3", 10 * time.Second, time.Minute, 3, 30 * time.Second},
		{"attempt 4", 10 * time.Second, time.Minute, 4, 40 * time.Second},
		{"attempt 5", 10 * time.Second, time.Minute, 5, 50 * time.Second},
		{"attempt 6 hits max", 10 * time.Second, time.Minute, 6, 60 * time.Second},
		{"capped at max", 10 * time.Second, time.Minute, 7, time.Minute},
		{"capped at max large", 10 * time.Second, time.Minute, 100, time.Minute},
		{"zero attempt", 10 * time.Second, time.Minute, 0, 10 * time.Second},
		{"negative attempt", 10 * time.Second, time.Minute, -5, 10 * time.Second},
		{"step larger than max", 30 * time.Second, 10 * time.Second, 1, 10 * time.Second},
		{"step equals max", 10 * time.Second, 10 * time.Second, 1, 10 * time.Second},
		{"step equals max attempt 2", 10 * time.Second, 10 * time.Second, 2, 10 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			strategy := LinearBackoff(tt.step, tt.max)
			got := strategy(tt.attempt)
			if got != tt.want {
				t.Errorf("LinearBackoff(%v, %v)(%d) = %v, want %v", tt.step, tt.max, tt.attempt, got, tt.want)
			}
		})
	}
}

func TestFixedBackoff(t *testing.T) {
	tests := []struct {
		name    string
		delay   time.Duration
		attempt int
		want    time.Duration
	}{
		{"attempt 1", 5 * time.Second, 1, 5 * time.Second},
		{"attempt 2", 5 * time.Second, 2, 5 * time.Second},
		{"attempt 5", 5 * time.Second, 5, 5 * time.Second},
		{"attempt 100", 5 * time.Second, 100, 5 * time.Second},
		{"zero attempt", 5 * time.Second, 0, 5 * time.Second},
		{"negative attempt", 5 * time.Second, -1, 5 * time.Second},
		{"zero delay", 0, 3, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			strategy := FixedBackoff(tt.delay)
			got := strategy(tt.attempt)
			if got != tt.want {
				t.Errorf("FixedBackoff(%v)(%d) = %v, want %v", tt.delay, tt.attempt, got, tt.want)
			}
		})
	}
}
