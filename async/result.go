package async

import (
	"sync"
)

// Result wraps an async operation's outcome.
//
// Producers (Run, RunWithTimeout, RunWithContext, Race, RaceWithTimeout)
// deliver exactly one outcome via complete/fail. The first delivery wins:
// completeOnce writes the cached value/error and closes done; later calls
// are no-ops. Get is safe to call multiple times (including concurrently)
// and Ready turns true as soon as the producer completes, independent of
// any Get call.
type Result[T any] struct {
	done chan struct{}

	timedOut bool
	mu       sync.RWMutex

	// completeOnce gates the single outcome delivery. It writes value/err
	// and closes done, so a second producer (e.g. a losing Race goroutine
	// or the panic-recovery path) can never double-close done or clobber
	// the cached outcome (X-03).
	completeOnce sync.Once

	// cached outcome, written under completeOnce before done is closed and
	// read by every caller after done is closed.
	value T
	err   error
}

// NewResult creates a new Result
func NewResult[T any]() *Result[T] {
	return &Result[T]{
		done: make(chan struct{}),
	}
}

// complete delivers the outcome. The first call wins: it caches value/err,
// closes done, and returns true. Later calls are no-ops and return false.
func (r *Result[T]) complete(value T, err error) bool {
	won := false
	r.completeOnce.Do(func() {
		r.value = value
		r.err = err
		won = true
		close(r.done)
	})
	return won
}

// fail delivers an error outcome with the zero value. Same first-call-wins
// semantics as complete.
func (r *Result[T]) fail(err error) bool {
	var zero T
	return r.complete(zero, err)
}

// Get blocks until the result is ready. Safe to call multiple times:
// every call returns the same cached value and error the producer
// delivered. The close of `done` happens under completeOnce, so repeated
// or concurrent calls never race on the cached outcome (X-03).
func (r *Result[T]) Get() (T, error) {
	<-r.done
	return r.value, r.err
}

// GetOrDefault returns the value or a default if there's an error
func (r *Result[T]) GetOrDefault(defaultValue T) T {
	v, err := r.Get()
	if err != nil {
		return defaultValue
	}
	return v
}

// Ready checks if result is available (non-blocking).
//
// Ready returns true once the producer has delivered its outcome, whether
// or not Get has been called. Once true, Get returns immediately.
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
