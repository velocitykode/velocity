// Package drain coordinates the stops of a component that runs work: the
// goroutines running the component's own work (Owner), and each run of it
// from its start to its completion (Run). One stop of a run owns its
// drain, every stop awaits the same completion or its own context, a run
// is forced at most once, and a stop called back from the work it would
// wait on never waits. It imports only the standard library, contract,
// internal/goroutine, internal/panicerr and async.
package drain

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/goroutine"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// Owner is a component's own work across all its runs: the goroutines
// running it, each recorded from before its first call to user code until
// after its last. A stop entered from one of them (Nested) is nested in
// work that cannot finish until it returns, so it must not wait on that
// work. The zero value is ready; an Owner must not be copied after use.
type Owner struct {
	work goroutine.Set
}

// Nested reports whether the calling goroutine is running the owner's
// work.
func (o *Owner) Nested() bool {
	return o.work.Contains(goroutine.ID())
}

// Enter records the calling goroutine as running the owner's work and
// returns its id for the matching Leave, for a goroutine whose work does
// not fit one Do (it defers its own cleanup around the work).
func (o *Owner) Enter() uint64 {
	id := goroutine.ID()
	o.work.Enter(id)
	return id
}

// Leave undoes the Enter that returned id.
func (o *Owner) Leave(id uint64) {
	o.work.Leave(id)
}

// Do runs fn on the calling goroutine, recorded as the owner's work.
func (o *Owner) Do(fn func()) {
	id := o.Enter()
	defer o.Leave(id)
	fn()
}

// Go runs fn on a goroutine of its own, recorded as the owner's work from
// its first statement. A panic in fn is contained by async.Go.
func (o *Owner) Go(fn func()) {
	async.Go(func() { o.Do(fn) })
}

// NewRun returns a new run of the owner, admitting work until it is
// closed. Its pointer is the run's identity: a loop or task that holds it
// acts on its own run only, never on a later one.
func (o *Owner) NewRun() *Run {
	return &Run{
		owner:    o,
		idle:     make(chan struct{}),
		finished: make(chan struct{}),
	}
}

// Stop runs one stop of r and returns its result:
//
//   - called from the owner's own work, it waits for nothing and changes
//     nothing: it returns r's result when r has finished, else an error
//     wrapping contract.ErrStopFromOwnWork;
//   - the first Stop of r closes r's admission and runs work on a
//     goroutine of its own, recorded as the owner's work, then finishes r
//     with work's result (a panic in work is its result);
//   - every Stop, the first, an overlapping or a later one, then awaits r
//     (see Run.Await): r's retained result, or ctx's error at ctx, when
//     force is claimed.
//
// work runs off the caller's goroutine so that neither user code in it
// nor a Stop it calls back can hold the caller past ctx; at ctx the work
// goes on, and its result is kept for the Stops that follow. When the run
// admits work, work waits for r.Idle itself, at the point its sequence
// needs. A nil work finishes r with nil.
func (o *Owner) Stop(ctx context.Context, r *Run, work func() error, force func()) error {
	if o.Nested() {
		if Closed(r.finished) {
			return r.err
		}
		return fmt.Errorf("velocity: stop called from the work it would wait for: %w", contract.ErrStopFromOwnWork)
	}
	if r.Close() {
		o.Go(func() { r.Finish(runContained(work)) })
	}
	return r.Await(ctx, force)
}

// runContained runs work, returning a panic in it as its error.
func runContained(work func() error) (err error) {
	if work == nil {
		return nil
	}
	defer func() {
		if p := recover(); p != nil {
			err = panicerr.FromRecovered(p)
		}
	}()
	return work()
}

// closedBit marks a run's admission closed in Run.state; the bits below
// it count the admitted work not yet released.
const closedBit = int64(1) << 62

// Run is one run of a component, from its start to its completion.
//
// Admission: Admit counts one unit of work (a task, a dispatch, a pump)
// until Close, and fails from then on, so no unit starts after the stop
// began waiting; Idle is closed once admission is closed and every
// admitted unit was released. A run nobody admits into is idle at Close.
//
// Completion: Finish publishes the run's result once; Await returns it to
// every stop, overlapping or later, and never returns before it except at
// the stop's own ctx. The run is forced at most once.
//
// Nothing in Run holds a lock across a call, and its methods are safe for
// concurrent use.
type Run struct {
	owner *Owner
	// state is the count of admitted, unreleased units, plus closedBit
	// once admission is closed.
	state    atomic.Int64
	idle     chan struct{}
	finished chan struct{}
	finish   atomic.Bool
	// err is the run's result, written before finished is closed.
	err    error
	forced atomic.Bool
}

// Admit counts one unit of work, to be released with Release, and
// reports true; once admission is closed it counts nothing and reports
// false.
func (r *Run) Admit() bool {
	for {
		s := r.state.Load()
		if s&closedBit != 0 {
			return false
		}
		if r.state.CompareAndSwap(s, s+1) {
			return true
		}
	}
}

// Release ends one unit Admit counted. The last one released after Close
// makes the run idle.
func (r *Run) Release() {
	if r.state.Add(-1) == closedBit {
		close(r.idle)
	}
}

// Close closes admission and reports whether this call closed it: exactly
// one caller gets true, and it owns the run's stop.
func (r *Run) Close() bool {
	old := r.state.Or(closedBit)
	if old&closedBit != 0 {
		return false
	}
	if old == 0 {
		close(r.idle)
	}
	return true
}

// Stopping reports whether a stop has closed the run's admission, so the
// run is draining or finished rather than running.
func (r *Run) Stopping() bool {
	return r.state.Load()&closedBit != 0
}

// Idle returns a channel closed once admission is closed and every
// admitted unit was released.
func (r *Run) Idle() <-chan struct{} {
	return r.idle
}

// Finish publishes err as the run's result. The first call wins; later
// ones do nothing.
func (r *Run) Finish(err error) {
	if r.finish.CompareAndSwap(false, true) {
		r.err = err
		close(r.finished)
	}
}

// Finished returns a channel closed once the run has finished.
func (r *Run) Finished() <-chan struct{} {
	return r.finished
}

// Await waits until the run has finished and returns its result, or until
// ctx is done. At ctx, unless the run finished meanwhile, it claims the
// run's force, when force is non-nil and no Await claimed it before, and
// starts it as the owner's work on a goroutine of its own without waiting
// on it, then returns ctx's error. A nil ctx waits without a bound.
func (r *Run) Await(ctx context.Context, force func()) error {
	if ctx == nil {
		<-r.finished
		return r.err
	}
	select {
	case <-r.finished:
		return r.err
	case <-ctx.Done():
	}
	if Closed(r.finished) {
		return r.err
	}
	if force != nil && r.forced.CompareAndSwap(false, true) {
		r.owner.Go(force)
	}
	return ctx.Err()
}

// Coordinator is the stop coordination of a component that stops once:
// the stop that ends it owns its drain, makes the drained channel with
// Begin, runs the stop with Drain, which closes the channel when the stop
// returns, and a stop that overlaps it waits on the channel, or its own
// ctx, with Await. Its own work (a stop's diagnostic line, the transport
// stop, a serve loop, a task) is recorded like an Owner's, so a stop
// entered from it (Nested) neither waits on that work nor runs the
// transport stop on the same goroutine. A component that runs more than
// once uses an Owner and a Run per run instead. The zero value is ready.
type Coordinator struct {
	own Owner

	// drained is the owning stop's channel, nil until a stop ended the
	// component. Guarded by the component's lock.
	drained chan struct{}

	// forced claims the stop's one force (see Await).
	forced atomic.Bool
}

// Begin makes the drain of the stop that owns it and returns it. The
// caller holds the component's lock.
func (c *Coordinator) Begin() chan struct{} {
	c.drained = make(chan struct{})
	return c.drained
}

// Ended returns the drain of the stop that ended the component, nil when
// none has. The caller holds the component's lock.
func (c *Coordinator) Ended() chan struct{} {
	return c.drained
}

// Drain runs the owner's stop as stop work, then closes drained for every
// stop that overlaps it, whatever stop does.
func (c *Coordinator) Drain(drained chan struct{}, stop func()) {
	defer close(drained)
	c.own.Do(stop)
}

// Run runs fn with the calling goroutine recorded as running stop work.
func (c *Coordinator) Run(fn func()) {
	c.own.Do(fn)
}

// Work returns the set of goroutines running stop work, for work that
// enters it on a goroutine of its own and leaves it later than a Run
// would allow.
func (c *Coordinator) Work() *goroutine.Set {
	return &c.own.work
}

// Nested reports whether the calling goroutine is running stop work.
func (c *Coordinator) Nested() bool {
	return c.own.Nested()
}

// Await waits until done is closed or ctx is done. At ctx, unless done
// closed meanwhile, it returns ctx.Err(), and the first Await to get there
// with a force starts it as stop work on a goroutine of its own, without
// waiting on it: the component stops once, so its stop is forced at most
// once however many stops time out waiting on it. Otherwise it returns
// nil.
func (c *Coordinator) Await(ctx context.Context, done <-chan struct{}, force func()) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
	}
	if Closed(done) {
		return nil
	}
	if force != nil && c.forced.CompareAndSwap(false, true) {
		c.own.Go(force)
	}
	return ctx.Err()
}

// Closed reports whether done is closed.
func Closed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}
