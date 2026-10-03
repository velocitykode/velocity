package router

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/velocitykode/velocity/internal/drain"
	"github.com/velocitykode/velocity/internal/eventemit"
)

// ErrEventBufferFull is returned by an async dispatcher when the worker
// channel cannot accept more events. The router hands the drop to the
// failure policy (it is counted as a failed event); this sentinel is
// exposed so consumer code that wraps the dispatcher can observe drops.
var ErrEventBufferFull = errors.New("velocity/router: event buffer full, dropping event")

// errEventDispatcherStopped is what an async dispatcher returns for an
// event that reaches its pool after the pool stopped: a dispatch racing a
// replacing SetAsyncEventDispatcher or SetEventDispatcher, or one from
// work the router does not admit (a goroutine a handler started) after
// Shutdown. Shutdown itself stops a pool only once every request it
// admitted returned. The router counts the drop like a full buffer, as a
// failed event.
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
// prior pool was already stopping, or the call comes from the router's
// own work (one of its listeners, a request handler), it does not wait;
// that pool keeps draining in the background, and the router's Shutdown
// waits for it. Called once the router's Shutdown began, the new pool is
// stopped at once.
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
	r.retireAsyncPool()

	pool := &asyncEventPool{}
	pool.setTarget(fn)
	stop := &asyncEventStop{
		q:   &asyncEventQueue{ch: make(chan asyncDispatchItem, bufferSize)},
		run: r.own.NewRun(),
	}
	r.startEventWorkers(stop, pool, workers)

	r.events.Set(stop.q.enqueue)
	r.asyncStop = stop
	r.asyncPool = pool
	if r.requestsStopping() {
		r.own.Signal(stop.run, stop.work)
	}
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

// retireAsyncPool stops the current async pool, if any, and keeps it with
// the router until its workers delivered what it buffered: the call waits
// for that, except when the pool was already stopping or the call comes
// from the router's own work, which the pool's drain may wait for; the
// router's Shutdown then waits for it. After it, delivery is sync until a
// new pool is installed.
func (r *VelocityRouterV2) retireAsyncPool() {
	p := r.asyncStop
	if p == nil {
		return
	}
	r.asyncStop, r.asyncPool = nil, nil
	if p.run.Stopping() || r.own.Nested() {
		r.own.Signal(p.run, p.work)
		return
	}
	_ = r.own.Stop(context.Background(), p.run, p.work, nil)
}

// startEventWorkers spawns worker goroutines that consume events from
// the stop's queue and invoke the pool's current target with panic
// recovery. Listener failures route through the shared reporter so
// drops/panics surface via the same metrics. Each worker is a unit
// admitted into the pool's run, and the router's own work while it runs,
// so a stop its listener calls knows it cannot wait for the pool. The
// pool is recorded with the router until its run finished.
func (r *VelocityRouterV2) startEventWorkers(stop *asyncEventStop, pool *asyncEventPool, workers int) {
	r.keepPool(stop)
	for i := 0; i < workers; i++ {
		stop.run.Admit()
		// Not async.Go: each delivery goes through
		// eventemit.DispatchContained, which recovers a panicking target,
		// and its failure to the router's failure policy (r.events.Fail).
		// async.Go would log panics in addition but bypass the failure
		// count.
		go func() { //safe-goroutine: deliveries are contained by eventemit.DispatchContained, see above
			defer stop.run.Release()
			id := r.own.Enter()
			defer r.own.Leave(id)
			r.runEventWorker(stop.q.ch, pool)
		}()
	}
}

// keepPool records a pool the router started, dropping the ones whose
// run finished.
func (r *VelocityRouterV2) keepPool(p *asyncEventStop) {
	r.poolsMu.Lock()
	defer r.poolsMu.Unlock()
	kept := r.pools[:0]
	for _, q := range r.pools {
		if !drain.Closed(q.run.Finished()) {
			kept = append(kept, q)
		}
	}
	r.pools = append(kept, p)
}

// startedPools returns every pool the router started whose run has not
// finished.
func (r *VelocityRouterV2) startedPools() []*asyncEventStop {
	r.poolsMu.Lock()
	defer r.poolsMu.Unlock()
	return append([]*asyncEventStop(nil), r.pools...)
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

// asyncEventStop is one async worker pool's stop: its queue, and its run,
// whose admitted units are the pool's workers. The pool's stop stops the
// queue, so no later event is accepted, and waits for the workers to
// deliver what it still buffers.
type asyncEventStop struct {
	q   *asyncEventQueue
	run *drain.Run
}

// work is the pool's stop, run once per pool by internal/drain.
func (s *asyncEventStop) work() error {
	s.q.stop()
	<-s.run.Idle()
	return nil
}
