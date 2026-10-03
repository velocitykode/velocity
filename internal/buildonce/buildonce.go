// Package buildonce builds a value per key at most once at a time, with no
// lock held while the build runs. A component that builds values lazily by
// name (the cache manager's stores, the notification manager's channels)
// runs user code in the build (a driver factory, a logger), and user code
// can call back into the component: holding the component's lock across
// it deadlocks, and building twice under a race runs user code twice.
//
// Serial is the second shape of the same idea, for a value whose
// transitions must not overlap: it runs functions one at a time, each
// caller its own, again with no lock held while a function runs.
//
// It imports the standard library, internal/goroutine and
// internal/panicerr only.
package buildonce

import (
	"context"
	"errors"
	"sync"

	"github.com/velocitykode/velocity/internal/goroutine"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// Group builds values by key. The zero value is ready to use. Safe for
// concurrent use.
type Group[V any] struct {
	mu    sync.Mutex
	calls map[string]*call[V]
}

// call is one build in progress.
type call[V any] struct {
	joined int // callers that waited on it, under Group.mu
	done   chan struct{}
	v      V
	err    error
}

// inside is goroutine.Inside; a test counts its calls to prove an
// uncontended Do walks no stack.
var inside = goroutine.Inside

// buildFrame names runBuild, the frame every build runs under, for the
// re-entry check (goroutine.Inside).
var buildFrame = goroutine.FuncName(runBuild)

// runBuild runs a build: its frame on a goroutine's stack says the
// goroutine is inside a build.
func runBuild(build func()) { build() }

// Do runs build for key on the calling goroutine, with no lock held, and
// returns its result. A concurrent Do for the same key does not build
// again: it waits for the build in progress and returns its result, or
// returns ctx.Err() when ctx ends first. A Do that finds its key's build
// in progress while the calling goroutine is itself inside a build (that
// one, or one of another key, of any Group) returns an error at once
// instead of waiting: waiting on its own build would never end, and
// waiting from inside another build is how two builds that each need the
// other deadlock. The check walks the caller's stack, so it runs on that
// contended path only: an uncontended Do walks no stack. A build started
// on a goroutine the build itself spawned and waits for cannot be
// detected, and deadlocks.
//
// Once a build returns, the key is free again: the next Do builds anew, so
// the caller publishes a successful value where later lookups find it
// before build returns. A build that panics frees the key too: the panic
// reaches Do's caller, and concurrent waiters get it as a *panicerr.Error
// holding the raw panic value.
//
// The errors Do returns itself (a re-entrant call, a panicked build) do not
// name the key: the caller wraps them with what the key names.
func (g *Group[V]) Do(ctx context.Context, key string, build func() (V, error)) (V, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	g.mu.Lock()
	if c, ok := g.calls[key]; ok {
		g.mu.Unlock()
		// The stack is walked with no lock held; a build that ends
		// meanwhile has closed c.done, so the wait below returns at once.
		if inside(buildFrame) {
			var zero V
			return zero, errors.New("requested from inside its own build, or from inside another build while this one is in progress")
		}
		g.mu.Lock()
		c.joined++
		g.mu.Unlock()
		select {
		case <-c.done:
			return c.v, c.err
		case <-ctx.Done():
			var zero V
			return zero, ctx.Err()
		}
	}
	c := &call[V]{done: make(chan struct{})}
	if g.calls == nil {
		g.calls = make(map[string]*call[V])
	}
	g.calls[key] = c
	g.mu.Unlock()

	finished := false
	defer func() {
		if finished {
			g.finish(key, c)
			return
		}
		p := recover()
		if p == nil {
			// runtime.Goexit: the build never returned.
			c.err = errors.New("the build did not return")
			g.finish(key, c)
			return
		}
		// The raw value, formatted only when the error is read: a value
		// whose String or Error blocks must not keep the key held.
		c.err = panicerr.FromRecovered(p)
		g.finish(key, c)
		panic(p)
	}()
	runBuild(func() { c.v, c.err = build() })
	finished = true
	return c.v, c.err
}

// Joined returns how many callers have joined the build of key in
// progress to wait for its result, 0 when none is in progress. A test uses
// it to know every caller is waiting before it lets the build finish.
func (g *Group[V]) Joined(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c, ok := g.calls[key]; ok {
		return c.joined
	}
	return 0
}

// finish frees key and wakes the waiters of c.
func (g *Group[V]) finish(key string, c *call[V]) {
	g.mu.Lock()
	if g.calls[key] == c {
		delete(g.calls, key)
	}
	g.mu.Unlock()
	close(c.done)
}
