package teardown

import (
	"context"
	"errors"

	"github.com/velocitykode/velocity/internal/drain"
)

// Children shuts down the children a manager holds (its stores, channels,
// disks), one run at a time, through the manager's drain.Owner. The zero
// value is ready; a Children must not be copied after use.
//
// A manager's Shutdown empties its registry and hands what it held to
// Detach, with its lock held, then runs the returned wait with no lock
// held:
//
//   - a Shutdown that finds children in the registry detaches them into a
//     new run; the run shuts each down contained (Close), on a goroutine of
//     the owner's own, and keeps the joined result;
//   - every Shutdown waits for the latest run, or for its own ctx; at ctx
//     the run goes on. The run is bounded by the ctx of the Shutdown that
//     began it: its children's Shutdowns receive that ctx, and later
//     callers await with their own. A Shutdown that overlaps a run waits for the same
//     run, and one that finds nothing new returns that run's retained
//     result (also when children were published and removed again since);
//     one on a manager that never held a child returns nil;
//   - a run begun while the one before it is still shutting down first
//     waits for that run and joins its result, so a Shutdown's result
//     covers every child published before the call, including those an
//     earlier Shutdown was still closing;
//   - a Shutdown called from a child's Shutdown (the owner's own work)
//     returns an error wrapping contract.ErrStopFromOwnWork at once: it
//     would wait on itself.
type Children[V any] struct {
	own  drain.Owner
	last *childrenRun
}

// childrenRun is one run and its work.
type childrenRun struct {
	run  *drain.Run
	work func(ctx context.Context) error
}

// Detach begins the Shutdown of a manager: the caller holds the manager's
// lock and passes children, what its registry held, which it has emptied.
// named wraps a child's shutdown error with the child's name; it runs in
// the run's work, never under the caller's lock. Detach calls no code of
// the caller's and returns the wait the caller runs, with no lock held,
// for the Shutdown's result.
func (c *Children[V]) Detach(children map[string]V, named func(name string, err error) error) func(ctx context.Context) error {
	if len(children) > 0 {
		var prev *drain.Run
		if c.last != nil && !drain.Closed(c.last.run.Finished()) {
			prev = c.last.run
		}
		c.last = &childrenRun{
			run:  c.own.NewRun(),
			work: func(ctx context.Context) error { return shutdownChildren(ctx, prev, children, named) },
		}
	}
	last := c.last
	if last == nil {
		return func(context.Context) error { return nil }
	}
	return func(ctx context.Context) error {
		return c.own.Stop(ctx, last.run, func() error { return last.work(ctx) }, nil)
	}
}

// shutdownChildren is the work of one run: it waits for prev, the run
// before it when that one had not finished as this one began, and joins
// its result; then it shuts every child down contained and joins the
// errors.
func shutdownChildren[V any](ctx context.Context, prev *drain.Run, children map[string]V, named func(string, error) error) error {
	var errs []error
	if prev != nil {
		errs = append(errs, prev.Await(context.Background(), nil))
	}
	for name, child := range children {
		if err := Close(ctx, child); err != nil {
			errs = append(errs, named(name, err))
		}
	}
	return errors.Join(errs...)
}
