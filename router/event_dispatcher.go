package router

import (
	"context"
	"errors"
	"runtime"
	"sync"
)

// ErrEventBufferFull is returned by an async dispatcher when the worker
// channel cannot accept more events. Callers of the event dispatcher in
// the router currently ignore the error; this sentinel is exposed so
// consumer code that wraps the dispatcher can observe drops.
var ErrEventBufferFull = errors.New("velocity: event buffer full, dropping event")

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
// first; any events still in its buffer are dropped.
func (r *VelocityRouterV2) SetAsyncEventDispatcher(fn func(event interface{}) error, workers, bufferSize int) {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if bufferSize <= 0 {
		bufferSize = 1024
	}

	// Stop any previously-running async workers. We discard the error
	// here because the caller is replacing the dispatcher wholesale.
	if r.stopEventDispatcher != nil {
		_ = r.stopEventDispatcher(context.Background())
	}

	ch := make(chan interface{}, bufferSize)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ev := range ch {
				safeInvokeListener(fn, ev)
			}
		}()
	}

	r.eventDispatcher = func(event interface{}) error {
		select {
		case ch <- event:
			return nil
		default:
			return ErrEventBufferFull
		}
	}

	var stopOnce sync.Once
	var stopErr error
	r.stopEventDispatcher = func(ctx context.Context) error {
		stopOnce.Do(func() {
			close(ch)
			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
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

func safeInvokeListener(fn func(event interface{}) error, ev interface{}) {
	defer func() {
		if p := recover(); p != nil {
			// Swallow — listener panics must not kill the worker pool.
			// Users who want visibility should recover inside their own fn.
			_ = p
		}
	}()
	_ = fn(ev)
}
