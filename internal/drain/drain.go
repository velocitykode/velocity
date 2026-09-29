// Package drain coordinates the stops of a component that serves work:
// one stop owns the drain, stops that overlap it wait for it or their own
// context, and a stop called back from the work it would wait on never
// waits. The gRPC server and gateway and the scheduler share it. It
// imports only the standard library, internal/goroutine and async.
package drain

import (
	"context"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/internal/goroutine"
)

// Coordinator is one component's stop coordination. The stop that ends a
// running component owns its drain: it makes the drained channel with
// Begin, runs the stop with Drain, which closes the channel when the stop
// returns, and a stop that overlaps it waits on the channel, or its own
// ctx, with Await. The Coordinator also records the goroutines running
// the component's stop work (a stop's diagnostic line, the transport stop,
// which may call caller code, a serve loop or a task): a stop entered from
// one of them (Nested) is nested in work that cannot finish until it
// returns, so it must neither wait on that work nor run the transport stop
// on the same goroutine. The zero value is ready.
type Coordinator struct {
	inside goroutine.Set

	// drained is the owning stop's channel, nil until a stop ended the
	// component. Guarded by the component's lock.
	drained chan struct{}
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
	c.Run(stop)
}

// Run runs fn with the calling goroutine recorded as running stop work.
func (c *Coordinator) Run(fn func()) {
	id := goroutine.ID()
	c.inside.Enter(id)
	defer c.inside.Leave(id)
	fn()
}

// Work returns the set of goroutines running stop work, for work that
// enters it on a goroutine of its own and leaves it later than a Run
// would allow.
func (c *Coordinator) Work() *goroutine.Set {
	return &c.inside
}

// Nested reports whether the calling goroutine is running stop work.
func (c *Coordinator) Nested() bool {
	return c.inside.Contains(goroutine.ID())
}

// Await waits until done is closed or ctx is done. At ctx, unless done
// closed meanwhile, it starts force, when there is one, as stop work on a
// goroutine of its own, without waiting on it, and returns ctx.Err();
// otherwise it returns nil.
func (c *Coordinator) Await(ctx context.Context, done <-chan struct{}, force func()) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
	}
	if Closed(done) {
		return nil
	}
	if force != nil {
		async.Go(func() { c.Run(force) })
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
