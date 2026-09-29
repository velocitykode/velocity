package async

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// PanicError is the typed recovered-panic error surfaced by GoWithRecoverE
// and the async helpers. It is a re-export of internal/panicerr.Error so
// adopters do not need to depend on the internal package.
type PanicError = panicerr.Error

// FromRecovered converts a recovered panic value into a *PanicError typed as
// `error`. Re-exported so adopters can match the framework's panic-to-error
// shape without importing the internal helper.
func FromRecovered(r any) error { return panicerr.FromRecovered(r) }

var (
	// logger is the package logger SetLogger installed; nil means the
	// fallback logger. A write reads it once (GetLogger), and SetLogger only
	// swaps the pointer.
	logger atomic.Pointer[contract.Logger]

	panicHook atomic.Pointer[func(context.Context, any) bool]
)

// SetLogger sets the package-level logger recovered panics and GoCtx
// cancellations are written to. The logger is process-wide: velocity.New
// sets it to the app logger, the newest live app's logger is the installed
// one, and an app's Shutdown hands it back to the previous live app's
// logger, or the default when none is left. Nil restores the default, the
// framework's standalone fallback logger, which writes warnings and errors
// to standard error. Safe for concurrent use, including from inside the
// package logger's own methods. A line that starts after SetLogger returns
// goes to l; a line already being written may still reach the logger it
// replaces, even after that logger is closed. The framework's built-in
// file logger sends such a late warning or error to the standalone
// fallback logger; a custom logger's behaviour after close is its own.
func SetLogger(l contract.Logger) {
	if l == nil {
		logger.Store(nil)
		return
	}
	logger.Store(&l)
}

// GetLogger returns the current package-level logger. Safe for concurrent
// reads.
//
// Callers can use the returned logger to emit messages tagged with the same
// sink the async package uses for panic logs.
func GetLogger() contract.Logger {
	if p := logger.Load(); p != nil {
		return *p
	}
	return fallbacklog.Logger{}
}

// SetPanicHook installs an interceptor invoked for every panic recovered
// by the async package's helpers (Run, RunWithTimeout, RunWithContext, Go,
// GoCtx, GoWithRecover, GoWithRecoverE, GoWithLogger, ForEach, GoForEach,
// TryForEach). Pass nil to clear. The hook is process-wide: velocity.New
// installs one reporting to the app's error handler, the newest live app's
// hook is the installed one, and an app's Shutdown hands it back to the
// previous live app's hook, or none. A panic in a goroutine an older app
// started is therefore reported to the newest app while that app lives.
//
// A hook that returns normally takes the panic over: the package does not
// also log it (velocity.New installs a hook that reports the panic to the
// app's error handler, whose log reporter writes the one entry). The hook itself is panic-safe: if it
// panics, that panic is swallowed and the package logs the recovered panic
// as it does with no hook. GoWithLogger logs to the logger it was given,
// and a GoWithRecover or GoWithRecoverE recover function runs, with a hook
// or without.
//
// The hook receives the context of the work that panicked: the ctx given to
// GoCtx or RunWithContext, and context.Background for the helpers that
// take none, so a report can carry the request, trace and span ids.
func SetPanicHook(hook func(ctx context.Context, p any)) {
	if hook == nil {
		panicHook.Store(nil)
		return
	}
	safe := func(ctx context.Context, p any) (took bool) {
		defer func() {
			if recover() != nil {
				took = false
			}
		}()
		hook(ctx, p)
		return true
	}
	panicHook.Store(&safe)
}

// runPanicHook runs the installed panic hook, if any, with ctx, the
// context of the work that panicked, and reports whether it took the panic
// over: it ran and returned normally.
func runPanicHook(ctx context.Context, p any) bool {
	if h := panicHook.Load(); h != nil && *h != nil {
		return (*h)(ctx, p)
	}
	return false
}

// logRecoveredPanic emits a structured Error log for a recovered panic.
// The "stack" field carries the calling goroutine's frames via debug.Stack().
// Because logRecoveredPanic is invoked from inside the deferred recover()
// frame of the panicking goroutine, debug.Stack() captures that goroutine's
// frames, i.e. the real site of the panic, not an unrelated supervisor.
//
// Extra key/value pairs (e.g. "name", "<callsite>") are appended after the
// canonical "panic" / "stack" fields. debug.Stack() is invoked exactly once
// per recovery so the formatted backtrace cost is paid only on the slow path.
// The line goes to l, or the package logger when l is nil (see logError).
func logRecoveredPanic(l contract.Logger, p any, kvs ...any) {
	attrs := make([]any, 0, 4+len(kvs))
	attrs = append(attrs, "panic", p, "stack", string(debug.Stack()))
	attrs = append(attrs, kvs...)
	logError(l, "async: panic recovered", attrs...)
}

// logError writes an Error line through l, or the package logger when l is
// nil. The package writes its lines inside a helper's deferred recover or
// on a goroutine with no recovery, so a logger that panics while writing
// is contained: the line goes to the framework's standalone fallback
// logger instead of crashing the process.
func logError(l contract.Logger, msg string, kvs ...any) {
	if l == nil {
		l = GetLogger()
	}
	fallbacklog.Write(l, func(w contract.Logger) { w.Error(msg, kvs...) })
}

// handlePanic handles panics in goroutines: the installed panic hook takes
// the panic over, given ctx, the context of the work that panicked, or the
// package logs it.
func handlePanic(ctx context.Context, p any) {
	if runPanicHook(ctx, p) {
		return
	}
	logRecoveredPanic(nil, p)
}

// Run executes function asynchronously
func Run[T any](fn func() T) *Result[T] {
	r := NewResult[T]()

	go func() {
		defer func() {
			if p := recover(); p != nil {
				handlePanic(context.Background(), p)
				r.fail(panicerr.FromRecovered(p))
			}
		}()
		r.complete(fn(), nil)
	}()

	return r
}

// RunWithTimeout executes with timeout. If fn panics before the timeout
// fires, the recovered panic is forwarded through panicCh so the result
// carries the panic error (not a misleading timeout error).
func RunWithTimeout[T any](timeout time.Duration, fn func() T) *Result[T] {
	r := NewResult[T]()

	go func() {
		defer func() {
			if p := recover(); p != nil {
				handlePanic(context.Background(), p)
				r.fail(panicerr.FromRecovered(p))
			}
		}()

		done := make(chan T, 1)
		// panicCh is cap=1 so the inner goroutine never blocks if the outer
		// already moved on to the timeout branch (drop-on-floor is fine: the
		// panic was already handled by handlePanic).
		panicCh := make(chan error, 1)
		go func() {
			defer func() {
				if p := recover(); p != nil {
					handlePanic(context.Background(), p)
					panicCh <- panicerr.FromRecovered(p)
				}
			}()
			done <- fn()
		}()

		// time.NewTimer + defer Stop instead of time.After: time.After's
		// underlying timer is not collected until it fires, leaking it for
		// the full timeout even when fn finishes first.
		t := time.NewTimer(timeout)
		defer t.Stop()

		select {
		case v := <-done:
			r.complete(v, nil)
		case err := <-panicCh:
			r.fail(err)
		case <-t.C:
			r.setTimedOut()
			r.fail(fmt.Errorf("operation timed out after %v", timeout))
		}
	}()

	return r
}

// RunWithContext executes with context for cancellation. If fn panics
// before ctx is canceled, the recovered panic is forwarded through panicCh
// so the result carries the panic error instead of hanging forever waiting
// on a `done` send that will never happen.
func RunWithContext[T any](ctx context.Context, fn func() T) *Result[T] {
	r := NewResult[T]()

	go func() {
		defer func() {
			if p := recover(); p != nil {
				handlePanic(ctx, p)
				r.fail(panicerr.FromRecovered(p))
			}
		}()

		done := make(chan T, 1)
		// panicCh is cap=1 so the inner goroutine never blocks if the outer
		// already moved on to the ctx-cancel branch.
		panicCh := make(chan error, 1)
		go func() {
			defer func() {
				if p := recover(); p != nil {
					handlePanic(ctx, p)
					panicCh <- panicerr.FromRecovered(p)
				}
			}()
			done <- fn()
		}()

		select {
		case v := <-done:
			r.complete(v, nil)
		case err := <-panicCh:
			r.fail(err)
		case <-ctx.Done():
			r.fail(ctx.Err())
		}
	}()

	return r
}

// Go executes function without waiting
func Go(fn func()) {
	go func() {
		defer func() {
			if p := recover(); p != nil {
				handlePanic(context.Background(), p)
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
				handlePanic(ctx, p)
			}
		}()
		done := make(chan struct{})
		go func() {
			defer func() {
				if p := recover(); p != nil {
					handlePanic(ctx, p)
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
				logError(nil, "async: GoCtx context done", "error", err)
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
								handlePanic(context.Background(), p2)
							}
						}()
						recoverFn(p)
					}()
					runPanicHook(context.Background(), p)
				} else {
					handlePanic(context.Background(), p)
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
								handlePanic(context.Background(), p2)
							}
						}()
						recoverFn(panicerr.New(p))
					}()
					runPanicHook(context.Background(), p)
				} else {
					handlePanic(context.Background(), p)
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
func GoWithLogger(l contract.Logger, name string, fn func()) {
	go func() {
		defer func() {
			if p := recover(); p != nil {
				logRecoveredPanic(l, p, "name", name)
				runPanicHook(context.Background(), p)
			}
		}()
		fn()
	}()
}
