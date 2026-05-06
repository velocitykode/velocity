package async

import (
	"context"
	"sync"
	"time"

	"github.com/velocitykode/velocity/internal/panicerr"
)

// All runs functions in parallel, waits for all
func All[T any](fns ...func() T) []T {
	results := make([]*Result[T], len(fns))

	for i, fn := range fns {
		results[i] = Run(fn)
	}

	values := make([]T, len(fns))
	for i, r := range results {
		v, _ := r.Get() // Ignore errors in basic version
		values[i] = v
	}

	return values
}

// AllWithError runs functions in parallel, returns first error
func AllWithError[T any](fns ...func() (T, error)) ([]T, error) {
	type resultPair struct {
		value T
		err   error
	}

	results := make([]*Result[resultPair], len(fns))

	for i, fn := range fns {
		fn := fn // capture for closure
		results[i] = Run(func() resultPair {
			v, err := fn()
			return resultPair{value: v, err: err}
		})
	}

	values := make([]T, len(fns))
	for i, r := range results {
		pair, _ := r.Get()
		if pair.err != nil {
			return nil, pair.err
		}
		values[i] = pair.value
	}

	return values, nil
}

// Race returns first completed result
func Race[T any](fns ...func() T) *Result[T] {
	r := NewResult[T]()
	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	for _, fn := range fns {
		fn := fn // capture for closure
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					// Ignore panics in losing goroutines
					return
				}
			}()

			select {
			case <-ctx.Done():
				return
			default:
				value := fn()
				select {
				case r.valueCh <- value:
					cancel() // Cancel other goroutines
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Guarantee the cancelCtx is released even if every fn panics or
	// none reaches the send. Safe to call cancel twice.
	go func() {
		wg.Wait()
		cancel()
	}()

	return r
}

// RaceWithTimeout returns first completed result or times out
func RaceWithTimeout[T any](timeout time.Duration, fns ...func() T) *Result[T] {
	r := NewResult[T]()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	// Track if any function completed
	completed := make(chan T, len(fns))

	for _, fn := range fns {
		fn := fn // capture for closure
		go func() {
			defer func() {
				if p := recover(); p != nil {
					// Ignore panics in losing goroutines
					return
				}
			}()

			select {
			case <-ctx.Done():
				return
			default:
				value := fn()
				select {
				case completed <- value:
					// Successfully sent
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Wait for first completion or timeout
	go func() {
		select {
		case value := <-completed:
			cancel()
			r.valueCh <- value
		case <-ctx.Done():
			cancel()
			r.setTimedOut()
			r.errorCh <- context.DeadlineExceeded
		}
	}()

	return r
}

// ForEach executes function for each item with concurrency limit
func ForEach[T any](items []T, concurrency int, fn func(T)) {
	if concurrency <= 0 {
		concurrency = len(items)
	}

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, concurrency)

	for _, item := range items {
		item := item // capture for closure
		wg.Add(1)
		semaphore <- struct{}{} // Acquire
		go func() {
			defer func() {
				if p := recover(); p != nil {
					handlePanic(p)
				}
				<-semaphore // Release
				wg.Done()
			}()
			fn(item)
		}()
	}

	wg.Wait()
}

// GoForEach is a fire-and-forget bounded fan-out. It returns immediately
// while a supervisor goroutine dispatches workers capped at `concurrency`.
// Panics in fn are routed to the package panic handler.
//
// Returns immediately. Callers that need to know when all items have been
// processed should use ForEach (blocking) or wire their own coordination.
// The input slice is snapshotted so the caller can safely mutate it after
// GoForEach returns.
func GoForEach[T any](items []T, concurrency int, fn func(T)) {
	if len(items) == 0 {
		return
	}
	if concurrency <= 0 {
		concurrency = len(items)
	}
	snapshot := make([]T, len(items))
	copy(snapshot, items)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				handlePanic(p)
			}
		}()
		var wg sync.WaitGroup
		semaphore := make(chan struct{}, concurrency)
		for _, item := range snapshot {
			item := item
			wg.Add(1)
			semaphore <- struct{}{}
			go func() {
				defer func() {
					if p := recover(); p != nil {
						handlePanic(p)
					}
					<-semaphore
					wg.Done()
				}()
				fn(item)
			}()
		}
		wg.Wait()
	}()
}

// TryForEach runs fn for every item with bounded concurrency and collects a
// per-item error slice. The returned slice has length == len(items); index i
// is fn's result for items[i], or nil on success. Panics inside fn are
// converted to an error (via panicerr.FromRecovered) and surfaced in the
// matching slot.
//
// Blocking: returns once all items finish.
func TryForEach[T any](items []T, concurrency int, fn func(T) error) []error {
	errs := make([]error, len(items))
	if len(items) == 0 {
		return errs
	}
	if concurrency <= 0 {
		concurrency = len(items)
	}
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, concurrency)
	for i, item := range items {
		i, item := i, item
		wg.Add(1)
		semaphore <- struct{}{}
		go func() {
			defer func() {
				if p := recover(); p != nil {
					handlePanic(p)
					errs[i] = panicerr.FromRecovered(p)
				}
				<-semaphore
				wg.Done()
			}()
			errs[i] = fn(item)
		}()
	}
	wg.Wait()
	return errs
}

// Map transforms items in parallel
func Map[T, R any](items []T, fn func(T) R) []R {
	results := make([]*Result[R], len(items))

	for i, item := range items {
		item := item // capture for closure
		results[i] = Run(func() R {
			return fn(item)
		})
	}

	values := make([]R, len(items))
	for i, r := range results {
		v, _ := r.Get()
		values[i] = v
	}

	return values
}
