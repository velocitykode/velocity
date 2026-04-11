package async

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Logger is the logging interface for the async package.
type Logger interface {
	Error(msg string, kvs ...any)
}

var (
	loggerMu sync.RWMutex
	logger   Logger = &stdLogger{}
)

type stdLogger struct{}

func (stdLogger) Error(msg string, kvs ...any) { log.Print("[ERROR] " + msg + fmtKVs(kvs)) }

func fmtKVs(kvs []any) string {
	if len(kvs) == 0 {
		return ""
	}
	s := ""
	for i := 0; i+1 < len(kvs); i += 2 {
		s += fmt.Sprintf(" %v=%v", kvs[i], kvs[i+1])
	}
	return s
}

// SetLogger sets the package-level logger for panic recovery.
func SetLogger(l Logger) {
	loggerMu.Lock()
	defer loggerMu.Unlock()
	logger = l
}

func getLogger() Logger {
	loggerMu.RLock()
	defer loggerMu.RUnlock()
	return logger
}

// handlePanic handles panics in goroutines
func handlePanic(p any) {
	getLogger().Error("async: panic recovered", "panic", p)
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
