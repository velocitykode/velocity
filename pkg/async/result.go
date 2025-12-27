package async

import (
	"sync"
)

// Result wraps an async operation's outcome
type Result[T any] struct {
	valueCh  chan T
	errorCh  chan error
	done     chan struct{}
	timedOut bool
	mu       sync.RWMutex
}

// NewResult creates a new Result
func NewResult[T any]() *Result[T] {
	return &Result[T]{
		valueCh: make(chan T, 1),
		errorCh: make(chan error, 1),
		done:    make(chan struct{}),
	}
}

// Get blocks until result is ready
func (r *Result[T]) Get() (T, error) {
	select {
	case v := <-r.valueCh:
		close(r.done)
		return v, nil
	case err := <-r.errorCh:
		close(r.done)
		var zero T
		return zero, err
	}
}

// GetOrDefault returns the value or a default if there's an error
func (r *Result[T]) GetOrDefault(defaultValue T) T {
	v, err := r.Get()
	if err != nil {
		return defaultValue
	}
	return v
}

// Ready checks if result is available (non-blocking)
func (r *Result[T]) Ready() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// TimedOut returns true if operation timed out
func (r *Result[T]) TimedOut() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.timedOut
}

// setTimedOut marks the result as timed out
func (r *Result[T]) setTimedOut() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.timedOut = true
}
