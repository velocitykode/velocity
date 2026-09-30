package router

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/drain"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/goroutine"
)

// ErrEventBufferFull is returned by an async dispatcher when the worker
// channel cannot accept more events. The router hands the drop to the
// failure policy (it is counted as a failed event); this sentinel is
// exposed so consumer code that wraps the dispatcher can observe drops.
var ErrEventBufferFull = errors.New("velocity/router: event buffer full, dropping event")

// errEventDispatcherStopped is what an async dispatcher returns for an
// event dispatched after ShutdownEventDispatcher (or a replacing
// SetAsyncEventDispatcher) stopped its pool: a request still running past
// the server's shutdown deadline dispatches its late events into a pool
// that no longer accepts them. The router counts the drop like a full
// buffer, as a failed event.
var errEventDispatcherStopped = errors.New("velocity/router: event dispatcher stopped, dropping event")

// SetAsyncEventDispatcher wires an event dispatcher that delivers events
// to fn from a pool of worker goroutines reading a buffered channel.
// Dispatch from request handlers is non-blocking: when the buffer is
// full, events are dropped and ErrEventBufferFull is returned.
//
// Use this in production to keep event listeners off the request hot
// path. Use SetEventDispatcher (synchronous) if your workload cannot
// tolerate drops and listeners are guaranteed fast.
//
// workers <= 0 defaults to runtime.NumCPU().
// bufferSize <= 0 defaults to 1024.
//
// Worker panics are recovered silently so one bad listener invocation
// does not tear down the pool.
//
// Calling SetAsyncEventDispatcher replaces any previously installed
// dispatcher. If a prior async dispatcher is running, it is stopped
// first: it stops accepting events and the call waits for its workers to
// deliver the events still buffered, to that pool's own target. When the
// prior pool was already stopped (with a deadline that expired, say), or
// the call comes from one of the prior pool's own listeners, it does not
// wait; that pool keeps draining in the background.
//
// A later BindEventDispatcher (the framework re-wiring the app dispatcher
// at a lifecycle boundary, see SetEventDispatcher) re-points this pool at
// the app's current Services.Events and keeps delivery async;
// SetEventDispatcher switches delivery back to sync. So fn survives only
// until the next boundary: an app that wants an independent sink calls it
// after Bootstrap() returns and before Serve(). Configuration calls must
// be serialized (see SetEventDispatcher).
func (r *VelocityRouterV2) SetAsyncEventDispatcher(fn func(ctx context.Context, event interface{}) error, workers, bufferSize int) {
	workers, bufferSize = normalizeAsyncSizing(workers, bufferSize)
	r.stopPriorAsyncDispatcher()

	pool := &asyncEventPool{}
	pool.setTarget(fn)
	stop := &asyncEventStop{q: &asyncEventQueue{ch: make(chan asyncDispatchItem, bufferSize)}}
	r.startEventWorkers(stop, pool, workers)

	r.events.Set(stop.q.enqueue)
	r.asyncStop = stop
	r.asyncPool = pool
}

// eventTargetFn is the function an async worker pool delivers to.
type eventTargetFn func(ctx context.Context, event interface{}) error

// asyncEventPool is one async worker pool's delivery target. Each pool
// owns its holder, so re-pointing the current pool never redirects an
// older pool that is still draining.
type asyncEventPool struct {
	target atomic.Pointer[eventTargetFn]
}

// setTarget sets the function the pool's workers deliver to; nil makes
// them drop events.
func (p *asyncEventPool) setTarget(fn func(ctx context.Context, event interface{}) error) {
	if fn == nil {
		p.target.Store(nil)
		return
	}
	t := eventTargetFn(fn)
	p.target.Store(&t)
}

// asyncDispatchItem couples a buffered event with the ctx that was in
// scope when it was enqueued so worker goroutines deliver listeners the
// originating request/job ctx instead of context.Background.
type asyncDispatchItem struct {
	ctx   context.Context
	event interface{}
}

// normalizeAsyncSizing applies defaults for <=0 inputs.
func normalizeAsyncSizing(workers, bufferSize int) (int, int) {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if bufferSize <= 0 {
		bufferSize = 1024
	}
	return workers, bufferSize
}

// stopPriorAsyncDispatcher tears down any previously-installed async
// dispatcher, waiting for its drain only when this call is the one that
// stops it. Errors are intentionally swallowed: the caller is overwriting
// the dispatcher wholesale.
func (r *VelocityRouterV2) stopPriorAsyncDispatcher() {
	if r.asyncStop != nil {
		_ = r.asyncStop.stop(context.Background(), false)
	}
}

// startEventWorkers spawns worker goroutines that consume events from
// the stop's queue and invoke the pool's current target with panic
// recovery. Listener failures route through the shared reporter so
// drops/panics surface via the same metrics. Each worker is recorded as
// the pool's stop work while it runs, so a stop its listener calls knows
// it cannot wait for the pool.
func (r *VelocityRouterV2) startEventWorkers(stop *asyncEventStop, pool *asyncEventPool, workers int) {
	for i := 0; i < workers; i++ {
		stop.workers.Add(1)
		// Not async.Go: each delivery goes through
		// eventemit.DispatchContained, which recovers a panicking target,
		// and its failure to the router's failure policy (r.events.Fail).
		// async.Go would log panics in addition but bypass the failure
		// count.
		go func() {
			defer stop.workers.Done()
			id := goroutine.ID()
			stop.stops.Work().Enter(id)
			defer stop.stops.Work().Leave(id)
			r.runEventWorker(stop.q.ch, pool)
		}()
	}
}

// runEventWorker drains a single channel until close, delivering each
// event to the pool's target current when it is dequeued.
func (r *VelocityRouterV2) runEventWorker(ch <-chan asyncDispatchItem, pool *asyncEventPool) {
	for item := range ch {
		t := pool.target.Load()
		if t == nil {
			continue
		}
		r.events.Fail(item.ctx, eventemit.DispatchContained(item.ctx, *t, item.event), item.event)
	}
}

// asyncEventQueue is the buffered channel one async worker pool reads,
// guarded so that no send ever meets the closed channel. A request
// goroutine may still dispatch after the pool stopped (a handler running
// past the server's shutdown deadline, or a Timeout handler goroutine
// reporting a late panic), so the stop and every send are ordered by mu:
// enqueue sends under the read lock only while stopped is false, and stop
// sets stopped and closes ch under the write lock. The read lock is
// shared, so senders never wait on each other, and the send inside it is
// non-blocking, so stop waits at most for the sends already in flight.
type asyncEventQueue struct {
	mu      sync.RWMutex
	stopped bool
	ch      chan asyncDispatchItem
}

// enqueue pushes an event without blocking. It returns
// ErrEventBufferFull when the channel is full and
// errEventDispatcherStopped once the pool stopped, so the caller can
// account for the drop. The ctx passed by the caller is captured
// alongside the event so the worker goroutine delivers it to listeners.
func (q *asyncEventQueue) enqueue(ctx context.Context, event interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.stopped {
		return errEventDispatcherStopped
	}
	select {
	case q.ch <- asyncDispatchItem{ctx: ctx, event: event}:
		return nil
	default:
		return ErrEventBufferFull
	}
}

// stop makes every later enqueue a drop and closes the channel, so the
// workers exit once they have delivered what it still buffers.
func (q *asyncEventQueue) stop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.stopped {
		q.stopped = true
		close(q.ch)
	}
}

// asyncEventStop is one async worker pool's stop. The first stop owns
// the drain: it stops the queue and waits, on a goroutine of its own, for
// the workers to deliver what it still buffers. A stop that overlaps or
// follows it waits for that drain or its own ctx, and a stop called from
// one of the pool's listeners returns at once, since it runs on a worker
// the drain waits for.
type asyncEventStop struct {
	q       *asyncEventQueue
	workers sync.WaitGroup
	// mu guards the drain the owning stop began (stops.Begin, Ended).
	mu    sync.Mutex
	stops drain.Coordinator
}

// stop stops the pool. The owning stop, and with joinWaits every later
// one, waits for the drain until ctx is done and returns ctx.Err() then;
// the workers keep draining in the background. Without joinWaits a stop
// that joins a drain already under way returns at once.
func (s *asyncEventStop) stop(ctx context.Context, joinWaits bool) error {
	nested := s.stops.Nested()
	owner, drained := s.begin()
	if owner {
		s.q.stop()
		async.Go(func() { s.stops.Drain(drained, s.workers.Wait) })
	}
	switch {
	case drain.Closed(drained):
		return nil
	case nested:
		return errchain.Errorf("velocity/router: event dispatcher stopped from its own listener; the pool drains without this call waiting for it: %w: %w", contract.ErrStopFromOwnWork, errEventDispatcherStopped)
	case !owner && !joinWaits:
		return nil
	}
	return s.stops.Await(ctx, drained, nil)
}

// begin records a stop: the first owns the drain and gets a fresh drained
// channel, a later one the owner's.
func (s *asyncEventStop) begin() (owner bool, drained chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.stops.Ended(); d != nil {
		return false, d
	}
	return true, s.stops.Begin()
}

// OwnsCaller reports whether the router owns the calling goroutine:
// whether the goroutine is one of the current async event pool's workers,
// delivering an event to a listener. It is false when delivery is
// synchronous.
// The answer is for this instance only: another router's work, or
// another app's, is not this one's. App.Shutdown asks it because its
// teardown drains the event pool and waits for that work, so a Shutdown
// called from the work would wait on itself; it is refused instead. Like the other configuration
// calls, it must not overlap SetAsyncEventDispatcher.
func (r *VelocityRouterV2) OwnsCaller() bool {
	return r.asyncStop != nil && r.asyncStop.stops.Nested()
}

// ShutdownEventDispatcher drains pending events and stops dispatcher
// workers. It is safe to call whether SetAsyncEventDispatcher was used
// or not — in the synchronous case it is a no-op.
//
// Events already queued when it is called are delivered to the pool's
// target. An event dispatched after it was called (a request still
// running past the server's shutdown deadline) is dropped and counted as a
// failed event.
//
// If ctx expires before workers drain, ShutdownEventDispatcher returns
// ctx.Err() and workers continue in the background until their channel
// is empty; this is preferred over abrupt termination, which would drop
// events mid-handle.
//
// The first call stops the pool; a call that overlaps or follows it
// waits for that same drain, or its own ctx, and returns nil once the
// drain is over. A call from one of the pool's own listeners cannot wait
// for the workers it runs on: it stops the pool and returns an error at
// once.
func (r *VelocityRouterV2) ShutdownEventDispatcher(ctx context.Context) error {
	if r.asyncStop == nil {
		return nil
	}
	return r.asyncStop.stop(ctx, true)
}
