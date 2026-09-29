package grpc

import (
	"context"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/internal/goroutine"
)

// stopCoordinator is the stop coordination the Server and the Gateway
// share. The stop that ends a running component owns its drain: it makes
// the drained channel, runs the transport's graceful or forced stop, and
// closes the channel when that returns. A stop that overlaps it waits on
// the channel, or its own ctx, then forces. The coordinator also records
// the goroutines running stop work (the owner's diagnostic line, the
// transport stop, which calls a caller-supplied listener's Close, and the
// Server's serve loop, which calls its Addr): a stop entered from one of
// them is nested in work that cannot finish until it returns, so it must
// neither wait on that work nor call the transport's stop on the same
// goroutine. The zero value is ready.
type stopCoordinator struct {
	inside goroutine.Set

	// drained is the owning stop's channel, nil until a stop ended the
	// component. Guarded by the component's mu.
	drained chan struct{}
}

// begin makes the drain of the stop that owns it and returns it. The
// caller holds the component's mu.
func (c *stopCoordinator) begin() chan struct{} {
	c.drained = make(chan struct{})
	return c.drained
}

// ended returns the drain of the stop that ended the component, nil when
// none has. The caller holds the component's mu.
func (c *stopCoordinator) ended() chan struct{} {
	return c.drained
}

// drain runs the owner's transport stop as stop work, then closes drained
// for every stop that overlaps it, whatever stop does.
func (c *stopCoordinator) drain(drained chan struct{}, stop func()) {
	defer close(drained)
	c.run(stop)
}

// run runs fn with the calling goroutine recorded as running stop work.
func (c *stopCoordinator) run(fn func()) {
	id := goroutine.ID()
	c.inside.Enter(id)
	defer c.inside.Leave(id)
	fn()
}

// nested reports whether the calling goroutine is running stop work.
func (c *stopCoordinator) nested() bool {
	return c.inside.Contains(goroutine.ID())
}

// await waits until drained is closed or ctx is done. At ctx, unless
// drained closed meanwhile, it starts force on its own goroutine, without
// waiting on it, and returns ctx.Err(); otherwise it returns nil.
func (c *stopCoordinator) await(ctx context.Context, drained <-chan struct{}, force func()) error {
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
	}
	if closed(drained) {
		return nil
	}
	async.Go(force)
	return ctx.Err()
}

// closed reports whether done is closed.
func closed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}
