package queue

import "time"

// BackoffStrategy calculates the delay before retrying a failed job.
// attempt starts at 1 for the first retry.
type BackoffStrategy func(attempt int) time.Duration

// ExponentialBackoff returns a strategy that doubles the delay each attempt.
// delay = min(base * 2^(attempt-1), max)
func ExponentialBackoff(base, max time.Duration) BackoffStrategy {
	return func(attempt int) time.Duration {
		if attempt <= 0 {
			attempt = 1
		}
		delay := base
		for i := 1; i < attempt; i++ {
			delay *= 2
			if delay > max {
				return max
			}
		}
		if delay > max {
			return max
		}
		return delay
	}
}

// LinearBackoff returns a strategy that increases the delay linearly.
// delay = min(step * attempt, max)
func LinearBackoff(step, max time.Duration) BackoffStrategy {
	return func(attempt int) time.Duration {
		if attempt <= 0 {
			attempt = 1
		}
		delay := step * time.Duration(attempt)
		if delay > max {
			return max
		}
		return delay
	}
}

// FixedBackoff returns a strategy that always returns the same delay.
func FixedBackoff(delay time.Duration) BackoffStrategy {
	return func(_ int) time.Duration {
		return delay
	}
}
