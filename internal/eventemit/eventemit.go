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
// Failures the app shares with it (Emitter.Share); one whose drops happen
// on a path that must not block (the ORM's, inside a database driver
// callback) counts them there and applies the rest of the policy later
// (Emitter.FailLater). A component used on its own records into Failures
// of its own.
//
// # Concurrency
//
// Every field is safe for concurrent use: the dispatcher, the shared
// Failures and the logger source are atomic pointers, so Set, Share and
// UseLogger may race with Emit on any goroutine, and Emit never takes a lock
// (queue drivers dispatch while holding their own mutex). A dispatch reads
// the dispatcher once, so it completes against the dispatcher it read even
// when Set replaces it meanwhile. Failures keeps its count and hook in
// atomics, and its set of already-logged event names and its set of
// goroutines running the hook under a mutex that is held only to test,
// insert or remove an entry, never while logging or calling the hook.
package eventemit

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/trace"
)

// FailureMessage is the warn line Failures.Record writes for the first
// failure of each event name. The line carries the event name under "event"
// and the failure under "error", after the request, trace and span ids the
// dispatch's context carries (request_id, trace_id, span_id).
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
// panic value under "panic", after the ids the dispatch's context carries.
const HookPanicMessage = "event failure hook panicked; the panic is counted as a failure"

// Hook is called with every failure a Failures records: the error and the
// event as it was dispatched, whatever its type. It runs on the goroutine
// that saw the failure (a request, a job, a background pump), so it must be
// quick. A panic in it is recovered and counted (see Failures.Record). A
// failure the hook causes on its own goroutine while it runs (it reads a
// cache whose listener fails, say) is counted and logged but not handed to
// the hook again, so a hook cannot recurse through the failures it causes.
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
	// hooking holds the goroutines running the hook now (GoroutineID).
	hooking map[uint64]struct{}
}

// Record applies the policy to one failed delivery of event, however many
// of its listeners failed (a caller records the delivery once, with the
// listeners' failures joined): it counts it,
// logs it at warn level through logger (the framework's standalone fallback
// logger when logger is nil) when it is the first failure of the event's
// name, and calls the hook. A panic in the hook is recovered and counted as
// one more failure, and its first occurrence is logged at error level; it
// never re-enters the policy, so the hook is not called for it. ctx is the
// dispatch's context: both lines are bound to the request, trace and span
// ids it carries, read only when a line is written. A logger that panics
// while writing either line is contained: the line goes to the fallback
// logger instead, and the count and the hook are unaffected.
//
// A failure recorded on a goroutine that is running the hook, one the hook
// caused, is counted and logged but not handed to the hook: the hook is
// never re-entered on its own goroutine. Failures on other goroutines are
// handed to it as usual, even while it runs.
func (f *Failures) Record(ctx context.Context, logger contract.Logger, err error, event any) {
	f.count.Add(1)
	f.report(ctx, logger, err, event, false)
}

// report applies the policy's line and hook to a failure already counted:
// the first-failure line, then the hook unless skipHook.
func (f *Failures) report(ctx context.Context, logger contract.Logger, err error, event any, skipHook bool) {
	name := EventName(event)
	if f.firstOf(name) {
		WriteLine(ctx, logger, func(l contract.Logger) {
			l.Warn(FailureMessage, "event", name, "error", err)
		})
	}
	if !skipHook {
		f.callHook(ctx, logger, err, event, name)
	}
}

// WriteLine writes one line with write, through logger (the fallback when
// nil) bound to the ids ctx carries. A logger that panics, in With or in
// the write, is contained and the same line goes to the fallback logger:
// a diagnostic written this way never fails the work it describes, and a
// panicking logger does not hide the line. The failure policy writes its
// lines through it, so a panicking logger never skips the accounting or
// the hook and is not counted as a failure of its own.
//
// The containment is fallbacklog.Write's; WriteLine adds the ctx binding,
// on the fallback line too. A line's values (an error whose Error method
// panics, say) can panic the fallback as well, which is contained there.
func WriteLine(ctx context.Context, logger contract.Logger, write func(contract.Logger)) {
	fallbacklog.Write(logger, func(l contract.Logger) { write(boundTo(ctx, l)) })
}

// boundTo returns logger (the fallback when nil) bound to the ids ctx
// carries.
func boundTo(ctx context.Context, logger contract.Logger) contract.Logger {
	l := fallbacklog.Resolve(logger)
	if fields := trace.LogFields(ctx); len(fields) > 0 {
		return l.With(fields...)
	}
	return l
}

// callHook calls the hook, when one is set, with err and event, recovering
// and counting a panic in it. It does not call the hook on a goroutine that
// is running it already (see Record).
func (f *Failures) callHook(ctx context.Context, logger contract.Logger, err error, event any, name string) {
	h := f.hook.Load()
	if h == nil {
		return
	}
	gid := GoroutineID()
	if !f.enterHook(gid) {
		return
	}
	defer f.leaveHook(gid)
	defer func() {
		if p := recover(); p != nil {
			f.count.Add(1)
			if f.hookPanicLogged.CompareAndSwap(false, true) {
				WriteLine(ctx, logger, func(l contract.Logger) {
					l.Error(HookPanicMessage, "event", name, "panic", fmt.Sprint(p))
				})
			}
		}
	}()
	(*h)(err, event)
}

// enterHook marks goroutine gid as running the hook. It reports false,
// marking nothing, when gid is running it already.
func (f *Failures) enterHook(gid uint64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, running := f.hooking[gid]; running {
		return false
	}
	if f.hooking == nil {
		f.hooking = make(map[uint64]struct{})
	}
	f.hooking[gid] = struct{}{}
	return true
}

// leaveHook unmarks goroutine gid.
func (f *Failures) leaveHook(gid uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.hooking, gid)
}

// hookRunningHere reports whether the calling goroutine is running the
// hook.
func (f *Failures) hookRunningHere() bool {
	if f.hook.Load() == nil {
		return false
	}
	gid := GoroutineID()
	f.mu.Lock()
	defer f.mu.Unlock()
	_, running := f.hooking[gid]
	return running
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
// function does not record it again. A panic in dispatch is recovered here
// and is such a failure: recorded once, as the typed panic error
// (panicerr.FromRecovered), and returned marked, so the component that
// dispatched survives it. The framework builds the dispatch function it
// hands every component this way. It returns nil when dispatch is nil.
func (f *Failures) Recording(dispatch func(ctx context.Context, event any) error, logger contract.Logger) func(ctx context.Context, event any) error {
	if dispatch == nil {
		return nil
	}
	return func(ctx context.Context, event any) error {
		if ctx == nil {
			ctx = context.Background()
		}
		err := dispatchRecovering(dispatch, ctx, event)
		if err == nil {
			return nil
		}
		f.Record(ctx, logger, err, event)
		return &recordedError{err: err}
	}
}

// dispatchRecovering calls dispatch, returning a panic in it as the typed
// panic error.
func dispatchRecovering(dispatch func(ctx context.Context, event any) error, ctx context.Context, event any) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = panicerr.FromRecovered(p)
		}
	}()
	return dispatch(ctx, event)
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
// is not recovered here (in an app, the dispatch function Recording built
// recovers and records it first): a component whose caller must survive one
// from any other dispatcher recovers it and hands it to Fail.
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

// FailLater applies the failure policy to err, a failed dispatch of event,
// in two steps, for a path that must not block (a database driver
// callback, which holds its connection): the failure is counted now, and
// the returned function applies the rest (the first-failure line, through
// the component's logger read when it runs, and the hook) when the caller
// runs it, away from that path. The caller runs it at most once. A failure
// counted on a goroutine running the hook is not handed to the hook when
// the function runs (see Failures.Record). FailLater returns nil, counting
// nothing, for a nil err or one the dispatch function already recorded.
func (e *Emitter) FailLater(ctx context.Context, err error, event any) func() {
	if err == nil || Recorded(err) {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	f := e.failures()
	f.count.Add(1)
	nested := f.hookRunningHere()
	return func() { f.report(ctx, e.log(), err, event, nested) }
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

// gidParseFallback feeds GoroutineID's failure path with unique sentinels.
// Sentinels live above 1<<63 so they can never collide with a real goroutine
// ID within the lifetime of a process.
var gidParseFallback atomic.Uint64

// GoroutineID returns the running goroutine's ID by parsing the first line
// of runtime.Stack ("goroutine N [...]"). Used only on failure paths, which
// are rare by construction; the cost is acceptable there and the
// per-goroutine re-entry guards it enables (the failure hook's, the event
// dispatcher's failure-report bridge's) cannot be built from a context,
// which a re-entrant call need not carry.
//
// The header format is not a formally stable runtime API (though it has been
// stable in practice for many releases and is relied on by widely used
// libraries), so the failure mode is chosen deliberately: if parsing ever
// fails, the function returns a process-unique sentinel instead of a shared
// zero value. A shared zero would make every unparsed goroutine look like
// the same goroutine and falsely suppress unrelated work whenever a guard is
// held; a unique sentinel merely degrades the guard to a no-op for that one
// call, which errs on the side of reporting rather than suppressing.
func GoroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	const prefix = "goroutine "
	s := buf[:n]
	if len(s) <= len(prefix) {
		return 1<<63 | gidParseFallback.Add(1)
	}
	var id uint64
	digits := 0
	for _, c := range s[len(prefix):] {
		if c < '0' || c > '9' {
			break
		}
		id = id*10 + uint64(c-'0')
		digits++
	}
	if digits == 0 {
		return 1<<63 | gidParseFallback.Add(1)
	}
	return id
}
