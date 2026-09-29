package grpc

import (
	"context"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/internal/goroutine"
)

// stopGuard records the goroutines running a stop's own work for a Server
// or Gateway: its diagnostic line, the transport stop (which calls a
// caller-supplied listener's Close) and the serve loop (which calls its
// Addr). A stop entered from one of them is nested in work that cannot
// finish until it returns, so it must neither wait on that work nor call
// the transport's stop on the same goroutine. The zero value is ready.
type stopGuard struct{ inside goroutine.Set }

// run runs fn with the calling goroutine recorded as running stop work.
func (g *stopGuard) run(fn func()) {
	id := goroutine.ID()
	g.inside.Enter(id)
	defer g.inside.Leave(id)
	fn()
}

// nested reports whether the calling goroutine is running stop work.
func (g *stopGuard) nested() bool {
	return g.inside.Contains(goroutine.ID())
}

// awaitStop waits until done is closed or ctx is done. At ctx, unless done
// closed meanwhile, it starts force on its own goroutine, without waiting
// on it, and returns ctx.Err(); otherwise it returns nil.
func awaitStop(ctx context.Context, done <-chan struct{}, force func()) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
	}
	select {
	case <-done:
		return nil
	default:
		async.Go(force)
		return ctx.Err()
	}
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
