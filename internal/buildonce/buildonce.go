// Package buildonce builds a value per key at most once at a time, with no
// lock held while the build runs. A component that builds values lazily by
// name (the cache manager's stores, the notification manager's channels)
// runs user code in the build (a driver factory, a logger), and user code
// can call back into the component: holding the component's lock across
// it deadlocks, and building twice under a race runs user code twice.
//
// It imports the standard library and internal/goroutine only.
package buildonce

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/velocitykode/velocity/internal/goroutine"
)

// Group builds values by key. The zero value is ready to use. Safe for
// concurrent use.
type Group[V any] struct {
	mu    sync.Mutex
	calls map[string]*call[V]
}

// call is one build in progress.
type call[V any] struct {
	owner uint64 // the goroutine running the build
	done  chan struct{}
	v     V
	err   error
}

// Do runs build for key on the calling goroutine, with no lock held, and
// returns its result. A concurrent Do for the same key does not build
// again: it waits for the build in progress and returns its result, or
// returns ctx.Err() when ctx ends first. A Do for a key from inside that
// key's own build (the same goroutine) returns an error at once instead of
// waiting on itself. A build started on another goroutine that the build
// itself waits for cannot be detected, and deadlocks.
//
// Once a build returns, the key is free again: the next Do builds anew, so
// the caller publishes a successful value where later lookups find it
// before build returns. A build that panics frees the key too: the panic
// reaches Do's caller, and concurrent waiters get it as an error.
//
// The errors Do returns itself (a re-entrant call, a panicked build) do not
// name the key: the caller wraps them with what the key names.
func (g *Group[V]) Do(ctx context.Context, key string, build func() (V, error)) (V, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	id := goroutine.ID()
	g.mu.Lock()
	if c, ok := g.calls[key]; ok {
		g.mu.Unlock()
		if c.owner == id {
			var zero V
			return zero, errors.New("requested from inside its own build")
		}
		select {
		case <-c.done:
			return c.v, c.err
		case <-ctx.Done():
			var zero V
			return zero, ctx.Err()
		}
	}
	c := &call[V]{owner: id, done: make(chan struct{})}
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
		c.err = fmt.Errorf("the build panicked: %v", p)
		g.finish(key, c)
		panic(p)
	}()
	c.v, c.err = build()
	finished = true
	return c.v, c.err
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
