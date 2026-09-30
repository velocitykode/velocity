// Package eventemit is how a framework component hands its events to the
// event dispatcher it was given, and the one thing that happens when a
// dispatch fails.
//
// A component holds an Emitter. The framework installs the app's dispatcher
// on it through the component's SetEventDispatcher seam (Emitter.Set), the
// component hands each event over with EmitBuilt, whose builder runs only
// when a dispatcher is installed, so no event is built that nobody would
// receive. When the dispatch fails (a listener
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
// Failures and the logger source are one binding behind one atomic
// pointer, so Set, Share, UseLogger and SetShared may race with Emit on any
// goroutine, and Emit never takes a lock (queue drivers dispatch while
// holding their own mutex). A dispatch reads the binding once, so it
// completes against the dispatcher it read, and records its failure in
// the Failures bound with it, even when the binding is replaced meanwhile.
// SetShared replaces the three together: the handover of a process-wide
// emitter (the queue's batch events) from one app to the next. Failures keeps its count and hook in
// atomics, and its set of already-logged event names and its set of
// goroutines running the hook under a mutex that is held only to test,
// insert or remove an entry, never while logging or calling the hook.
package eventemit

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/goroutine"
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
	// hooking holds the goroutines running the hook now.
	hooking goroutine.Set
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
	gid := goroutine.ID()
	if f.hooking.Contains(gid) {
		return
	}
	f.hooking.Enter(gid)
	defer f.hooking.Leave(gid)
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

// hookRunningHere reports whether the calling goroutine is running the
// hook.
func (f *Failures) hookRunningHere() bool {
	if f.hook.Load() == nil {
		return false
	}
	return f.hooking.Contains(goroutine.ID())
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
		err := DispatchContained(ctx, dispatch, event)
		if err == nil {
			return nil
		}
		f.Record(ctx, logger, err, event)
		return &recordedError{err: err}
	}
}

// DispatchContained calls dispatch, returning a panic in it as the typed
// panic error (a contract.RecoveredPanic). It is the one recover for a
// dispatch target: Emit and Recording call it, and so does any framework
// goroutine that calls a stored dispatch target directly, since a recover
// only catches panics on its own goroutine. It does not record the
// failure; the caller routes the error to its failure policy.
func DispatchContained(ctx context.Context, dispatch func(ctx context.Context, event any) error, event any) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = panicerr.FromRecovered(p)
		}
	}()
	return dispatch(ctx, event)
}

// recordedError marks a dispatch failure a Failures already recorded.
type recordedError struct{ err error }

func (e *recordedError) Error() string { return errchain.Text(e.err) }
func (e *recordedError) Unwrap() error { return e.err }

// Recorded reports whether err, or an error it wraps, is a failure a
// Failures already recorded (a dispatch function built by
// Failures.Recording returned it). The walk calls the Unwrap and As
// methods of the errors err wraps, which may be user code, through
// errchain.As: bounded and contained, so a panic in one, or a chain that
// loops, counts as not recorded, and the failure is recorded rather than
// lost.
func Recorded(err error) bool {
	_, recorded := errchain.As[*recordedError](err)
	return recorded
}

// EventName returns the name failures of event are logged under: the
// event's Name when it implements contract.Event, its Go type otherwise.
// Name is user code: a panic in it is contained and the Go type is used,
// so a failure is never left uncounted, unlogged or unhooked by the name
// it is logged under.
func EventName(event any) string {
	if name := contractName(event); name != "" {
		return name
	}
	return fmt.Sprintf("%T", event)
}

// contractName returns the event's Name when it implements
// contract.Event, or "" when it does not or its Name panics.
func contractName(event any) (name string) {
	e, ok := event.(contract.Event)
	if !ok {
		return ""
	}
	defer func() {
		if recover() != nil {
			name = ""
		}
	}()
	return e.Name()
}

// Emitter holds one component's event dispatcher and applies the failure
// policy to what a dispatch returns. The zero value has no dispatcher, logs
// through the framework's standalone fallback logger and records into
// Failures of its own.
//
// The dispatcher, the shared Failures and the logger source are one
// binding, read with one atomic load: a dispatch and the failure it causes
// go to the binding in place when the dispatch started, and SetShared
// replaces all three at once, so a failure is never counted in one owner's
// Failures and logged through another's logger.
type Emitter struct {
	binding atomic.Pointer[binding]
	own     Failures
}

// binding is what an Emitter holds: never mutated after it is stored.
type binding struct {
	dispatch dispatchFunc
	shared   *Failures
	logger   loggerSource
}

// update replaces the binding with change applied to a copy of the
// current one, retrying when another update raced it.
func (e *Emitter) update(change func(b *binding)) {
	for {
		old := e.binding.Load()
		next := &binding{}
		if old != nil {
			*next = *old
		}
		change(next)
		if e.binding.CompareAndSwap(old, next) {
			return
		}
	}
}

// Set installs fn as the dispatcher; nil removes it.
func (e *Emitter) Set(fn func(ctx context.Context, event any) error) {
	e.update(func(b *binding) { b.dispatch = fn })
}

// SetShared installs fn as the dispatcher, f as the Failures the emitter
// records into (nil for its own) and logger as the logger it writes
// through (nil for the fallback logger), in one step: the handover of a
// process-wide emitter from one app to another, where no failure may land
// in one app's Failures between the two.
func (e *Emitter) SetShared(fn func(ctx context.Context, event any) error, f *Failures, logger func() contract.Logger) {
	e.binding.Store(&binding{dispatch: fn, shared: f, logger: logger})
}

// Dispatcher returns the installed dispatcher, or nil.
func (e *Emitter) Dispatcher() func(ctx context.Context, event any) error {
	if b := e.binding.Load(); b != nil && b.dispatch != nil {
		return b.dispatch
	}
	return nil
}

// Installed reports whether a dispatcher is installed: one atomic load, so a
// component can check it before building an event nobody would receive.
func (e *Emitter) Installed() bool {
	b := e.binding.Load()
	return b != nil && b.dispatch != nil
}

// EmitBuilt calls build and hands the event it returns to the installed
// dispatcher under ctx (context.Background when nil), only when a
// dispatcher is installed: with none, build is never called, so an event
// nobody would receive is never built. It is the only way to hand an event
// to an Emitter. It reports whether a dispatcher was installed. A nil
// Emitter has no dispatcher. build runs on the caller's goroutine and is
// not retained. A failed dispatch goes to the failure policy (see Fail): a
// panic in the dispatcher is recovered here and is such a failure, as the
// typed panic error (panicerr.FromRecovered), so the component that emitted
// survives it, and it is recorded once (in an app, the dispatch function
// Recording built has already recovered and recorded it, so Fail skips
// it). The failure goes to the Failures and logger bound with the
// dispatcher it read.
func (e *Emitter) EmitBuilt(ctx context.Context, build func() any) bool {
	// Kept within the inlining budget, so the no-dispatcher path costs
	// the caller one atomic load and no call.
	return e != nil && e.Installed() && e.emitBuilt(ctx, build)
}

// emitBuilt is EmitBuilt past its fast check. It reads the binding again:
// a dispatcher removed meanwhile builds nothing, and the event goes to the
// dispatcher, Failures and logger of the binding it read.
func (e *Emitter) emitBuilt(ctx context.Context, build func() any) bool {
	b := e.binding.Load()
	if b == nil || b.dispatch == nil {
		return false
	}
	e.emit(b, ctx, build())
	return true
}

// emit hands event to b's dispatcher under ctx (context.Background when
// nil) and applies the failure policy to a failed dispatch against the
// same binding.
func (e *Emitter) emit(b *binding, ctx context.Context, event any) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := DispatchContained(ctx, b.dispatch, event); err != nil {
		e.fail(b, ctx, err, event)
	}
}

// Fail applies the failure policy to err, a failed dispatch of event: unless
// the dispatch function already recorded it (Recorded), it is recorded in
// the Failures the emitter shares (see Share and SetShared), or its own,
// logging through the component's logger (see UseLogger). A nil err is
// ignored.
func (e *Emitter) Fail(ctx context.Context, err error, event any) {
	e.fail(e.binding.Load(), ctx, err, event)
}

func (e *Emitter) fail(b *binding, ctx context.Context, err error, event any) {
	if err == nil || Recorded(err) {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.failuresOf(b).Record(ctx, logOf(b), err, event)
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
	b := e.binding.Load()
	f := e.failuresOf(b)
	f.count.Add(1)
	nested := f.hookRunningHere()
	return func() { f.report(ctx, logOf(b), err, event, nested) }
}

// Share makes the emitter record its failures in f, the app's Failures;
// nil returns it to its own.
func (e *Emitter) Share(f *Failures) {
	e.update(func(b *binding) { b.shared = f })
}

// FailureCount returns the count of the Failures the emitter records into.
func (e *Emitter) FailureCount() uint64 {
	return e.failures().Count()
}

// failures returns the Failures the emitter records into now.
func (e *Emitter) failures() *Failures {
	return e.failuresOf(e.binding.Load())
}

// failuresOf returns the Failures b records into: its shared one, or the
// emitter's own.
func (e *Emitter) failuresOf(b *binding) *Failures {
	if b != nil && b.shared != nil {
		return b.shared
	}
	return &e.own
}

// UseLogger makes the emitter log through the logger source returns at the
// time of a failure: the component's logger. A nil source, or a source that
// returns nil, means the framework's standalone fallback logger.
func (e *Emitter) UseLogger(source func() contract.Logger) {
	e.update(func(b *binding) { b.logger = source })
}

// logOf returns the logger b's source returns now, or nil.
func logOf(b *binding) contract.Logger {
	if b != nil && b.logger != nil {
		return b.logger()
	}
	return nil
}
