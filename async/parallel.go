package async

import (
	"context"
	"time"
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
				case r.valueCh <- value:
					cancel() // Cancel other goroutines
				case <-ctx.Done():
					return
				}
			}
		}()
	}

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

	semaphore := make(chan struct{}, concurrency)
	done := make(chan struct{}, len(items))

	for _, item := range items {
		item := item            // capture for closure
		semaphore <- struct{}{} // Acquire
		go func() {
			defer func() {
				<-semaphore // Release
				done <- struct{}{}
				if p := recover(); p != nil {
					handlePanic(p)
				}
			}()
			fn(item)
		}()
	}

	// Wait for all to complete
	for range items {
		<-done
	}
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
