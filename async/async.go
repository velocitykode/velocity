package async

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/internal/panicerr"
)

// Logger is the logging interface for the async package.
type Logger interface {
	Error(msg string, kvs ...any)
}

// PanicError is the typed recovered-panic error surfaced by GoWithRecoverE
// and the async helpers. It is a re-export of internal/panicerr.Error so
// adopters do not need to depend on the internal package.
type PanicError = panicerr.Error

// FromRecovered converts a recovered panic value into a *PanicError typed as
// `error`. Re-exported so adopters can match the framework's panic-to-error
// shape without importing the internal helper.
func FromRecovered(r any) error { return panicerr.FromRecovered(r) }

var (
	loggerMu sync.RWMutex
	logger   Logger = &stdLogger{}

	panicHook atomic.Pointer[func(any)]
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

// GetLogger returns the current package-level logger. Safe for concurrent
// reads. Named GetLogger (not Logger) because the package already exports a
// Logger interface type, and Go disallows a function and type sharing a name.
//
// Callers can use the returned logger to emit messages tagged with the same
// sink the async package uses for panic logs.
func GetLogger() Logger {
	return getLogger()
}

func getLogger() Logger {
	loggerMu.RLock()
	defer loggerMu.RUnlock()
	return logger
}

// SetPanicHook installs a non-logging interceptor invoked for every panic
// recovered by the async package's helpers (Run, RunWithTimeout,
// RunWithContext, Go, GoCtx, GoWithRecover, GoWithRecoverE, GoWithLogger,
// ForEach, GoForEach, TryForEach). Pass nil to clear. The hook runs in
// addition to logging, not in place of it. The hook itself is panic-safe:
// if it panics, the panic is swallowed.
func SetPanicHook(hook func(any)) {
	if hook == nil {
		panicHook.Store(nil)
		return
	}
	safe := func(p any) {
		defer func() { _ = recover() }()
		hook(p)
	}
	panicHook.Store(&safe)
}

func runPanicHook(p any) {
	if h := panicHook.Load(); h != nil && *h != nil {
		(*h)(p)
	}
}

// handlePanic handles panics in goroutines
func handlePanic(p any) {
	getLogger().Error("async: panic recovered", "panic", p)
	runPanicHook(p)
}

// Run executes function asynchronously
func Run[T any](fn func() T) *Result[T] {
	r := NewResult[T]()

	go func() {
		defer func() {
			if p := recover(); p != nil {
				handlePanic(p)
				r.errorCh <- panicerr.FromRecovered(p)
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
				r.errorCh <- panicerr.FromRecovered(p)
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
				r.errorCh <- panicerr.FromRecovered(p)
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

// GoCtx runs fn in a panic-recovered goroutine bound to ctx. The supervisor
// returns when ctx is canceled or fn returns, whichever comes first. The
// helper logs `ctx.Err()` on cancellation via the package logger so adopters
// can trace early termination.
//
// fn receives ctx so it can wire its own select on `ctx.Done()` if it needs
// to interrupt mid-flight; without that, fn runs to completion even after
// cancellation (Go offers no goroutine preemption).
func GoCtx(ctx context.Context, fn func(ctx context.Context)) {
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		defer func() {
			if p := recover(); p != nil {
				handlePanic(p)
			}
		}()
		done := make(chan struct{})
		go func() {
			defer func() {
				if p := recover(); p != nil {
					handlePanic(p)
				}
				close(done)
			}()
			fn(ctx)
		}()
		select {
		case <-done:
			// fn returned on its own; no log.
		case <-ctx.Done():
			// fn may still be running. We log and return; the responsibility
			// for fn returning rests with fn (it should respect ctx).
			if err := ctx.Err(); err != nil {
				getLogger().Error("async: GoCtx context done", "error", err)
			}
		}
	}()
}

// GoWithRecover executes fn in a goroutine and routes any panic to recoverFn.
// If recoverFn is nil, panics fall back to the package-level handler (same
// path Go uses), so callers can supply nil to opt out of custom handling.
// A panic raised inside recoverFn itself is also recovered and logged.
//
// SetPanicHook observers see every panic regardless of whether recoverFn is
// supplied, so metrics/telemetry sinks don't go dark when a caller installs
// custom handling.
func GoWithRecover(fn func(), recoverFn func(any)) {
	go func() {
		defer func() {
			if p := recover(); p != nil {
				if recoverFn != nil {
					func() {
						defer func() {
							if p2 := recover(); p2 != nil {
								handlePanic(p2)
							}
						}()
						recoverFn(p)
					}()
					runPanicHook(p)
				} else {
					handlePanic(p)
				}
			}
		}()
		fn()
	}()
}

// GoWithRecoverE is the typed sibling of GoWithRecover. recoverFn receives a
// *PanicError so callers don't have to type-assert `any`. If recoverFn is
// nil, panics fall back to the package-level handler.
func GoWithRecoverE(fn func(), recoverFn func(*PanicError)) {
	go func() {
		defer func() {
			if p := recover(); p != nil {
				if recoverFn != nil {
					func() {
						defer func() {
							if p2 := recover(); p2 != nil {
								handlePanic(p2)
							}
						}()
						recoverFn(panicerr.New(p))
					}()
					runPanicHook(p)
				} else {
					handlePanic(p)
				}
			}
		}()
		fn()
	}()
}

// GoWithLogger runs fn in a panic-recovered goroutine and routes panics to
// the supplied logger with structured fields (`name`, `panic`). If l is nil,
// the package logger is used. Convenient for adopters that already carry a
// scoped logger and want panics tagged with a callsite name.
func GoWithLogger(l Logger, name string, fn func()) {
	go func() {
		defer func() {
			if p := recover(); p != nil {
				logTo := l
				if logTo == nil {
					logTo = getLogger()
				}
				logTo.Error("async: panic recovered", "name", name, "panic", p)
				runPanicHook(p)
			}
		}()
		fn()
	}()
}
