package router

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/velocitykode/velocity/internal/panicerr"
)

// ErrEventBufferFull is returned by an async dispatcher when the worker
// channel cannot accept more events. Callers of the event dispatcher in
// the router currently ignore the error; this sentinel is exposed so
// consumer code that wraps the dispatcher can observe drops.
var ErrEventBufferFull = errors.New("velocity/router: event buffer full, dropping event")

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
// first: its channel is closed and the call waits for its workers to
// deliver the events still buffered, to that pool's own target. When the
// prior pool was already stopped with a deadline that expired, the call
// does not wait again; that pool keeps draining in the background.
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
	ch := make(chan asyncDispatchItem, bufferSize)
	wg := r.startEventWorkers(ch, pool, workers)

	r.eventDispatcher = makeNonBlockingEnqueuer(ch)
	r.stopEventDispatcher = makeDrainCloser(ch, wg)
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
// dispatcher. Errors are intentionally swallowed — the caller is
// overwriting the dispatcher wholesale.
func (r *VelocityRouterV2) stopPriorAsyncDispatcher() {
	if r.stopEventDispatcher != nil {
		_ = r.stopEventDispatcher(context.Background())
	}
}

// startEventWorkers spawns worker goroutines that consume events from
// ch and invoke the pool's current target with panic recovery. Listener
// failures route through the shared reporter so drops/panics surface via
// the same metrics.
func (r *VelocityRouterV2) startEventWorkers(ch <-chan asyncDispatchItem, pool *asyncEventPool, workers int) *sync.WaitGroup {
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		// Not async.Go: each invocation is wrapped by safeInvokeListener,
		// which already recovers per listener and routes failures through
		// r.onListenerFailure for drop accounting. async.Go would log
		// panics in addition but bypass the drop counter.
		go func() {
			defer wg.Done()
			r.runEventWorker(ch, pool)
		}()
	}
	return &wg
}

// runEventWorker drains a single channel until close, delivering each
// event to the pool's target current when it is dequeued.
func (r *VelocityRouterV2) runEventWorker(ch <-chan asyncDispatchItem, pool *asyncEventPool) {
	for item := range ch {
		t := pool.target.Load()
		if t == nil {
			continue
		}
		safeInvokeListener(*t, item.ctx, item.event, r.onListenerFailure)
	}
}

// onListenerFailure is the error callback installed by the worker
// pool. Shares the same drop-accounting as dispatchInstanceEvent so
// metrics stay coherent across sync and async paths.
func (r *VelocityRouterV2) onListenerFailure(err error, ev interface{}) {
	r.droppedEvents.Add(1)
	typedEvent, _ := ev.(Event)
	if r.OnEventDispatchError != nil {
		r.OnEventDispatchError(err, typedEvent)
		return
	}
	if r.firstDropLogged.CompareAndSwap(false, true) &&
		r.services != nil && r.services.Log != nil {
		r.services.Log.Warn(
			"velocity: async listener error (first occurrence; poll Router.DroppedEventCount or set Router.OnEventDispatchError)",
			"error", err.Error(),
		)
	}
}

// makeNonBlockingEnqueuer returns a dispatcher that pushes events
// without blocking; when the channel is full it returns
// ErrEventBufferFull so the caller can account for the drop. The ctx
// passed by the caller is captured alongside the event so the worker
// goroutine delivers it to listeners.
func makeNonBlockingEnqueuer(ch chan<- asyncDispatchItem) func(ctx context.Context, event interface{}) error {
	return func(ctx context.Context, event interface{}) error {
		if ctx == nil {
			ctx = context.Background()
		}
		select {
		case ch <- asyncDispatchItem{ctx: ctx, event: event}:
			return nil
		default:
			return ErrEventBufferFull
		}
	}
}

// makeDrainCloser closes the channel and waits for workers to finish,
// respecting ctx cancellation. Subsequent calls return the cached
// result so repeated Shutdown invocations are safe.
func makeDrainCloser(ch chan asyncDispatchItem, wg *sync.WaitGroup) func(context.Context) error {
	var (
		stopOnce sync.Once
		stopErr  error
	)
	return func(ctx context.Context) error {
		stopOnce.Do(func() {
			close(ch)
			done := make(chan struct{})
			// Not async.Go: must close(done) on panic so Shutdown never
			// blocks waiting on a goroutine that already died.
			go func() {
				defer func() {
					// Workers finish draining even if we panic below.
					_ = recover()
					close(done)
				}()
				wg.Wait()
			}()
			select {
			case <-done:
				stopErr = nil
			case <-ctx.Done():
				stopErr = ctx.Err()
			}
		})
		return stopErr
	}
}

// ShutdownEventDispatcher drains pending events and stops dispatcher
// workers. It is safe to call whether SetAsyncEventDispatcher was used
// or not — in the synchronous case it is a no-op.
//
// If ctx expires before workers drain, ShutdownEventDispatcher returns
// ctx.Err() and workers continue in the background until their channel
// is empty; this is preferred over abrupt termination, which would drop
// events mid-handle.
//
// After the first call, subsequent calls return the cached result
// without re-draining.
func (r *VelocityRouterV2) ShutdownEventDispatcher(ctx context.Context) error {
	if r.stopEventDispatcher == nil {
		return nil
	}
	return r.stopEventDispatcher(ctx)
}

// safeInvokeListener executes a listener, recovering from panics. Listener
// errors and panic-converted errors are reported via onErr if set.
func safeInvokeListener(fn func(ctx context.Context, event interface{}) error, ctx context.Context, ev interface{}, onErr func(error, interface{})) {
	var err error
	defer func() {
		if p := recover(); p != nil {
			// Listener panics must not kill the worker pool.
			err = panicerr.FromRecovered(p)
		}
		if err != nil && onErr != nil {
			onErr(err, ev)
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	err = fn(ctx, ev)
}
