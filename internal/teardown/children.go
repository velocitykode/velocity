package teardown

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/drain"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/nilval"
)

// Children is the lifecycle of the children a manager holds (its stores,
// channels, disks, connections): every way a child leaves the registry
// closes it contained, exactly once per instance, on runs of the
// manager's own drain.Owner. The zero value is ready; a Children must not
// be copied after use.
//
// The manager calls Shutdown, Retire and Generation with its lock held;
// none of them calls code of the manager's or a child's. The closes they
// return run with no lock held.
//
// Shutdown:
//
//   - a Shutdown called from a child's close (the owner's own work, a
//     retirement included) returns an error wrapping
//     contract.ErrStopFromOwnWork at once and changes nothing: the
//     registry keeps its children and the generation stays; it would wait
//     on itself;
//   - every other Shutdown empties the registry and advances the
//     generation, also when the registry is empty;
//   - a Shutdown that finds children, or retirements since the last run,
//     begins a new run: the run closes each child once per instance
//     (Close, through Drain when the child's close takes the ctx, so a
//     child returning at ctx is waited for), on a goroutine of the owner's
//     own, then waits for the retirements it took in, and keeps the
//     joined close errors as its result;
//   - every Shutdown waits for the latest run, or for its own ctx; at ctx
//     the run goes on. The run is bounded by the ctx of the Shutdown that
//     began it: its children's closes receive that ctx, and later callers
//     await with their own. A Shutdown that overlaps a run waits for the
//     same run, and one that finds nothing new returns that run's retained
//     result (also when children were published and removed again since);
//     one on a manager that never held a child returns nil;
//   - a run begun while the one before it is still shutting down first
//     waits for that run and joins its result, so a Shutdown's result
//     covers every child published before the call, including those an
//     earlier Shutdown was still closing.
//
// Retire: a child that leaves the registry any other way (replaced,
// removed, cleared, discarded) is closed contained by the func Retire
// returns, unless the registry still holds the same instance under
// another name or it is already being retired. The next Shutdown's run
// waits for that close; its error is the retiring call's to report, and
// the run's result does not repeat it.
//
// Generation counts the accepted Shutdowns: a manager that builds a
// child with no lock held reads it before the build and compares it,
// under the lock, before it publishes, so a child built across a Shutdown
// is closed instead of published into the emptied registry.
//
// Closing returns the children that left the registry and whose closes
// have not returned: a manager that answers for its children's own work
// (OwnsCaller) asks them as well as the registry, since its Shutdown
// still waits for them.
type Children[V any] struct {
	own  drain.Owner
	last *childrenRun
	// generation counts accepted Shutdowns. Guarded by the manager's lock.
	generation uint64
	// pending is the run the retirements since the last Shutdown are
	// admitted into; the next Shutdown takes it as its run. Guarded by the
	// manager's lock.
	pending *drain.Run
	// mu guards retiring and closing, which a retirement's close and a
	// run's work leave with no manager lock held. No code of a caller's
	// runs under it.
	mu sync.Mutex
	// retiring holds the instances whose retirement close has not
	// returned.
	retiring []any
	// closing holds, for each run whose work has not returned, the
	// children that run detached from the registry.
	closing map[*drain.Run][]V
}

// childrenRun is one run and its work.
type childrenRun struct {
	run  *drain.Run
	work func(ctx context.Context) error
}

// Shutdown begins the Shutdown of a manager: the caller holds the
// manager's lock and passes its registry, which Shutdown empties unless
// the call is refused (see Children). named wraps a child's close error
// with the child's name; it runs in the run's work, never under the
// caller's lock. Shutdown returns the wait the caller runs, with no lock
// held, for the Shutdown's result.
func (c *Children[V]) Shutdown(registry *map[string]V, named func(name string, err error) error) func(ctx context.Context) error {
	if c.own.Nested() {
		return func(context.Context) error {
			return errchain.Errorf("velocity: Shutdown called from the close of a child it would wait for: %w", contract.ErrStopFromOwnWork)
		}
	}
	children := *registry
	*registry = make(map[string]V)
	c.generation++
	if len(children) > 0 || c.pending != nil {
		var prev *drain.Run
		if c.last != nil && !drain.Closed(c.last.run.Finished()) {
			prev = c.last.run
		}
		run := c.pending
		if run == nil {
			run = c.own.NewRun()
		}
		c.pending = nil
		c.detach(run, children)
		c.last = &childrenRun{
			run:  run,
			work: func(ctx context.Context) error { return c.shutdownChildren(ctx, prev, run, children, named) },
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
// its result; it closes every child once per instance, skipping one a
// retirement is closing, and joins the errors; then it waits for the
// retirements admitted into run, whose admission the Stop closed first.
func (c *Children[V]) shutdownChildren(ctx context.Context, prev, run *drain.Run, children map[string]V, named func(string, error) error) error {
	defer c.detach(run, nil)
	var errs []error
	if prev != nil {
		errs = append(errs, prev.Await(context.Background(), nil))
	}
	names := make([]string, 0, len(children))
	for name := range children {
		names = append(names, name)
	}
	slices.Sort(names)
	var closed []any
	for _, name := range names {
		child := any(children[name])
		if contains(closed, child) || c.isRetiring(child) {
			continue
		}
		closed = append(closed, child)
		if err := closeChild(ctx, child); err != nil {
			errs = append(errs, named(name, err))
		}
	}
	<-run.Idle()
	return errors.Join(errs...)
}

// Retire takes a child out of the manager's lifecycle when it left the
// registry other than by Shutdown: the caller holds the manager's lock,
// has already changed the registry, and passes it as held. It returns the
// close the caller runs once, with no lock held: it closes the child
// contained (Close, with a background ctx) on the calling goroutine,
// recorded as the owner's work, and returns the child's close error,
// which the caller reports. The next Shutdown waits for it.
//
// The close does nothing, and returns nil, when child is nil or a typed
// nil, when held still has the same instance under another name, or when
// that instance is already being retired; and when it is run again.
func (c *Children[V]) Retire(held map[string]V, child V) func() error {
	v := any(child)
	if nilval.Is(v) {
		return noClose
	}
	for _, h := range held {
		if SameInstance(any(h), v) {
			return noClose
		}
	}
	c.mu.Lock()
	if contains(c.retiring, v) {
		c.mu.Unlock()
		return noClose
	}
	c.retiring = append(c.retiring, v)
	c.mu.Unlock()
	if c.pending == nil {
		c.pending = c.own.NewRun()
	}
	run := c.pending
	// pending is only replaced under the manager's lock, and a Shutdown
	// closes its admission after taking it, so this Admit cannot fail.
	run.Admit()
	var ran atomic.Bool
	return func() (err error) {
		if !ran.CompareAndSwap(false, true) {
			return nil
		}
		defer run.Release()
		defer c.forget(v)
		c.own.Do(func() { err = Close(context.Background(), v) })
		return err
	}
}

// noClose is the close of a child Retire does not close.
func noClose() error { return nil }

// Generation returns the count of accepted Shutdowns. The caller holds
// the manager's lock.
func (c *Children[V]) Generation() uint64 {
	return c.generation
}

// OwnsCaller reports whether the calling goroutine is closing one of the
// manager's children, so a stop it calls that waits for the manager's
// Shutdown would wait on itself. Read without a lock.
func (c *Children[V]) OwnsCaller() bool {
	return c.own.Nested()
}

// Closing returns the children that left the registry and whose closes
// have not returned: those a Shutdown's run detached, until that run's
// work returns, and those a retirement is closing. A child held under two
// names appears once per name. It takes no manager lock and calls no code
// of the manager's or a child's.
func (c *Children[V]) Closing() []V {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []V
	for _, children := range c.closing {
		out = append(out, children...)
	}
	for _, r := range c.retiring {
		if v, ok := r.(V); ok {
			out = append(out, v)
		}
	}
	return out
}

// detach records children as the ones run closes; with none it forgets
// run, whose work returned.
func (c *Children[V]) detach(run *drain.Run, children map[string]V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(children) == 0 {
		delete(c.closing, run)
		return
	}
	if c.closing == nil {
		c.closing = make(map[*drain.Run][]V)
	}
	c.closing[run] = slices.Collect(maps.Values(children))
}

// isRetiring reports whether v's retirement close has not returned.
func (c *Children[V]) isRetiring(v any) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return contains(c.retiring, v)
}

// forget removes v from the instances being retired.
func (c *Children[V]) forget(v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i := slices.IndexFunc(c.retiring, func(r any) bool { return SameInstance(r, v) }); i >= 0 {
		c.retiring = slices.Delete(c.retiring, i, i+1)
	}
}

// contains reports whether vs holds the instance v.
func contains(vs []any, v any) bool {
	return slices.ContainsFunc(vs, func(r any) bool { return SameInstance(r, v) })
}

// SameInstance reports whether a and b are the same instance: equal
// values of one comparable dynamic type (a pointer, mostly). Values that
// cannot be compared are never the same, so == never panics here. It is
// the identity check of every lifecycle that closes once per instance.
func SameInstance(a, b any) bool {
	if a == nil || b == nil || reflect.TypeOf(a) != reflect.TypeOf(b) {
		return false
	}
	if !reflect.ValueOf(a).Comparable() || !reflect.ValueOf(b).Comparable() {
		return false
	}
	return a == b
}
