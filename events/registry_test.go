package events

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// Off removes a listener under every name it was registered for, however
// the names repeat.
func TestOff_RemovesTheListenerUnderEveryName(t *testing.T) {
	for _, names := range [][]string{{"a", "b"}, {"b", "a"}, {"a", "a"}, {"a", "b", "a", "x.*"}} {
		t.Run(fmt.Sprint(names), func(t *testing.T) {
			d := NewDispatcher()
			l := &cacheCountListener{}
			id := d.Listen(names, l)
			if !d.Off(id) {
				t.Fatal("Off reported the listener missing")
			}
			for _, name := range []string{"a", "b", "x.y"} {
				_ = d.Dispatch(context.Background(), name)
			}
			if got := atomic.LoadInt32(&l.count); got != 0 {
				t.Errorf("the removed listener ran %d times", got)
			}
			if d.Off(id) {
				t.Error("a second Off reported the listener removed again")
			}
		})
	}
}

// Flush of one name leaves a listener registered under others in place,
// and Off still removes it there.
func TestFlush_LeavesTheListenersOtherNamesRemovable(t *testing.T) {
	d := NewDispatcher()
	l := &cacheCountListener{}
	id := d.Listen([]string{"a", "b"}, l)
	d.Flush("a")
	_ = d.Dispatch(context.Background(), "b")
	if got := atomic.LoadInt32(&l.count); got != 1 {
		t.Fatalf("listener under the name Flush left ran %d times, want 1", got)
	}
	if !d.Off(id) {
		t.Fatal("Off after Flush reported the listener missing")
	}
	_ = d.Dispatch(context.Background(), "b")
	if got := atomic.LoadInt32(&l.count); got != 1 {
		t.Errorf("the removed listener ran again (count %d)", got)
	}
}

// The resolved-listener cache is cleared by every Listen, Off and Flush,
// and holds at most maxResolvedEntries names: dispatching unbounded
// distinct names does not grow it past the bound, and names past the
// bound still reach their listeners.
func TestResolvedCache_IsBoundedAndClearedByMutations(t *testing.T) {
	d := NewDispatcher()
	wild := &cacheCountListener{}
	d.Listen("user.*", wild)
	const names = 3 * maxResolvedEntries
	for i := range names {
		_ = d.Dispatch(context.Background(), fmt.Sprintf("user.%d", i))
	}
	if got := atomic.LoadInt32(&wild.count); got != names {
		t.Fatalf("wildcard listener ran %d times, want %d", got, names)
	}
	if got := resolvedEntries(d); got > maxResolvedEntries {
		t.Fatalf("cache holds %d entries, want at most %d", got, maxResolvedEntries)
	}
	id := d.Listen("other", &cacheCountListener{})
	if got := resolvedEntries(d); got != 0 {
		t.Errorf("cache holds %d entries after Listen, want 0", got)
	}
	_ = d.Dispatch(context.Background(), "user.1")
	d.Off(id)
	if got := resolvedEntries(d); got != 0 {
		t.Errorf("cache holds %d entries after Off, want 0", got)
	}
	_ = d.Dispatch(context.Background(), "user.1")
	d.Flush("nothing")
	if got := resolvedEntries(d); got != 0 {
		t.Errorf("cache holds %d entries after Flush, want 0", got)
	}
}

func resolvedEntries(d *DefaultDispatcher) int {
	n := 0
	d.resolvedCache.Range(func(any, any) bool { n++; return true })
	return n
}

// Listen, Off and Flush race dispatches from many goroutines. Each writer
// registers under a shared name and names of its own, and a dispatch of
// its own name after Off returned never reaches the removed listener.
// Run under -race.
func TestRegistry_ConcurrentMutationAndDispatch(t *testing.T) {
	d := NewDispatcher()
	d.Listen("shared.*", &cacheCountListener{})
	var wg sync.WaitGroup
	for g := range 8 {
		own := fmt.Sprintf("own.%d", g)
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := range 300 {
				var ownRuns atomic.Int32
				l := listenerFunc(func(_ context.Context, e interface{}) error {
					if e == own {
						ownRuns.Add(1)
					}
					return nil
				})
				id := d.Listen([]string{"shared.x", own, own + ".extra"}, l)
				_ = d.Dispatch(context.Background(), own)
				if i%5 == 0 {
					d.Flush(own + ".extra")
				}
				d.Off(id)
				before := ownRuns.Load()
				_ = d.Dispatch(context.Background(), own)
				if ownRuns.Load() != before {
					t.Error("a listener ran for a dispatch that started after Off returned")
				}
			}
		}()
		go func() {
			defer wg.Done()
			for range 600 {
				_ = d.Dispatch(context.Background(), "shared.x")
				_ = d.HasListeners("shared.x")
			}
		}()
	}
	wg.Wait()
}
