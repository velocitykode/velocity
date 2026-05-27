package async

import (
	"context"
	"sync"
	"time"

	"github.com/velocitykode/velocity/internal/panicerr"
)

// All runs functions in parallel and waits for every result. It returns the
// collected values in submission order and the first non-nil error seen.
//
// Errors here come from recovered panics inside the fn closures (see Run):
// a panicking fn would otherwise produce a zero value in the result slot
// with no signal to the caller, which has masked real failures with security
// implications (X-04). Callers MUST check the returned error before reading
// the values slice. If any fn panicked, the returned values slice still has
// len(fns) elements but the indices corresponding to panicked fns hold zero
// values; treat the slice as undefined when err != nil.
//
// All always waits for every fn to finish so spawned goroutines don't leak.
func All[T any](fns ...func() T) ([]T, error) {
	results := make([]*Result[T], len(fns))

	for i, fn := range fns {
		results[i] = Run(fn)
	}

	values := make([]T, len(fns))
	var firstErr error
	for i, r := range results {
		v, err := r.Get()
		values[i] = v
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return values, firstErr
}

// AllWithError runs functions in parallel and returns the first non-nil
// error encountered, where "error" is one of:
//
//  1. A panic recovered inside fn (surfaced via Run's deferred recover and
//     observed on the Result's error channel as r.Get's second return).
//  2. A normal error returned by fn (carried inside the resultPair).
//
// The panic-converted error is checked first so a panicking fn cannot be
// masked by a later fn's normal error. If any fn panics or returns an
// error, AllWithError returns nil values and that error; otherwise it
// returns the collected values in submission order.
//
// AllWithError always waits for every fn to finish so spawned goroutines
// do not leak. (X-04 follow-up: previously the Get error was discarded,
// so a panic in fn could be silently swallowed into a zero-value entry.)
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

	// Drain every Result before returning so no goroutine is left holding a
	// send on a buffered chan. Track the first error of either flavour.
	values := make([]T, len(fns))
	var firstErr error
	for i, r := range results {
		pair, err := r.Get()
		// Panic-converted errors take precedence: they indicate fn never
		// produced a meaningful pair (Run sends on errorCh, not valueCh).
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if pair.err != nil {
			if firstErr == nil {
				firstErr = pair.err
			}
			continue
		}
		values[i] = pair.value
	}
	if firstErr != nil {
		return nil, firstErr
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

// Map transforms items in parallel and returns the results in input order
// alongside the first non-nil error encountered. As with All, errors here
// originate from recovered panics inside fn; silently dropping them
// previously masked real failures (X-04). Callers MUST check the returned
// error before consuming values. The values slice is always len(items)
// long; indices corresponding to panicked fn calls hold zero values.
//
// Map always waits for every fn to finish so spawned goroutines don't leak.
func Map[T, R any](items []T, fn func(T) R) ([]R, error) {
	results := make([]*Result[R], len(items))

	for i, item := range items {
		item := item // capture for closure
		results[i] = Run(func() R {
			return fn(item)
		})
	}

	values := make([]R, len(items))
	var firstErr error
	for i, r := range results {
		v, err := r.Get()
		values[i] = v
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return values, firstErr
}
