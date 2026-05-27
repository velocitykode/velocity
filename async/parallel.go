package async

import (
	"context"
	"sync"
	"time"

	"github.com/velocitykode/velocity/internal/panicerr"
)

// defaultUnboundedCap is the internal hard cap applied to the legacy
// unbounded helpers (All, Map, AllWithError) so a caller passing an
// attacker-controlled slice (e.g. 10k IDs from a request body) cannot
// fan out one goroutine per element (X-02). Set to 1024 because that
// matches the typical kernel fd budget while still being orders of
// magnitude below the millions of goroutines an unbounded fan-out
// would spawn. Workloads that legitimately need more parallelism
// should call the explicit AllN / MapN / AllWithErrorN variants.
const defaultUnboundedCap = 1024

// effectiveCap returns the concurrency cap to use for a fan-out of size n.
// If requested <= 0 the function picks defaultUnboundedCap (used by the
// legacy variants). The result is clamped to n because spawning more
// workers than work items just wastes a slot in the semaphore.
func effectiveCap(requested, n int) int {
	cap := requested
	if cap <= 0 {
		cap = defaultUnboundedCap
	}
	if cap > n {
		cap = n
	}
	return cap
}

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
// All applies an internal concurrency cap (defaultUnboundedCap) so that
// callers passing an attacker-controlled slice cannot trigger unbounded
// goroutine fan-out (X-02). Callers that need an explicit cap should use
// AllN.
//
// All always waits for every fn to finish so spawned goroutines don't leak.
func All[T any](fns ...func() T) ([]T, error) {
	return AllN(0, fns...)
}

// AllN is the bounded sibling of All. concurrency caps the number of fns
// that may run at the same time. A concurrency <= 0 means "use the
// internal default cap" (defaultUnboundedCap), matching All's behaviour.
//
// Semantics for values/err are identical to All: values is always
// len(fns) long and the lowest-index non-nil error (panic-converted) wins,
// matching the historical "submission order" tie-break.
func AllN[T any](concurrency int, fns ...func() T) ([]T, error) {
	if len(fns) == 0 {
		return []T{}, nil
	}

	values := make([]T, len(fns))
	errs := make([]error, len(fns))

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, effectiveCap(concurrency, len(fns)))

	for i, fn := range fns {
		i, fn := i, fn // capture for closure
		wg.Add(1)
		semaphore <- struct{}{} // acquire before spawning so the cap is enforced
		go func() {
			defer func() {
				<-semaphore
				wg.Done()
			}()
			r := Run(fn)
			v, err := r.Get()
			values[i] = v
			errs[i] = err
		}()
	}

	wg.Wait()

	// Pick the lowest-index error to match the legacy submission-order
	// semantics of All. Iterating the slice serially (after wg.Wait) avoids
	// any race on the read.
	var firstErr error
	for _, e := range errs {
		if e != nil {
			firstErr = e
			break
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
// AllWithError applies an internal concurrency cap (defaultUnboundedCap)
// so that callers passing an attacker-controlled slice cannot trigger
// unbounded goroutine fan-out (X-02). Callers that need an explicit cap
// should use AllWithErrorN.
//
// AllWithError always waits for every fn to finish so spawned goroutines
// do not leak. (X-04 follow-up: previously the Get error was discarded,
// so a panic in fn could be silently swallowed into a zero-value entry.)
func AllWithError[T any](fns ...func() (T, error)) ([]T, error) {
	return AllWithErrorN(0, fns...)
}

// AllWithErrorN is the bounded sibling of AllWithError. concurrency caps
// the number of fns that may run at the same time. A concurrency <= 0
// means "use the internal default cap" (defaultUnboundedCap), matching
// AllWithError's behaviour.
func AllWithErrorN[T any](concurrency int, fns ...func() (T, error)) ([]T, error) {
	if len(fns) == 0 {
		return []T{}, nil
	}

	type resultPair struct {
		value T
		err   error
	}

	values := make([]T, len(fns))
	panicErrs := make([]error, len(fns))
	normalErrs := make([]error, len(fns))

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, effectiveCap(concurrency, len(fns)))

	for i, fn := range fns {
		i, fn := i, fn // capture for closure
		wg.Add(1)
		semaphore <- struct{}{} // acquire before spawning so the cap is enforced
		go func() {
			defer func() {
				<-semaphore
				wg.Done()
			}()
			r := Run(func() resultPair {
				v, err := fn()
				return resultPair{value: v, err: err}
			})
			pair, err := r.Get()
			if err != nil {
				panicErrs[i] = err
				return
			}
			if pair.err != nil {
				normalErrs[i] = pair.err
				return
			}
			values[i] = pair.value
		}()
	}

	wg.Wait()

	// Submission-order tie-break: walk indices ascending and pick the
	// lowest index that produced an error. Panic-converted errors and
	// normal errors share priority by index, matching the legacy semantics
	// where the loop also processed indices in order.
	for i := range fns {
		if panicErrs[i] != nil {
			return nil, panicErrs[i]
		}
		if normalErrs[i] != nil {
			return nil, normalErrs[i]
		}
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
// Map applies an internal concurrency cap (defaultUnboundedCap) so that
// callers passing an attacker-controlled slice cannot trigger unbounded
// goroutine fan-out (X-02). Callers that need an explicit cap should use
// MapN.
//
// Map always waits for every fn to finish so spawned goroutines don't leak.
func Map[T, R any](items []T, fn func(T) R) ([]R, error) {
	return MapN(0, items, fn)
}

// MapN is the bounded sibling of Map. concurrency caps the number of fn
// invocations that may run at the same time. A concurrency <= 0 means
// "use the internal default cap" (defaultUnboundedCap), matching Map's
// behaviour.
//
// Semantics for values/err are identical to Map: values is always
// len(items) long and the lowest-index non-nil error (panic-converted)
// wins, matching the historical "submission order" tie-break.
func MapN[T, R any](concurrency int, items []T, fn func(T) R) ([]R, error) {
	if len(items) == 0 {
		return []R{}, nil
	}

	values := make([]R, len(items))
	errs := make([]error, len(items))

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, effectiveCap(concurrency, len(items)))

	for i, item := range items {
		i, item := i, item // capture for closure
		wg.Add(1)
		semaphore <- struct{}{} // acquire before spawning so the cap is enforced
		go func() {
			defer func() {
				<-semaphore
				wg.Done()
			}()
			r := Run(func() R {
				return fn(item)
			})
			v, err := r.Get()
			values[i] = v
			errs[i] = err
		}()
	}

	wg.Wait()

	var firstErr error
	for _, e := range errs {
		if e != nil {
			firstErr = e
			break
		}
	}
	return values, firstErr
}
