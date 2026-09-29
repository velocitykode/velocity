// Package eventemit is how a framework component hands its events to the
// event dispatcher it was given, and the one thing that happens when a
// dispatch fails.
//
// A component holds an Emitter. The framework installs the app's dispatcher
// on it through the component's SetEventDispatcher seam (Emitter.Set), the
// component builds an event only when Installed reports a dispatcher, and it
// hands the event over with Emit. When the dispatch fails (a listener
// returned an error or panicked, or the event was dropped before any
// listener saw it), the failure goes to one policy, Failures.Record: the
// failure is counted, the first failure of each event name is logged at warn
// level through the component's logger, and the failure hook, when one is
// set, is called. No component discards a dispatch's result and no component
// keeps a policy of its own.
//
// In an app the policy state is the app's: the dispatch function the
// framework hands every component records each failure it returns in the
// app's Failures (Failures.Recording) and marks the returned error as
// recorded, so the component's Emitter does not record it a second time. A
// component that drops events without calling the dispatcher (the router's
// async buffer, the ORM statement-event queue) records those drops in the
// Failures the app shares with it (Emitter.Share). A component used on its
// own records into Failures of its own.
//
// # Concurrency
//
// Every field is safe for concurrent use: the dispatcher, the shared
// Failures and the logger source are atomic pointers, so Set, Share and
// UseLogger may race with Emit on any goroutine, and Emit never takes a lock
// (queue drivers dispatch while holding their own mutex). A dispatch reads
// the dispatcher once, so it completes against the dispatcher it read even
// when Set replaces it meanwhile. Failures keeps its count and hook in
// atomics and its set of already-logged event names under a mutex that is
// held only to test and insert a name, never while logging or calling the
// hook.
package eventemit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
)

// FailureMessage is the warn line Failures.Record writes for the first
// failure of each event name. The line carries the event name under "event"
// and the failure under "error".
const FailureMessage = "event dispatch failed; later failures of this event are counted, not logged"

// maxLoggedNames bounds the set of event names Failures remembers having
// logged. Framework event names are a fixed set, so the bound is never
// reached in practice; past it a new name's failures are still counted and
// handed to the hook, only not logged.
const maxLoggedNames = 1024

// dispatchFunc is the dispatcher an Emitter holds.
type dispatchFunc func(ctx context.Context, event any) error

// loggerSource returns the logger a component writes through now.
type loggerSource func() contract.Logger

// HookPanicMessage is the error line Failures.Record writes the first time
// the hook panics. The line carries the event name under "event" and the
// panic value under "panic".
const HookPanicMessage = "event failure hook panicked; the panic is counted as a failure"

// Hook is called with every failure a Failures records: the error and the
// event as it was dispatched, whatever its type. It runs on the goroutine
// that saw the failure (a request, a job, a background pump), so it must be
// quick. A panic in it is recovered and counted (see Failures.Record).
type Hook func(err error, event any)

// Failures is the failure policy's state: the count of failed dispatches,
// the event names whose first failure was already logged, and the hook. The
// zero value is ready to use and has no hook.
type Failures struct {
	count           atomic.Uint64
	hook            atomic.Pointer[Hook]
	hookPanicLogged atomic.Bool

	mu     sync.Mutex
	logged map[string]struct{}
}

// Record applies the policy to one failed dispatch of event: it counts it,
// logs it at warn level through logger (the framework's standalone fallback
// logger when logger is nil) when it is the first failure of the event's
// name, and calls the hook. A panic in the hook is recovered and counted as
// one more failure, and its first occurrence is logged at error level; it
// never re-enters the policy, so the hook is not called for it. ctx is the
// dispatch's context.
func (f *Failures) Record(ctx context.Context, logger contract.Logger, err error, event any) {
	f.count.Add(1)
	name := EventName(event)
	if f.firstOf(name) {
		fallbacklog.Resolve(logger).Warn(FailureMessage, "event", name, "error", err)
	}
	f.callHook(logger, err, event, name)
}

// callHook calls the hook, when one is set, with err and event, recovering
// and counting a panic in it.
func (f *Failures) callHook(logger contract.Logger, err error, event any, name string) {
	h := f.hook.Load()
	if h == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			f.count.Add(1)
			if f.hookPanicLogged.CompareAndSwap(false, true) {
				fallbacklog.Resolve(logger).Error(HookPanicMessage, "event", name, "panic", fmt.Sprint(p))
			}
		}
	}()
	(*h)(err, event)
}

// firstOf reports whether name has not been logged before, and remembers it.
func (f *Failures) firstOf(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, seen := f.logged[name]; seen {
		return false
	}
	if len(f.logged) >= maxLoggedNames {
		return false
	}
	if f.logged == nil {
		f.logged = make(map[string]struct{})
	}
	f.logged[name] = struct{}{}
	return true
}

// Count returns how many failed dispatches f has recorded.
func (f *Failures) Count() uint64 {
	return f.count.Load()
}

// SetHook installs the hook every later failure is handed to; nil removes
// it.
func (f *Failures) SetHook(h Hook) {
	if h == nil {
		f.hook.Store(nil)
		return
	}
	f.hook.Store(&h)
}

// Recording returns a dispatch function that calls dispatch and records each
// failure it returns in f, logging through logger, then returns the failure
// marked as recorded (see Recorded), so an Emitter holding the returned
// function does not record it again. The framework builds the dispatch
// function it hands every component this way. It returns nil when dispatch
// is nil.
func (f *Failures) Recording(dispatch func(ctx context.Context, event any) error, logger contract.Logger) func(ctx context.Context, event any) error {
	if dispatch == nil {
		return nil
	}
	return func(ctx context.Context, event any) error {
		if ctx == nil {
			ctx = context.Background()
		}
		err := dispatch(ctx, event)
		if err == nil {
			return nil
		}
		f.Record(ctx, logger, err, event)
		return &recordedError{err: err}
	}
}

// recordedError marks a dispatch failure a Failures already recorded.
type recordedError struct{ err error }

func (e *recordedError) Error() string { return e.err.Error() }
func (e *recordedError) Unwrap() error { return e.err }

// Recorded reports whether err, or an error it wraps, is a failure a
// Failures already recorded (a dispatch function built by
// Failures.Recording returned it).
func Recorded(err error) bool {
	var r *recordedError
	return errors.As(err, &r)
}

// EventName returns the name failures of event are logged under: the
// event's Name when it implements contract.Event, its Go type otherwise.
func EventName(event any) string {
	if e, ok := event.(contract.Event); ok {
		if name := e.Name(); name != "" {
			return name
		}
	}
	return fmt.Sprintf("%T", event)
}

// Emitter holds one component's event dispatcher and applies the failure
// policy to what a dispatch returns. The zero value has no dispatcher, logs
// through the framework's standalone fallback logger and records into
// Failures of its own.
type Emitter struct {
	dispatch atomic.Pointer[dispatchFunc]
	shared   atomic.Pointer[Failures]
	logger   atomic.Pointer[loggerSource]
	own      Failures
}

// Set installs fn as the dispatcher; nil removes it.
func (e *Emitter) Set(fn func(ctx context.Context, event any) error) {
	if fn == nil {
		e.dispatch.Store(nil)
		return
	}
	d := dispatchFunc(fn)
	e.dispatch.Store(&d)
}

// Dispatcher returns the installed dispatcher, or nil.
func (e *Emitter) Dispatcher() func(ctx context.Context, event any) error {
	if p := e.dispatch.Load(); p != nil {
		return *p
	}
	return nil
}

// Installed reports whether a dispatcher is installed: one atomic load, so a
// component can check it before building an event nobody would receive.
func (e *Emitter) Installed() bool {
	return e.dispatch.Load() != nil
}

// Emit hands event to the installed dispatcher under ctx (context.Background
// when nil) and applies the failure policy to a failed dispatch (see Fail).
// It reports whether a dispatcher was installed. A panic in the dispatcher
// is not recovered here: a component whose caller must survive one recovers
// it and hands it to Fail.
func (e *Emitter) Emit(ctx context.Context, event any) bool {
	p := e.dispatch.Load()
	if p == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := (*p)(ctx, event); err != nil {
		e.Fail(ctx, err, event)
	}
	return true
}

// Fail applies the failure policy to err, a failed dispatch of event: unless
// the dispatch function already recorded it (Recorded), it is recorded in
// the Failures the emitter shares (see Share), or its own, logging through
// the component's logger (see UseLogger). A nil err is ignored.
func (e *Emitter) Fail(ctx context.Context, err error, event any) {
	if err == nil || Recorded(err) {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.failures().Record(ctx, e.log(), err, event)
}

// Share makes the emitter record its failures in f, the app's Failures;
// nil returns it to its own.
func (e *Emitter) Share(f *Failures) {
	e.shared.Store(f)
}

// FailureCount returns the count of the Failures the emitter records into.
func (e *Emitter) FailureCount() uint64 {
	return e.failures().Count()
}

// failures returns the Failures the emitter records into.
func (e *Emitter) failures() *Failures {
	if f := e.shared.Load(); f != nil {
		return f
	}
	return &e.own
}

// UseLogger makes the emitter log through the logger source returns at the
// time of a failure: the component's logger. A nil source, or a source that
// returns nil, means the framework's standalone fallback logger.
func (e *Emitter) UseLogger(source func() contract.Logger) {
	if source == nil {
		e.logger.Store(nil)
		return
	}
	s := loggerSource(source)
	e.logger.Store(&s)
}

// log returns the component's logger now, or nil.
func (e *Emitter) log() contract.Logger {
	if p := e.logger.Load(); p != nil {
		return (*p)()
	}
	return nil
}
