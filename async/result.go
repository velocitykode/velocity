package async

import (
	"sync"
)

// Result wraps an async operation's outcome.
//
// Get is safe to call multiple times (including concurrently). Producers
// (Run, RunWithTimeout, RunWithContext, Race, RaceWithTimeout) send exactly
// one value on valueCh OR exactly one error on errorCh, and the first Get
// drains that send into the cached outcome and closes done. Subsequent
// calls observe done already-closed and read straight from the cache, so
// no caller blocks on an empty channel.
type Result[T any] struct {
	valueCh chan T
	errorCh chan error
	done    chan struct{}

	timedOut bool
	mu       sync.RWMutex

	// drainOnce gates the single goroutine that reads from valueCh/errorCh
	// and populates the cached outcome. Pairs with closeOnce on done so a
	// second concurrent caller does not also try to close the done channel
	// or drain an already-empty buffered channel (X-03).
	drainOnce sync.Once
	closeOnce sync.Once

	// cached outcome, written by the drainer and read by every caller
	// after done is closed.
	value T
	err   error
}

// NewResult creates a new Result
func NewResult[T any]() *Result[T] {
	return &Result[T]{
		valueCh: make(chan T, 1),
		errorCh: make(chan error, 1),
		done:    make(chan struct{}),
	}
}

// Get blocks until result is ready. Safe to call multiple times: subsequent
// calls return the same value and error as the first call without panicking
// or blocking forever. The internal `done` channel is closed via sync.Once
// so repeated/concurrent calls never trigger `close of closed channel`
// panics (X-03).
func (r *Result[T]) Get() (T, error) {
	// Fast path: outcome already cached. done was closed by the drainer.
	select {
	case <-r.done:
		return r.value, r.err
	default:
	}

	// Race to be the drainer. drainOnce makes exactly one goroutine read
	// from valueCh/errorCh and write the cached outcome. Losers fall
	// through and block on done below.
	r.drainOnce.Do(func() {
		select {
		case v := <-r.valueCh:
			r.value = v
		case e := <-r.errorCh:
			r.err = e
		}
		r.closeOnce.Do(func() { close(r.done) })
	})

	// Both the drainer and all losers wait here until done is closed. For
	// the drainer this is a no-op (done already closed inside the Once).
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
// Ready reflects whether Get has produced a cached outcome (i.e. whether
// done has been closed by the drainer). It returns false while the producer
// goroutine is still running, true once Get has consumed the producer's
// send. Callers using Ready as a polling primitive should call Get to
// trigger the drain.
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
