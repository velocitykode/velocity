package async

import (
	"context"
	"fmt"
	"time"
)

// panicHandler is a global panic handler
var panicHandler func(any)

// SetPanicHandler sets a custom panic handler
func SetPanicHandler(handler func(any)) {
	panicHandler = handler
}

// handlePanic handles panics in goroutines
func handlePanic(p any) {
	if panicHandler != nil {
		panicHandler(p)
	} else {
		fmt.Printf("async: panic recovered: %v\n", p)
	}
}

// Run executes function asynchronously
func Run[T any](fn func() T) *Result[T] {
	r := NewResult[T]()

	go func() {
		defer func() {
			if p := recover(); p != nil {
				handlePanic(p)
				r.errorCh <- fmt.Errorf("panic: %v", p)
			}
		}()
		r.valueCh <- fn()
	}()

	return r
}

// RunWithTimeout executes with timeout
func RunWithTimeout[T any](timeout time.Duration, fn func() T) *Result[T] {
	r := NewResult[T]()

	go func() {
		defer func() {
			if p := recover(); p != nil {
				handlePanic(p)
				r.errorCh <- fmt.Errorf("panic: %v", p)
			}
		}()

		done := make(chan T, 1)
		go func() {
			defer func() {
				if p := recover(); p != nil {
					handlePanic(p)
				}
			}()
			done <- fn()
		}()

		select {
		case v := <-done:
			r.valueCh <- v
		case <-time.After(timeout):
			r.setTimedOut()
			r.errorCh <- fmt.Errorf("operation timed out after %v", timeout)
		}
	}()

	return r
}

// RunWithContext executes with context for cancellation
func RunWithContext[T any](ctx context.Context, fn func() T) *Result[T] {
	r := NewResult[T]()

	go func() {
		defer func() {
			if p := recover(); p != nil {
				handlePanic(p)
				r.errorCh <- fmt.Errorf("panic: %v", p)
			}
		}()

		done := make(chan T, 1)
		go func() {
			defer func() {
				if p := recover(); p != nil {
					handlePanic(p)
				}
			}()
			done <- fn()
		}()

		select {
		case v := <-done:
			r.valueCh <- v
		case <-ctx.Done():
			r.errorCh <- ctx.Err()
		}
	}()

	return r
}

// Go executes function without waiting
func Go(fn func()) {
	go func() {
		defer func() {
			if p := recover(); p != nil {
				handlePanic(p)
			}
		}()
		fn()
	}()
}

// GoWithRecover executes with custom panic handler
func GoWithRecover(fn func(), recoverFn func(any)) {
	go func() {
		defer func() {
			if p := recover(); p != nil {
				recoverFn(p)
			}
		}()
		fn()
	}()
}
